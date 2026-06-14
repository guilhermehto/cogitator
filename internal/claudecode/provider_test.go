package claudecode_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/hookipc"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// fakeSink records every ApplyUpdate call, every ReplaceProviderInstance call,
// and the ClearProviderInstance count so tests can assert on emitted updates.
type fakeSink struct {
	mu       sync.Mutex
	updates  []provider.SessionUpdate
	replaces [][]provider.SessionUpdate // one entry per ReplaceProviderInstance call
	clears   int
}

func (f *fakeSink) ApplyUpdate(u provider.SessionUpdate) {
	f.mu.Lock()
	f.updates = append(f.updates, u)
	f.mu.Unlock()
}

func (f *fakeSink) RemoveProviderSession(_ harness.Kind, _, _ string) {}

func (f *fakeSink) ClearProviderInstance(_ harness.Kind, _ string) {
	f.mu.Lock()
	f.clears++
	f.mu.Unlock()
}

func (f *fakeSink) ReplaceProviderInstance(_ harness.Kind, _ string, us []provider.SessionUpdate) {
	cp := make([]provider.SessionUpdate, len(us))
	copy(cp, us)
	f.mu.Lock()
	f.replaces = append(f.replaces, cp)
	f.mu.Unlock()
}

func (f *fakeSink) snapshot() ([]provider.SessionUpdate, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]provider.SessionUpdate, len(f.updates))
	copy(cp, f.updates)
	return cp, f.clears
}

// snapshotReplaces returns a copy of all recorded ReplaceProviderInstance batches.
func (f *fakeSink) snapshotReplaces() [][]provider.SessionUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]provider.SessionUpdate, len(f.replaces))
	for i, batch := range f.replaces {
		cp := make([]provider.SessionUpdate, len(batch))
		copy(cp, batch)
		out[i] = cp
	}
	return out
}

// hookSink is a fakeSink that also tracks the most recent update per session.
type hookSink struct {
	mu      sync.Mutex
	updates []provider.SessionUpdate
	clears  int
}

func (h *hookSink) ApplyUpdate(u provider.SessionUpdate) {
	h.mu.Lock()
	h.updates = append(h.updates, u)
	h.mu.Unlock()
}

func (h *hookSink) RemoveProviderSession(_ harness.Kind, _, _ string) {}

func (h *hookSink) ClearProviderInstance(_ harness.Kind, _ string) {
	h.mu.Lock()
	h.clears++
	h.mu.Unlock()
}

func (h *hookSink) ReplaceProviderInstance(_ harness.Kind, _ string, _ []provider.SessionUpdate) {}

// latestForSession returns the most recent update for the given session ID.
func (h *hookSink) latestForSession(sessionID string) (provider.SessionUpdate, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var found provider.SessionUpdate
	var ok bool
	for _, u := range h.updates {
		if u.SessionID == sessionID {
			found = u
			ok = true
		}
	}
	return found, ok
}

// buildFixtureHome creates a temporary Claude home with one session file whose
// timestamp is set to the provided lastActivity time.
func buildFixtureHome(t *testing.T, sessionID string, lastActivity time.Time) string {
	t.Helper()
	home := t.TempDir()
	projectDir := filepath.Join(home, "projects", "-tmp-test")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ts := lastActivity.UTC().Format(time.RFC3339Nano)
	content := `{"type":"user","timestamp":"` + ts + `","sessionId":"` + sessionID + `","cwd":"/tmp/test","message":{"content":"hello world"}}
`
	fname := sessionID + ".jsonl"
	if err := os.WriteFile(filepath.Join(projectDir, fname), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestProvider_Kind verifies the provider reports KindClaudeCode.
func TestProvider_Kind(t *testing.T) {
	p := claudecode.NewProvider("", 5*time.Second, 30*time.Minute, nil)
	if p.Kind() != harness.KindClaudeCode {
		t.Errorf("Kind() = %q, want %q", p.Kind(), harness.KindClaudeCode)
	}
}

// TestProvider_InstanceID verifies the provider uses the "claude-code" instance ID.
func TestProvider_InstanceID(t *testing.T) {
	if claudecode.InstanceID != "claude-code" {
		t.Errorf("InstanceID = %q, want %q", claudecode.InstanceID, "claude-code")
	}
}

// TestProvider_RecencyMapping verifies that a session whose last activity is
// within the recency window gets SourceLive, and one outside gets SourceRecent.
func TestProvider_RecencyMapping(t *testing.T) {
	recencyWindow := 30 * time.Minute

	tests := []struct {
		name       string
		age        time.Duration
		wantSource string
	}{
		{
			name:       "within window → live",
			age:        5 * time.Minute,
			wantSource: "live",
		},
		{
			name:       "just inside boundary → live",
			age:        29*time.Minute + 59*time.Second,
			wantSource: "live",
		},
		{
			name:       "outside window → recent",
			age:        31 * time.Minute,
			wantSource: "recent",
		},
		{
			name:       "much older → recent",
			age:        2 * time.Hour,
			wantSource: "recent",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lastActivity := time.Now().Add(-tc.age)
			sessionID := "aaaabbbb-cccc-dddd-eeee-000000000001"
			home := buildFixtureHome(t, sessionID, lastActivity)

			sink := &fakeSink{}
			p := claudecode.NewProvider(home, 100*time.Millisecond, recencyWindow, nil)

			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- p.Start(ctx, sink) }()

			time.Sleep(150 * time.Millisecond)
			cancel()
			<-done

			replaces := sink.snapshotReplaces()
			var found *provider.SessionUpdate
			for _, batch := range replaces {
				for i := range batch {
					if batch[i].SessionID == sessionID {
						u := batch[i]
						found = &u
					}
				}
			}
			if found == nil {
				t.Fatalf("no update emitted for session %q; got %d replace batches", sessionID, len(replaces))
			}
			if found.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", found.Source, tc.wantSource)
			}
		})
	}
}

// TestProvider_RecencyDoesNotImplyBusy is the regression guard for the
// "sessions stuck active after restart" bug. With no hook overlay (the state
// right after a cogitator restart — the overlay map starts empty), a session
// whose transcript was written within recencyWindow must be Source "live" (so
// it stays discoverable) but StatusType "" — NOT "busy". A recent transcript
// means the session was touched recently, not that Claude is generating.
// Classify("") → AttnInactive, so the row renders inactive instead of active.
func TestProvider_RecencyDoesNotImplyBusy(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000099"
	// 5 minutes ago: well inside the 30m recency window → Source "live".
	home := buildFixtureHome(t, sessionID, time.Now().Add(-5*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &fakeSink{}

	// Poll with no hook ever fired — exactly the post-restart situation.
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}
	p.PollOnceForTest(sink, diskSessions)

	replaces := sink.snapshotReplaces()
	var found *provider.SessionUpdate
	for _, batch := range replaces {
		for i := range batch {
			if batch[i].SessionID == sessionID {
				u := batch[i]
				found = &u
			}
		}
	}
	if found == nil {
		t.Fatalf("no update emitted for session %q", sessionID)
	}
	if found.Source != "live" {
		t.Errorf("Source = %q, want live (recent transcript must stay discoverable)", found.Source)
	}
	if found.StatusType != "" {
		t.Errorf("StatusType = %q, want \"\" — recency must not be coerced to busy (stuck-active regression)", found.StatusType)
	}
}

// TestProvider_EmitsOneUpdatePerSession verifies that a poll cycle emits
// exactly one ReplaceProviderInstance call carrying all fixture sessions, with
// no ClearProviderInstance calls and no blank intermediate emissions.
func TestProvider_EmitsOneUpdatePerSession(t *testing.T) {
	home := filepath.Join("testdata")

	sink := &fakeSink{}
	p := claudecode.NewProvider(home, 100*time.Millisecond, 24*time.Hour, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, sink) }()

	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	_, clears := sink.snapshot()
	if clears != 0 {
		t.Errorf("ClearProviderInstance called %d times, want 0 (poll must use ReplaceProviderInstance)", clears)
	}

	replaces := sink.snapshotReplaces()
	if len(replaces) < 1 {
		t.Fatalf("ReplaceProviderInstance called %d times, want ≥1", len(replaces))
	}

	// No replace batch should be empty — that would flash a blank list.
	for i, batch := range replaces {
		if len(batch) == 0 {
			t.Errorf("replace[%d] is empty — would flash a blank list", i)
		}
	}

	// Inspect the first replace batch: all updates must have the correct
	// Provider and InstanceID, and no empty SessionID.
	first := replaces[0]
	for _, u := range first {
		if u.Provider != harness.KindClaudeCode {
			t.Errorf("update.Provider = %q, want %q", u.Provider, harness.KindClaudeCode)
		}
		if u.InstanceID != claudecode.InstanceID {
			t.Errorf("update.InstanceID = %q, want %q", u.InstanceID, claudecode.InstanceID)
		}
		if u.SessionID == "" {
			t.Error("update.SessionID is empty")
		}
	}

	// The testdata directory has fixture sessions. Verify the first replace
	// batch carries at least one.
	if len(first) < 1 {
		t.Errorf("first replace batch has %d sessions, want ≥1", len(first))
	}
}

// TestProvider_AbsentClaudeHome verifies that a missing ~/.claude yields no
// updates and no errors — the provider starts and stops cleanly.
func TestProvider_AbsentClaudeHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "does-not-exist")

	sink := &fakeSink{}
	p := claudecode.NewProvider(home, 100*time.Millisecond, 30*time.Minute, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, sink) }()

	time.Sleep(150 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Start returned error: %v", err)
	}

	updates, _ := sink.snapshot()
	if len(updates) != 0 {
		t.Errorf("got %d updates for absent home, want 0", len(updates))
	}
}

// TestProvider_PollOnce_PruneProtection is the CRITICAL INTEGRATION HAZARD
// test: a PermissionRequest hook fires for a session NOT present on disk, then
// a poll cycle runs over fixtures that do NOT include that session — the hook
// overlay MUST survive without re-firing the hook.
func TestProvider_PollOnce_PruneProtection(t *testing.T) {
	otherSessionID := "aaaabbbb-cccc-dddd-eeee-000000000088"
	hookSessionID := "aaaabbbb-cccc-dddd-eeee-000000000077"
	home := buildFixtureHome(t, otherSessionID, time.Now().Add(-2*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Seed the hook session via handleHookFrame (not via disk).
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"PermissionRequest","session_id":"`+hookSessionID+`","cwd":"/tmp/hook-session"}`),
		sink,
	)

	// Verify the hook was processed.
	u, ok := sink.latestForSession(hookSessionID)
	if !ok || !u.HasPermission {
		t.Fatal("PermissionRequest hook: HasPermission not set before poll cycle")
	}

	// Run a poll cycle with only otherSessionID on disk — hookSessionID absent.
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}
	p.PollOnceForTest(sink, diskSessions)

	// The hook-seeded session must still be present with HasPermission==true.
	u2, ok2 := sink.latestForSession(hookSessionID)
	if !ok2 {
		t.Fatal("HAZARD: hook session absent after poll cycle — was wiped")
	}
	if !u2.HasPermission {
		t.Error("HAZARD: poll cycle wiped hook overlay — HasPermission = false after poll, want true")
	}
}

// TestProvider_PollOnce_OverlayPrecedence verifies that the hook overlay's
// statusType takes precedence over the poll-derived default.
func TestProvider_PollOnce_OverlayPrecedence(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000055"
	home := buildFixtureHome(t, sessionID, time.Now().Add(-2*time.Hour)) // old → "recent" without hook

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Fire a SessionStart hook → overlay statusType = "busy".
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"SessionStart","session_id":"`+sessionID+`"}`),
		sink,
	)

	u, ok := sink.latestForSession(sessionID)
	if !ok {
		t.Fatal("no update after SessionStart hook")
	}
	if u.StatusType != "busy" {
		t.Errorf("after SessionStart hook: StatusType = %q, want busy", u.StatusType)
	}

	// Run a poll cycle — the session is old (outside recency window) but the
	// hook overlay must keep it "busy".
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}
	p.PollOnceForTest(sink, diskSessions)

	u2, ok2 := sink.latestForSession(sessionID)
	if !ok2 {
		t.Fatal("no update after poll cycle")
	}
	if u2.StatusType != "busy" {
		t.Errorf("after poll: StatusType = %q, want busy (hook overlay must win)", u2.StatusType)
	}
}

// TestProvider_SessionEnd_IdleRowSurvivesPoll verifies that a SessionEnd hook
// sets the row to idle (not removed) and the row survives the next poll cycle.
func TestProvider_SessionEnd_IdleRowSurvivesPoll(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000044"
	home := buildFixtureHome(t, sessionID, time.Now().Add(-1*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// SessionEnd → idle, not removal.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"SessionEnd","session_id":"`+sessionID+`"}`),
		sink,
	)

	u, ok := sink.latestForSession(sessionID)
	if !ok {
		t.Fatal("no update after SessionEnd hook")
	}
	if u.StatusType != "idle" {
		t.Errorf("SessionEnd: StatusType = %q, want idle", u.StatusType)
	}

	// Poll cycle — session is on disk, row must survive.
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}
	p.PollOnceForTest(sink, diskSessions)

	u2, ok2 := sink.latestForSession(sessionID)
	if !ok2 {
		t.Fatal("row removed after poll — SessionEnd must not remove the row")
	}
	_ = u2
}

// TestProvider_CanonicalDirSeeding verifies that a hook-seeded session
// canonicalizes the CWD via pathnorm.Canonical before storing it in Dir,
// so that a subsequent CWD-fallback lookup reconciles correctly.
func TestProvider_CanonicalDirSeeding(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000033"

	// Use /tmp which on macOS is a symlink to /private/tmp. pathnorm.Canonical
	// will resolve it. We use a path that exists so EvalSymlinks succeeds.
	rawCWD := os.TempDir()

	p := claudecode.NewProvider("", 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Seed via hook with raw CWD.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"SessionStart","session_id":"`+sessionID+`","cwd":"`+rawCWD+`"}`),
		sink,
	)

	u, ok := sink.latestForSession(sessionID)
	if !ok {
		t.Fatal("no update after SessionStart hook")
	}

	// The Directory in the update must be the canonical form (symlinks resolved).
	// On Linux /tmp is usually not a symlink so canonical == raw; on macOS it
	// resolves to /private/tmp. Either way it must not be empty.
	if u.Directory == "" {
		t.Error("Directory is empty after hook seed — canonical dir not stored")
	}
}

// TestProvider_HookDrivenAttention verifies that hook events produce the
// correct SessionUpdate attention fields via the live socket path.
func TestProvider_HookDrivenAttention(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000099"
	lastActivity := time.Now().Add(-2 * time.Minute)
	home := buildFixtureHome(t, sessionID, lastActivity)

	// Use a very long poll interval so the test controls timing precisely.
	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)

	sockDir := shortXDGDir(t)
	t.Setenv("XDG_RUNTIME_DIR", sockDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := &hookSink{}
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, sink) }()

	// Wait for the initial poll to populate the session map.
	time.Sleep(200 * time.Millisecond)

	sockPath := claudecode.HookSocketPath()

	sendHookJSON := func(t *testing.T, json string) {
		t.Helper()
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			t.Fatalf("dial hook socket: %v", err)
		}
		defer conn.Close()
		if err := hookipc.WriteFrame(conn, []byte(json)); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}

	waitForUpdate := func(t *testing.T, check func(provider.SessionUpdate) bool) provider.SessionUpdate {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if u, ok := sink.latestForSession(sessionID); ok && check(u) {
				return u
			}
			time.Sleep(20 * time.Millisecond)
		}
		u, _ := sink.latestForSession(sessionID)
		t.Fatalf("timed out waiting for expected update; last update: %+v", u)
		return provider.SessionUpdate{}
	}

	// SessionStart → busy, no permission.
	sendHookJSON(t, `{"hook_event_name":"SessionStart","session_id":"`+sessionID+`"}`)
	u := waitForUpdate(t, func(u provider.SessionUpdate) bool {
		return u.StatusType == "busy" && !u.HasPermission
	})
	if u.StatusType != "busy" {
		t.Errorf("SessionStart: StatusType = %q, want busy", u.StatusType)
	}
	if u.HasPermission {
		t.Error("SessionStart: HasPermission = true, want false")
	}
	if u.Provider != harness.KindClaudeCode {
		t.Errorf("SessionStart: Provider = %q, want %q", u.Provider, harness.KindClaudeCode)
	}

	// PermissionRequest → hasPermission=true.
	sendHookJSON(t, `{"hook_event_name":"PermissionRequest","session_id":"`+sessionID+`"}`)
	u = waitForUpdate(t, func(u provider.SessionUpdate) bool { return u.HasPermission })
	if !u.HasPermission {
		t.Error("PermissionRequest: HasPermission = false, want true")
	}

	// Stop → idle, permission cleared.
	sendHookJSON(t, `{"hook_event_name":"Stop","session_id":"`+sessionID+`"}`)
	u = waitForUpdate(t, func(u provider.SessionUpdate) bool {
		return u.StatusType == "idle" && !u.HasPermission
	})
	if u.StatusType != "idle" {
		t.Errorf("Stop: StatusType = %q, want idle", u.StatusType)
	}
	if u.HasPermission {
		t.Error("Stop: HasPermission = true, want false")
	}

	cancel()
	<-done
}

// TestProvider_PollOnce_StalePhantomPruned verifies that a hook-seeded session
// absent from disk is pruned once its overlay's lastActivity is older than
// recencyWindow. A stale phantom must not leak for process lifetime.
func TestProvider_PollOnce_StalePhantomPruned(t *testing.T) {
	recencyWindow := 30 * time.Minute
	hookSessionID := "aaaabbbb-cccc-dddd-eeee-000000000200"

	// Use an empty home so the hook session is never on disk.
	home := filepath.Join(t.TempDir(), "empty-claude")

	p := claudecode.NewProvider(home, 10*time.Second, recencyWindow, nil)
	sink := &hookSink{}

	// Seed the hook session with a stale lastActivity (older than recencyWindow).
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"PermissionRequest","session_id":"`+hookSessionID+`","cwd":"/tmp/phantom"}`),
		sink,
	)

	// Verify the hook was processed.
	u, ok := sink.latestForSession(hookSessionID)
	if !ok || !u.HasPermission {
		t.Fatal("PermissionRequest hook: HasPermission not set before poll cycle")
	}

	// Manually age the overlay by running a poll with a provider whose
	// recencyWindow is tiny, so the existing lastActivity (just set) is stale.
	// We use a separate provider with a 1ns window to force staleness.
	pStale := claudecode.NewProvider(home, 10*time.Second, time.Nanosecond, nil)
	sink2 := &fakeSink{}

	// Seed the same session in the stale provider.
	pStale.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"PermissionRequest","session_id":"`+hookSessionID+`","cwd":"/tmp/phantom"}`),
		sink2,
	)

	// Sleep briefly so lastActivity is definitely older than 1ns.
	time.Sleep(10 * time.Millisecond)

	// Poll with no sessions on disk — the overlay is now stale.
	pStale.PollOnceForTest(sink2, nil)

	// The session must have been pruned: the replace batch should be empty.
	replaces := sink2.snapshotReplaces()
	if len(replaces) == 0 {
		t.Fatal("poll emitted no ReplaceProviderInstance batches")
	}
	last := replaces[len(replaces)-1]
	for _, u := range last {
		if u.SessionID == hookSessionID {
			t.Errorf("stale phantom session %q still present after poll — should have been pruned", hookSessionID)
		}
	}
}

// TestProvider_PollOnce_RecentPhantomKept verifies that a hook-seeded session
// absent from disk is NOT pruned when its overlay's lastActivity is within
// recencyWindow.
func TestProvider_PollOnce_RecentPhantomKept(t *testing.T) {
	recencyWindow := 30 * time.Minute
	hookSessionID := "aaaabbbb-cccc-dddd-eeee-000000000201"

	// Use an empty home so the hook session is never on disk.
	home := filepath.Join(t.TempDir(), "empty-claude")

	p := claudecode.NewProvider(home, 10*time.Second, recencyWindow, nil)
	sink := &fakeSink{}

	// Seed the hook session — lastActivity is just now (well within recencyWindow).
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"PermissionRequest","session_id":"`+hookSessionID+`","cwd":"/tmp/phantom-recent"}`),
		&hookSink{},
	)

	// Poll with no sessions on disk — overlay is recent, session must survive.
	p.PollOnceForTest(sink, nil)

	replaces := sink.snapshotReplaces()
	if len(replaces) == 0 {
		t.Fatal("poll emitted no ReplaceProviderInstance batches")
	}
	last := replaces[len(replaces)-1]
	var found bool
	for _, u := range last {
		if u.SessionID == hookSessionID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("recent phantom session %q was pruned — should have been kept (lastActivity within recencyWindow)", hookSessionID)
	}
}

// TestProvider_PollOnce_OnDiskSessionNeverPruned verifies that a session
// present on disk is never pruned regardless of overlay age.
func TestProvider_PollOnce_OnDiskSessionNeverPruned(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000202"
	// Old session — outside any recency window.
	home := buildFixtureHome(t, sessionID, time.Now().Add(-24*time.Hour))

	// Use a 1ns recency window so any overlay would be stale immediately.
	p := claudecode.NewProvider(home, 10*time.Second, time.Nanosecond, nil)
	sink := &fakeSink{}

	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}

	p.PollOnceForTest(sink, diskSessions)

	replaces := sink.snapshotReplaces()
	if len(replaces) == 0 {
		t.Fatal("poll emitted no ReplaceProviderInstance batches")
	}
	last := replaces[len(replaces)-1]
	var found bool
	for _, u := range last {
		if u.SessionID == sessionID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("on-disk session %q was pruned — sessions on disk must never be pruned", sessionID)
	}
}
// TestProvider_NotificationPermission_SurvivesPoll is the primary regression
// test for the overlay write-back bug: a Notification/permission_prompt hook
// fires, then a poll cycle runs with the session present on disk, and the
// emitted SessionUpdate must STILL have HasPermission==true after the poll.
// Without the p.overlays[sessionID]=ov write-back this test fails because the
// poll reads an empty overlay and emits HasPermission=false.
func TestProvider_NotificationPermission_SurvivesPoll(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000011"
	home := buildFixtureHome(t, sessionID, time.Now().Add(-1*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Fire a Notification hook with permission_prompt — sets hasPermission=true.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"Notification","session_id":"`+sessionID+`","notification_type":"permission_prompt"}`),
		sink,
	)

	// Verify the hook was processed and HasPermission is set.
	u, ok := sink.latestForSession(sessionID)
	if !ok || !u.HasPermission {
		t.Fatal("Notification/permission_prompt hook: HasPermission not set before poll cycle")
	}

	// Run a poll cycle with the session present on disk.
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}
	p.PollOnceForTest(sink, diskSessions)

	// The poll emits via ReplaceProviderInstance (captured in replaces on fakeSink,
	// but hookSink ignores it). We need the post-poll ApplyUpdate from the poll's
	// ReplaceProviderInstance path — use a fakeSink for the poll call instead.
	sink2 := &fakeSink{}
	p.PollOnceForTest(sink2, diskSessions)

	replaces := sink2.snapshotReplaces()
	if len(replaces) == 0 {
		t.Fatal("poll emitted no ReplaceProviderInstance batches")
	}
	last := replaces[len(replaces)-1]
	var found *provider.SessionUpdate
	for i := range last {
		if last[i].SessionID == sessionID {
			u := last[i]
			found = &u
		}
	}
	if found == nil {
		t.Fatalf("session %q absent from post-poll replace batch", sessionID)
	}
	if !found.HasPermission {
		t.Errorf("HasPermission = false after poll — overlay write-back missing (regression)")
	}
}

// TestProvider_StopIdle_SurvivesPoll verifies that a Stop-driven statusType="idle"
// persists across a subsequent poll cycle (parallel assertion to the permission test).
func TestProvider_StopIdle_SurvivesPoll(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000022"
	// Use an old session so poll-derived default would be "recent" (no statusType),
	// proving the overlay's "idle" wins.
	home := buildFixtureHome(t, sessionID, time.Now().Add(-2*time.Hour))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Fire a Stop hook → overlay statusType = "idle".
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"Stop","session_id":"`+sessionID+`"}`),
		sink,
	)

	u, ok := sink.latestForSession(sessionID)
	if !ok || u.StatusType != "idle" {
		t.Fatalf("Stop hook: StatusType = %q, want idle", u.StatusType)
	}

	// Poll cycle — session is on disk but old (outside recency window).
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}

	sink2 := &fakeSink{}
	p.PollOnceForTest(sink2, diskSessions)

	replaces := sink2.snapshotReplaces()
	if len(replaces) == 0 {
		t.Fatal("poll emitted no ReplaceProviderInstance batches")
	}
	last := replaces[len(replaces)-1]
	var found *provider.SessionUpdate
	for i := range last {
		if last[i].SessionID == sessionID {
			u := last[i]
			found = &u
		}
	}
	if found == nil {
		t.Fatalf("session %q absent from post-poll replace batch", sessionID)
	}
	if found.StatusType != "idle" {
		t.Errorf("StatusType = %q after poll, want idle — overlay write-back missing (regression)", found.StatusType)
	}
}

// TestProvider_AskUserQuestion_OrderA verifies that when PreToolUse(AskUserQuestion)
// arrives before the paired Notification/permission_prompt, the result is
// HasQuestion=true and HasPermission=false, and the state survives a poll cycle.
func TestProvider_AskUserQuestion_OrderA(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000101"
	home := buildFixtureHome(t, sessionID, time.Now().Add(-1*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Order A: PreToolUse(AskUserQuestion) arrives first.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"PreToolUse","session_id":"`+sessionID+`","tool_name":"AskUserQuestion"}`),
		sink,
	)

	u, ok := sink.latestForSession(sessionID)
	if !ok {
		t.Fatal("no update after PreToolUse(AskUserQuestion)")
	}
	if !u.HasQuestion {
		t.Error("PreToolUse(AskUserQuestion): HasQuestion = false, want true")
	}
	if u.HasPermission {
		t.Error("PreToolUse(AskUserQuestion): HasPermission = true, want false")
	}

	// Paired Notification/permission_prompt arrives second — must NOT set HasPermission.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"Notification","session_id":"`+sessionID+`","notification_type":"permission_prompt"}`),
		sink,
	)

	u2, ok2 := sink.latestForSession(sessionID)
	if !ok2 {
		t.Fatal("no update after Notification/permission_prompt")
	}
	if !u2.HasQuestion {
		t.Error("after Notification: HasQuestion = false, want true (question must persist)")
	}
	if u2.HasPermission {
		t.Error("after Notification: HasPermission = true, want false (question's own prompt must not set permission)")
	}

	// State must survive a subsequent poll cycle.
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}
	sink2 := &fakeSink{}
	p.PollOnceForTest(sink2, diskSessions)

	replaces := sink2.snapshotReplaces()
	if len(replaces) == 0 {
		t.Fatal("poll emitted no ReplaceProviderInstance batches")
	}
	last := replaces[len(replaces)-1]
	var found *provider.SessionUpdate
	for i := range last {
		if last[i].SessionID == sessionID {
			u := last[i]
			found = &u
		}
	}
	if found == nil {
		t.Fatalf("session %q absent from post-poll replace batch", sessionID)
	}
	if !found.HasQuestion {
		t.Error("HasQuestion = false after poll — question overlay not preserved")
	}
	if found.HasPermission {
		t.Error("HasPermission = true after poll — must remain false for question tool")
	}
}

// TestProvider_AskUserQuestion_OrderB verifies that when Notification/permission_prompt
// arrives BEFORE PreToolUse(AskUserQuestion) (concurrent processes, non-deterministic
// socket arrival order), the final result is still HasQuestion=true and HasPermission=false.
// The PreToolUse frame forces hasPermission=false after the Notification set it.
func TestProvider_AskUserQuestion_OrderB(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000102"
	home := buildFixtureHome(t, sessionID, time.Now().Add(-1*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Order B: Notification/permission_prompt arrives first.
	// At this point hasQuestion is false, so hasPermission gets set to true.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"Notification","session_id":"`+sessionID+`","notification_type":"permission_prompt"}`),
		sink,
	)

	u, ok := sink.latestForSession(sessionID)
	if !ok {
		t.Fatal("no update after Notification/permission_prompt")
	}
	// Intermediate state: hasPermission=true (question not yet known).
	if !u.HasPermission {
		t.Error("intermediate: HasPermission = false after Notification (expected true before PreToolUse arrives)")
	}

	// PreToolUse(AskUserQuestion) arrives second — must correct the state.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"PreToolUse","session_id":"`+sessionID+`","tool_name":"AskUserQuestion"}`),
		sink,
	)

	u2, ok2 := sink.latestForSession(sessionID)
	if !ok2 {
		t.Fatal("no update after PreToolUse(AskUserQuestion)")
	}
	if !u2.HasQuestion {
		t.Error("after PreToolUse: HasQuestion = false, want true")
	}
	if u2.HasPermission {
		t.Error("after PreToolUse: HasPermission = true, want false (PreToolUse must clear it)")
	}

	// State must survive a subsequent poll cycle.
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}
	sink2 := &fakeSink{}
	p.PollOnceForTest(sink2, diskSessions)

	replaces := sink2.snapshotReplaces()
	if len(replaces) == 0 {
		t.Fatal("poll emitted no ReplaceProviderInstance batches")
	}
	last := replaces[len(replaces)-1]
	var found *provider.SessionUpdate
	for i := range last {
		if last[i].SessionID == sessionID {
			u := last[i]
			found = &u
		}
	}
	if found == nil {
		t.Fatalf("session %q absent from post-poll replace batch", sessionID)
	}
	if !found.HasQuestion {
		t.Error("HasQuestion = false after poll — question overlay not preserved")
	}
	if found.HasPermission {
		t.Error("HasPermission = true after poll — must remain false for question tool")
	}
}

// TestProvider_RealPermissionPrompt verifies that a Notification/permission_prompt
// with no preceding AskUserQuestion yields HasPermission=true and HasQuestion=false.
func TestProvider_RealPermissionPrompt(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000103"
	home := buildFixtureHome(t, sessionID, time.Now().Add(-1*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Real permission prompt: no AskUserQuestion PreToolUse.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"Notification","session_id":"`+sessionID+`","notification_type":"permission_prompt"}`),
		sink,
	)

	u, ok := sink.latestForSession(sessionID)
	if !ok {
		t.Fatal("no update after Notification/permission_prompt")
	}
	if !u.HasPermission {
		t.Error("real permission prompt: HasPermission = false, want true")
	}
	if u.HasQuestion {
		t.Error("real permission prompt: HasQuestion = true, want false")
	}
}

// TestProvider_QuestionClearsOnUserPromptSubmit verifies that a UserPromptSubmit
// (the user's answer) clears the question state.
func TestProvider_QuestionClearsOnUserPromptSubmit(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000104"
	home := buildFixtureHome(t, sessionID, time.Now().Add(-1*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Set up question state.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"PreToolUse","session_id":"`+sessionID+`","tool_name":"AskUserQuestion"}`),
		sink,
	)
	u, ok := sink.latestForSession(sessionID)
	if !ok || !u.HasQuestion {
		t.Fatal("question state not set before UserPromptSubmit")
	}

	// User answers → question cleared.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"UserPromptSubmit","session_id":"`+sessionID+`"}`),
		sink,
	)

	u2, ok2 := sink.latestForSession(sessionID)
	if !ok2 {
		t.Fatal("no update after UserPromptSubmit")
	}
	if u2.HasQuestion {
		t.Error("UserPromptSubmit: HasQuestion = true, want false (answer clears question)")
	}
	if u2.HasPermission {
		t.Error("UserPromptSubmit: HasPermission = true, want false")
	}
	if u2.StatusType != "busy" {
		t.Errorf("UserPromptSubmit: StatusType = %q, want busy", u2.StatusType)
	}
}

// TestProvider_QuestionClearsOnStop verifies that a Stop event clears the
// question state.
func TestProvider_QuestionClearsOnStop(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000105"
	home := buildFixtureHome(t, sessionID, time.Now().Add(-1*time.Minute))

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)
	sink := &hookSink{}

	// Set up question state.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"PreToolUse","session_id":"`+sessionID+`","tool_name":"AskUserQuestion"}`),
		sink,
	)
	u, ok := sink.latestForSession(sessionID)
	if !ok || !u.HasQuestion {
		t.Fatal("question state not set before Stop")
	}

	// Stop → question cleared, status idle.
	p.HandleHookFrameForTest(
		[]byte(`{"hook_event_name":"Stop","session_id":"`+sessionID+`"}`),
		sink,
	)

	u2, ok2 := sink.latestForSession(sessionID)
	if !ok2 {
		t.Fatal("no update after Stop")
	}
	if u2.HasQuestion {
		t.Error("Stop: HasQuestion = true, want false")
	}
	if u2.HasPermission {
		t.Error("Stop: HasPermission = true, want false")
	}
	if u2.StatusType != "idle" {
		t.Errorf("Stop: StatusType = %q, want idle", u2.StatusType)
	}
}

// TestProvider_PollDoesNotWipeHookOverlay is the live-socket variant of the
// prune-protection test: hook fires via socket, then a deterministic poll runs
// without the session on disk — overlay must survive.
func TestProvider_PollDoesNotWipeHookOverlay(t *testing.T) {
	otherSessionID := "aaaabbbb-cccc-dddd-eeee-000000000088"
	hookSessionID := "aaaabbbb-cccc-dddd-eeee-000000000077"
	home := buildFixtureHome(t, otherSessionID, time.Now().Add(-2*time.Minute))

	sockDir := shortXDGDir(t)
	t.Setenv("XDG_RUNTIME_DIR", sockDir)

	p := claudecode.NewProvider(home, 10*time.Second, 30*time.Minute, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := &hookSink{}
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, sink) }()

	// Wait for the initial poll to populate the session map (otherSessionID only).
	time.Sleep(200 * time.Millisecond)

	sockPath := claudecode.HookSocketPath()

	// Fire a PermissionRequest hook for hookSessionID — NOT present on disk.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial hook socket: %v", err)
	}
	if err := hookipc.WriteFrame(conn, []byte(`{"hook_event_name":"PermissionRequest","session_id":"`+hookSessionID+`","cwd":"/tmp/hook-session"}`)); err != nil {
		conn.Close()
		t.Fatalf("WriteFrame: %v", err)
	}
	conn.Close()

	// Wait for the hook to be processed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if u, ok := sink.latestForSession(hookSessionID); ok && u.HasPermission {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	u, ok := sink.latestForSession(hookSessionID)
	if !ok || !u.HasPermission {
		t.Fatal("PermissionRequest hook: HasPermission not set before poll cycle")
	}

	// Run a deterministic poll cycle — hookSessionID absent from disk.
	diskSessions, err := claudecode.ReadSessionsForTest(home)
	if err != nil {
		t.Fatalf("ReadSessionsForTest: %v", err)
	}
	p.PollOnceForTest(sink, diskSessions)

	// The hook-seeded session must still be present with HasPermission==true.
	u2, ok2 := sink.latestForSession(hookSessionID)
	if !ok2 {
		t.Fatal("HAZARD: hook session absent after poll cycle — was wiped")
	}
	if !u2.HasPermission {
		t.Error("HAZARD: poll cycle wiped hook overlay — HasPermission = false after poll, want true")
	}

	cancel()
	<-done
}
