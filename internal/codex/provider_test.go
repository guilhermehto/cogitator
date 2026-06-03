package codex_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// fakeSink records every ApplyUpdate call and the most recent
// ClearProviderInstance call so tests can assert on emitted updates.
type fakeSink struct {
	mu      sync.Mutex
	updates []provider.SessionUpdate
	clears  int
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

func (f *fakeSink) ReplaceProviderInstance(_ harness.Kind, _ string, _ []provider.SessionUpdate) {}

func (f *fakeSink) snapshot() ([]provider.SessionUpdate, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]provider.SessionUpdate, len(f.updates))
	copy(cp, f.updates)
	return cp, f.clears
}

// buildFixtureHome creates a temporary CODEX_HOME with one rollout file whose
// session_meta timestamp is set to the provided lastActivity time.
func buildFixtureHome(t *testing.T, sessionID string, lastActivity time.Time) string {
	t.Helper()
	home := t.TempDir()
	sessDir := filepath.Join(home, "sessions", "2026", "06", "03")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ts := lastActivity.UTC().Format(time.RFC3339Nano)
	content := `{"timestamp":"` + ts + `","type":"session_meta","payload":{"id":"` + sessionID + `","timestamp":"` + ts + `","cwd":"/tmp/test","originator":"codex-tui","cli_version":"0.136.0","source":"cli"}}
{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"user_message","message":"hello world"}}
`
	fname := "rollout-2026-06-03T10-00-00-000Z-" + sessionID + ".jsonl"
	if err := os.WriteFile(filepath.Join(sessDir, fname), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestProvider_RecencyMapping verifies that a session whose last activity is
// within the recency window gets SourceLive, and one outside gets SourceRecent.
func TestProvider_RecencyMapping(t *testing.T) {
	recencyWindow := 30 * time.Minute

	tests := []struct {
		name       string
		age        time.Duration // how old the session's last activity is
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
			p := codex.NewProvider(home, 100*time.Millisecond, recencyWindow, nil)

			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			// Run the provider; it polls immediately on Start.
			done := make(chan error, 1)
			go func() { done <- p.Start(ctx, sink) }()

			// Wait for at least one poll cycle.
			time.Sleep(150 * time.Millisecond)
			cancel()
			<-done

			updates, _ := sink.snapshot()
			var found *provider.SessionUpdate
			for i := range updates {
				if updates[i].SessionID == sessionID {
					found = &updates[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("no update emitted for session %q; got %d updates", sessionID, len(updates))
			}
			if found.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", found.Source, tc.wantSource)
			}
		})
	}
}

// TestProvider_EmitsOneUpdatePerSession verifies that a poll cycle emits
// exactly one SessionUpdate per fixture session under instance id "codex"
// with Provider=codex.
func TestProvider_EmitsOneUpdatePerSession(t *testing.T) {
	home := filepath.Join("testdata")

	sink := &fakeSink{}
	// Use a very large recency window so all sessions are SourceLive for this
	// test (we only care about count and metadata, not source).
	p := codex.NewProvider(home, 100*time.Millisecond, 24*time.Hour, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, sink) }()

	// Wait for the initial poll.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	updates, clears := sink.snapshot()
	if clears < 1 {
		t.Errorf("ClearProviderInstance called %d times, want ≥1", clears)
	}

	// Count updates from the first poll cycle (before the second tick fires).
	// All updates must have Provider=codex and InstanceID="codex".
	for _, u := range updates {
		if u.Provider != harness.KindCodex {
			t.Errorf("update.Provider = %q, want %q", u.Provider, harness.KindCodex)
		}
		if u.InstanceID != codex.InstanceID {
			t.Errorf("update.InstanceID = %q, want %q", u.InstanceID, codex.InstanceID)
		}
		if u.SessionID == "" {
			t.Error("update.SessionID is empty")
		}
	}

	// The testdata directory has at least 2 fixture sessions (well-formed +
	// truncated). Verify we got at least that many updates.
	if len(updates) < 2 {
		t.Errorf("got %d updates, want ≥2 (one per fixture session)", len(updates))
	}
}

// TestProvider_AbsentCodexHome verifies that a missing CODEX_HOME yields no
// updates and no errors — the provider starts and stops cleanly.
func TestProvider_AbsentCodexHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "does-not-exist")

	sink := &fakeSink{}
	p := codex.NewProvider(home, 100*time.Millisecond, 30*time.Minute, nil)

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

// TestProvider_Kind verifies the provider reports KindCodex.
func TestProvider_Kind(t *testing.T) {
	p := codex.NewProvider("", 5*time.Second, 30*time.Minute, nil)
	if p.Kind() != harness.KindCodex {
		t.Errorf("Kind() = %q, want %q", p.Kind(), harness.KindCodex)
	}
}

// ── Step-10 tests: hook-driven attention + poll-vs-hook merge ─────────────────

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

// TestProvider_HookDrivenAttention verifies that hook events produce the
// correct SessionUpdate attention fields:
//
//   - SessionStart → statusType="busy", hasPermission=false
//   - Stop → statusType="idle", hasPermission=false
//   - PermissionRequest → hasPermission=true
//   - error indicator → lastError set
func TestProvider_HookDrivenAttention(t *testing.T) {
	sessionID := "aaaabbbb-cccc-dddd-eeee-000000000099"
	lastActivity := time.Now().Add(-2 * time.Minute)
	home := buildFixtureHome(t, sessionID, lastActivity)

	// Use a very long poll interval so the test controls timing precisely.
	p := codex.NewProvider(home, 10*time.Second, 30*time.Minute, nil)

	// Use a short XDG dir so the socket path is short enough for macOS.
	sockDir := shortXDGDir(t)
	t.Setenv("XDG_RUNTIME_DIR", sockDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := &hookSink{}
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, sink) }()

	// Wait for the initial poll to populate the session map.
	time.Sleep(200 * time.Millisecond)

	sockPath := codex.HookSocketPath()

	sendHookJSON := func(t *testing.T, json string) {
		t.Helper()
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			t.Fatalf("dial hook socket: %v", err)
		}
		defer conn.Close()
		if err := codex.WriteFrame(conn, []byte(json)); err != nil {
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

	// Error indicator → lastError set.
	sendHookJSON(t, `{"hook_event_name":"SessionStart","session_id":"`+sessionID+`","error":"something failed"}`)
	u = waitForUpdate(t, func(u provider.SessionUpdate) bool { return !u.LastError.IsZero() })
	if u.LastError.IsZero() {
		t.Error("error indicator: LastError is zero, want non-zero")
	}

	cancel()
	<-done
}

// TestProvider_PollDoesNotWipeHookOverlay is the CRITICAL INTEGRATION HAZARD
// test: a PermissionRequest hook fires for a session NOT present on disk, then
// a poll cycle runs over fixtures that do NOT include that session — the hook
// overlay MUST survive without re-firing the hook.
//
// This exercises the exact race: hook arrives before rollout file is flushed →
// next poll would previously clear the session → attention disappears.
func TestProvider_PollDoesNotWipeHookOverlay(t *testing.T) {
	// Build a fixture home with a DIFFERENT session so the hook session is
	// absent from disk during the poll.
	otherSessionID := "aaaabbbb-cccc-dddd-eeee-000000000088"
	hookSessionID := "aaaabbbb-cccc-dddd-eeee-000000000077"
	home := buildFixtureHome(t, otherSessionID, time.Now().Add(-2*time.Minute))

	// Use a short XDG dir so the socket path is short enough for macOS.
	sockDir := shortXDGDir(t)
	t.Setenv("XDG_RUNTIME_DIR", sockDir)

	// Use a very long poll interval so we control when polls happen via pollOnce.
	p := codex.NewProvider(home, 10*time.Second, 30*time.Minute, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := &hookSink{}
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, sink) }()

	// Wait for the initial poll to populate the session map (otherSessionID only).
	time.Sleep(200 * time.Millisecond)

	sockPath := codex.HookSocketPath()

	// Fire a PermissionRequest hook for hookSessionID — NOT present on disk.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial hook socket: %v", err)
	}
	if err := codex.WriteFrame(conn, []byte(`{"hook_event_name":"PermissionRequest","session_id":"`+hookSessionID+`","cwd":"/tmp/hook-session"}`)); err != nil {
		conn.Close()
		t.Fatalf("WriteFrame: %v", err)
	}
	conn.Close()

	// Wait for the hook to be processed and HasPermission to be set.
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

	// Now run a poll cycle deterministically WITHOUT re-firing the hook.
	// The fixture home still only has otherSessionID on disk — hookSessionID
	// is absent. The overlay MUST survive.
	diskSessions, err := codex.ReadSessionsForTest(home)
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
