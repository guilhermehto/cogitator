package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/goliveira/opencode-monitor/internal/discovery"
	"github.com/goliveira/opencode-monitor/internal/oc"
)

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
	s := New(ctx)
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
	s := New(ctx)
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

func TestSnapshotSortBreaksLastActivityTiesBySessionID(t *testing.T) {
	ctx := context.Background()
	s := New(ctx)
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
	s := New(ctx)
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
	s := New(ctx)
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
	s := New(ctx)
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
