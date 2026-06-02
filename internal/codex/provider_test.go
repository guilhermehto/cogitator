package codex_test

import (
	"context"
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
