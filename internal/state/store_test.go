package state

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/discovery"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/oc"
	"github.com/guilhermehto/cogitator/internal/provider"
)

func newTestStore(ctx context.Context) *Store {
	return New(ctx, config.Default(), slog.Default())
}

func makeSession(id string, updatedMs int64) oc.Session {
	var s oc.Session
	s.ID = id
	s.Title = id
	s.Time.Updated = updatedMs
	return s
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestSyncRecentPrunesOnlyRecentRows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)

	s.SyncRecent(inst.ID, []oc.Session{makeSession("A", 1_000), makeSession("B", 1_000)})

	s.ApplyEvent(inst.ID, oc.Event{
		Type:       "session.status",
		Properties: mustJSON(t, oc.SessionStatusEvt{SessionID: "A", Status: oc.Status{Type: "busy"}}),
	})

	s.SyncRecent(inst.ID, []oc.Session{makeSession("A", 2_000)})

	snap := s.snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session after pruning, got %d", len(snap.Sessions))
	}
	if snap.Sessions[0].SessionID != "A" {
		t.Fatalf("expected remaining session A, got %q", snap.Sessions[0].SessionID)
	}
	if snap.Sessions[0].Source != SourceLive {
		t.Fatalf("expected remaining session to stay live, got %q", snap.Sessions[0].Source)
	}
}

func TestApplyEventUnknownTypeDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)

	ch := s.Subscribe()
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected initial snapshot")
	}

	s.ApplyEvent(inst.ID, oc.Event{Type: "server.heartbeat", Properties: mustJSON(t, map[string]any{})})

	select {
	case <-ch:
		t.Fatal("unexpected publish for unknown/no-op event")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSnapshotCarriesCreated(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)

	sess := makeSession("A", 2_000)
	sess.Time.Created = 1_700_000
	s.SyncRecent(inst.ID, []oc.Session{sess})

	snap := s.snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(snap.Sessions))
	}
	want := time.UnixMilli(1_700_000)
	if !snap.Sessions[0].Created.Equal(want) {
		t.Fatalf("Created = %v, want %v", snap.Sessions[0].Created, want)
	}
}

func TestSnapshotCreatedZeroWhenAbsent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)

	// makeSession does not set Time.Created, so it stays zero on the
	// wire. The view must mirror that so the renderer's fallback path
	// (LastActivity DESC) kicks in instead of treating the row as
	// "born at the Unix epoch".
	s.SyncRecent(inst.ID, []oc.Session{makeSession("A", 2_000)})

	snap := s.snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(snap.Sessions))
	}
	if !snap.Sessions[0].Created.IsZero() {
		t.Fatalf("Created = %v, want zero", snap.Sessions[0].Created)
	}
}

func TestSnapshotSortBreaksLastActivityTiesBySessionID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)

	s.SyncRecent(inst.ID, []oc.Session{makeSession("b", 1_000), makeSession("a", 1_000)})

	snap := s.snapshot()
	if len(snap.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(snap.Sessions))
	}
	if snap.Sessions[0].SessionID != "a" || snap.Sessions[1].SessionID != "b" {
		t.Fatalf("unexpected order: %q, %q", snap.Sessions[0].SessionID, snap.Sessions[1].SessionID)
	}
}

func TestApplyEventQuestionPendingLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)
	s.SyncRecent(inst.ID, []oc.Session{makeSession("S1", 1_000)})

	applyQuestion := func(callID, status string) {
		t.Helper()
		s.ApplyEvent(inst.ID, oc.Event{
			Type: "message.part.updated",
			Properties: mustJSON(t, map[string]any{
				"part": map[string]any{
					"sessionID": "S1",
					"type":      "tool",
					"tool":      "question",
					"callID":    callID,
					"state": map[string]any{
						"status": status,
					},
				},
			}),
		})
	}

	assertAttention := func(want Attention) {
		t.Helper()
		snap := s.snapshot()
		if len(snap.Sessions) != 1 {
			t.Fatalf("expected 1 session, got %d", len(snap.Sessions))
		}
		if got := snap.Sessions[0].Attention; got != want {
			t.Fatalf("attention = %q, want %q", got, want)
		}
	}

	assertAttention(AttnInactive)

	applyQuestion("call-1", "pending")
	assertAttention(AttnQuestionPending)

	applyQuestion("call-1", "running")
	assertAttention(AttnQuestionPending)

	applyQuestion("call-2", "pending")
	assertAttention(AttnQuestionPending)

	applyQuestion("call-1", "completed")
	assertAttention(AttnQuestionPending)

	applyQuestion("call-2", "error")
	assertAttention(AttnInactive)
}
func TestSnapshotDedupesSessionAcrossInstances(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	instA := discovery.Instance{ID: "127.0.0.1:1111", Host: "127.0.0.1", Port: 1111}
	instB := discovery.Instance{ID: "127.0.0.1:2222", Host: "127.0.0.1", Port: 2222}
	s.AddInstance(instA)
	s.AddInstance(instB)

	// Both processes serve the same project, so /session returns the same
	// session ID on each. Each instance therefore lands a SourceRecent row.
	shared := []oc.Session{makeSession("ses_dup", 1_000)}
	s.SyncRecent(instA.ID, shared)
	s.SyncRecent(instB.ID, shared)

	// Only one process holds the user's TUI session, so only one SSE
	// event arrives — that instance's row gets promoted to SourceLive.
	s.ApplyEvent(instB.ID, oc.Event{
		Type:       "session.status",
		Properties: mustJSON(t, oc.SessionStatusEvt{SessionID: "ses_dup", Status: oc.Status{Type: "busy"}}),
	})

	snap := s.snapshot()
	count := 0
	var winner SessionView
	for _, sv := range snap.Sessions {
		if sv.SessionID == "ses_dup" {
			count++
			winner = sv
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 row for ses_dup after dedupe, got %d", count)
	}
	if winner.Source != SourceLive {
		t.Fatalf("expected live row to win dedupe, got source %q", winner.Source)
	}
	if winner.InstanceID != instB.ID {
		t.Fatalf("expected live instance %q to win, got %q", instB.ID, winner.InstanceID)
	}
}

func TestSnapshotDedupePicksMostRecentWhenSameSource(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	instA := discovery.Instance{ID: "127.0.0.1:1111", Host: "127.0.0.1", Port: 1111}
	instB := discovery.Instance{ID: "127.0.0.1:2222", Host: "127.0.0.1", Port: 2222}
	s.AddInstance(instA)
	s.AddInstance(instB)

	// Both rows are SourceRecent (no SSE event for either) but instB's
	// /session response carried a fresher Time.Updated. The dedupe should
	// pick the row with the more recent LastActivity within the same source.
	s.SyncRecent(instA.ID, []oc.Session{makeSession("ses_dup", 1_000)})
	s.SyncRecent(instB.ID, []oc.Session{makeSession("ses_dup", 5_000)})

	snap := s.snapshot()
	count := 0
	var winner SessionView
	for _, sv := range snap.Sessions {
		if sv.SessionID == "ses_dup" {
			count++
			winner = sv
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 row for ses_dup after dedupe, got %d", count)
	}
	if winner.InstanceID != instB.ID {
		t.Fatalf("expected fresher instance %q to win, got %q", instB.ID, winner.InstanceID)
	}
	wantActivity := time.UnixMilli(5_000)
	if !winner.LastActivity.Equal(wantActivity) {
		t.Fatalf("expected LastActivity %v, got %v", wantActivity, winner.LastActivity)
	}
}

func TestRecordInstanceErrorAndSuccessLifecycle(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.UnreachableThreshold = 2
	s := New(ctx, cfg, slog.Default())
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 7777}
	s.AddInstance(inst)

	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }
	s.RecordInstanceError(inst.ID, context.DeadlineExceeded)

	snap := s.snapshot()
	if len(snap.UnreachableInstances) != 0 {
		t.Fatalf("unexpected unreachable instances after first error: %+v", snap.UnreachableInstances)
	}

	now = now.Add(time.Second)
	s.RecordInstanceError(inst.ID, context.DeadlineExceeded)
	snap = s.snapshot()
	if len(snap.UnreachableInstances) != 1 {
		t.Fatalf("expected one unreachable instance at threshold, got %+v", snap.UnreachableInstances)
	}
	f := snap.UnreachableInstances[0]
	if f.InstanceID != inst.ID || f.ConsecutiveFailures != 2 || f.Host != "127.0.0.1" || f.Port != 7777 {
		t.Fatalf("unexpected failure entry: %+v", f)
	}

	s.RecordInstanceSuccess(inst.ID)
	snap = s.snapshot()
	if len(snap.UnreachableInstances) != 0 {
		t.Fatalf("expected unreachable list to clear after success, got %+v", snap.UnreachableInstances)
	}
}

func TestSnapshotDedupePrefersHealthyInstance(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.UnreachableThreshold = 2
	s := New(ctx, cfg, slog.Default())
	instA := discovery.Instance{ID: "127.0.0.1:1111", Host: "127.0.0.1", Port: 1111}
	instB := discovery.Instance{ID: "127.0.0.1:2222", Host: "127.0.0.1", Port: 2222}
	s.AddInstance(instA)
	s.AddInstance(instB)

	shared := []oc.Session{makeSession("ses_dup", 1_000)}
	s.SyncRecent(instA.ID, shared)
	s.SyncRecent(instB.ID, shared)

	// instA wins by source (live) before reachability is considered.
	s.ApplyEvent(instA.ID, oc.Event{
		Type:       "session.status",
		Properties: mustJSON(t, oc.SessionStatusEvt{SessionID: "ses_dup", Status: oc.Status{Type: "busy"}}),
	})

	// Once instA crosses the unreachable threshold, dedupe should pick instB.
	s.RecordInstanceError(instA.ID, context.DeadlineExceeded)
	s.RecordInstanceError(instA.ID, context.DeadlineExceeded)

	snap := s.snapshot()
	count := 0
	var winner SessionView
	for _, sv := range snap.Sessions {
		if sv.SessionID == "ses_dup" {
			count++
			winner = sv
		}
	}
	if count != 1 {
		t.Fatalf("expected one deduped row, got %d", count)
	}
	if winner.InstanceID != instB.ID {
		t.Fatalf("expected healthy instance %q to win dedupe, got %q", instB.ID, winner.InstanceID)
	}
	if len(snap.UnreachableInstances) != 1 || snap.UnreachableInstances[0].InstanceID != instA.ID {
		t.Fatalf("expected only instA in unreachable list, got %+v", snap.UnreachableInstances)
	}
}

// status drives the oc-row attention through busy then idle.
func applyStatus(t *testing.T, s *Store, instID, sid, statusType string) {
	t.Helper()
	s.ApplyEvent(instID, oc.Event{
		Type:       "session.status",
		Properties: mustJSON(t, oc.SessionStatusEvt{SessionID: sid, Status: oc.Status{Type: statusType}}),
	})
}

func attentionOf(t *testing.T, s *Store, sid string) Attention {
	t.Helper()
	for _, sv := range s.snapshot().Sessions {
		if sv.SessionID == sid {
			return sv.Attention
		}
	}
	t.Fatalf("session %q not found in snapshot", sid)
	return ""
}

func TestFinishedLifecycleOpenCode(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)
	s.SyncRecent(inst.ID, []oc.Session{makeSession("S1", 1_000)})

	// Idle from discovery, never active → must not show finished.
	if got := attentionOf(t, s, "S1"); got != AttnInactive {
		t.Fatalf("fresh idle: got %q, want %q", got, AttnInactive)
	}

	// User requested something: agent goes busy.
	applyStatus(t, s, inst.ID, "S1", "busy")
	if got := attentionOf(t, s, "S1"); got != AttnActive {
		t.Fatalf("busy: got %q, want %q", got, AttnActive)
	}

	// Agent finishes → idle. Now finished, because it was active.
	applyStatus(t, s, inst.ID, "S1", "idle")
	if got := attentionOf(t, s, "S1"); got != AttnFinished {
		t.Fatalf("active→idle: got %q, want %q", got, AttnFinished)
	}

	// User views the session → finished clears back to inactive.
	s.MarkViewed("opencode", inst.ID, "S1")
	if got := attentionOf(t, s, "S1"); got != AttnInactive {
		t.Fatalf("after MarkViewed: got %q, want %q", got, AttnInactive)
	}

	// Loop repeats: active again, idle again → finished again.
	applyStatus(t, s, inst.ID, "S1", "busy")
	applyStatus(t, s, inst.ID, "S1", "idle")
	if got := attentionOf(t, s, "S1"); got != AttnFinished {
		t.Fatalf("second loop: got %q, want %q", got, AttnFinished)
	}
}

func TestFinishedMarkViewedScansAllInstancesWhenIDEmpty(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)
	s.SyncRecent(inst.ID, []oc.Session{makeSession("S1", 1_000)})
	applyStatus(t, s, inst.ID, "S1", "busy")
	applyStatus(t, s, inst.ID, "S1", "idle")
	if got := attentionOf(t, s, "S1"); got != AttnFinished {
		t.Fatalf("precondition: got %q, want %q", got, AttnFinished)
	}
	// The workspace Row carries no instance id; MarkViewed must still find it.
	s.MarkViewed("opencode", "", "S1")
	if got := attentionOf(t, s, "S1"); got != AttnInactive {
		t.Fatalf("empty-instance MarkViewed: got %q, want %q", got, AttnInactive)
	}
}

func TestFinishedSupersededByPendingRequest(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)
	s.SyncRecent(inst.ID, []oc.Session{makeSession("S1", 1_000)})
	applyStatus(t, s, inst.ID, "S1", "busy")
	applyStatus(t, s, inst.ID, "S1", "idle")
	if got := attentionOf(t, s, "S1"); got != AttnFinished {
		t.Fatalf("precondition: got %q, want %q", got, AttnFinished)
	}
	// A pending permission is the live reason to look — it must win over finished.
	s.ApplyEvent(inst.ID, oc.Event{
		Type:       "permission.asked",
		Properties: mustJSON(t, oc.PermissionRequest{ID: "p1", SessionID: "S1"}),
	})
	if got := attentionOf(t, s, "S1"); got != AttnPermissionPending {
		t.Fatalf("pending perm over finished: got %q, want %q", got, AttnPermissionPending)
	}
}

func TestFinishedLifecycleProvider(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)

	apply := func(statusType string) {
		s.ApplyUpdate(provider.SessionUpdate{
			Provider:   harness.Kind("codex"),
			InstanceID: "codex",
			SessionID:  "C1",
			StatusType: statusType,
			Source:     string(SourceLive),
		})
	}

	apply("busy")
	if got := attentionOf(t, s, "C1"); got != AttnActive {
		t.Fatalf("provider busy: got %q, want %q", got, AttnActive)
	}
	apply("idle")
	if got := attentionOf(t, s, "C1"); got != AttnFinished {
		t.Fatalf("provider active→idle: got %q, want %q", got, AttnFinished)
	}
	s.MarkViewed("codex", "codex", "C1")
	if got := attentionOf(t, s, "C1"); got != AttnInactive {
		t.Fatalf("provider MarkViewed: got %q, want %q", got, AttnInactive)
	}
}

func TestFinishedProviderSurvivesReplace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(ctx)
	codex := harness.Kind("codex")

	mk := func(statusType string) provider.SessionUpdate {
		return provider.SessionUpdate{
			Provider:   codex,
			InstanceID: "codex",
			SessionID:  "C1",
			StatusType: statusType,
			Source:     string(SourceLive),
		}
	}

	s.ReplaceProviderInstance(codex, "codex", []provider.SessionUpdate{mk("busy")})
	s.ReplaceProviderInstance(codex, "codex", []provider.SessionUpdate{mk("idle")})
	if got := attentionOf(t, s, "C1"); got != AttnFinished {
		t.Fatalf("provider replace active→idle: got %q, want %q", got, AttnFinished)
	}
	// A subsequent identical-idle refresh must not lose the finished badge.
	s.ReplaceProviderInstance(codex, "codex", []provider.SessionUpdate{mk("idle")})
	if got := attentionOf(t, s, "C1"); got != AttnFinished {
		t.Fatalf("finished lost on identical refresh: got %q, want %q", got, AttnFinished)
	}
}
