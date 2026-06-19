package omp_test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/omp"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// shortXDGDir creates a short directory path suitable for use as XDG_RUNTIME_DIR
// so the resulting socket path stays within macOS's 104-char sun_path limit.
func shortXDGDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cog-xdg")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestHookSocketPath_Consistency(t *testing.T) {
	if a, b := omp.HookSocketPath(), omp.HookSocketPath(); a != b {
		t.Errorf("HookSocketPath() not stable: %q != %q", a, b)
	}
	if !strings.HasSuffix(omp.HookSocketPath(), "cogitator-omp-hook.sock") {
		t.Errorf("HookSocketPath() = %q, want suffix cogitator-omp-hook.sock", omp.HookSocketPath())
	}
}

func TestParseHookEvent(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantEvent string
		wantID    string
		wantCWD   string
		wantTool  string
	}{
		{
			name:      "session id recovered from session_file basename",
			raw:       `{"hook_event_name":"turn_start","cwd":"/tmp/x","session_file":"/h/sessions/-tmp-x/2026-06-19T10-00-00-000Z_019edf7d-0232.jsonl"}`,
			wantEvent: "turn_start",
			wantID:    "019edf7d-0232",
			wantCWD:   "/tmp/x",
		},
		{
			name:      "explicit session_id wins over file",
			raw:       `{"hook_event_name":"turn_end","session_id":"explicit","session_file":"/a/2026_other.jsonl"}`,
			wantEvent: "turn_end",
			wantID:    "explicit",
		},
		{
			name:      "tool name carried for tool_call",
			raw:       `{"hook_event_name":"tool_call","tool_name":"ask","cwd":"/tmp/x"}`,
			wantEvent: "tool_call",
			wantCWD:   "/tmp/x",
			wantTool:  "ask",
		},
		{
			name:      "PascalCase alias normalized",
			raw:       `{"hook_event_name":"SessionStart","cwd":"/tmp/x"}`,
			wantEvent: "session_start",
			wantCWD:   "/tmp/x",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := omp.ParseHookEvent([]byte(tc.raw))
			if err != nil {
				t.Fatalf("ParseHookEvent: %v", err)
			}
			if ev.EventName != tc.wantEvent {
				t.Errorf("EventName = %q, want %q", ev.EventName, tc.wantEvent)
			}
			if ev.SessionID != tc.wantID {
				t.Errorf("SessionID = %q, want %q", ev.SessionID, tc.wantID)
			}
			if ev.CWD != tc.wantCWD {
				t.Errorf("CWD = %q, want %q", ev.CWD, tc.wantCWD)
			}
			if ev.ToolName != tc.wantTool {
				t.Errorf("ToolName = %q, want %q", ev.ToolName, tc.wantTool)
			}
		})
	}
}

func TestParseHookEvent_MalformedJSON(t *testing.T) {
	if _, err := omp.ParseHookEvent([]byte(`{not json`)); err == nil {
		t.Error("ParseHookEvent(malformed) = nil error, want non-nil")
	}
}

// TestProvider_SocketRoundTrip exercises the full live path: the provider's
// Start opens the hook listener, a client dials the real Unix socket and writes
// a framed event, and the sink receives the resulting SessionUpdate.
func TestProvider_SocketRoundTrip(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortXDGDir(t))

	// Long poll interval so the listener, not the poll, drives updates.
	p := omp.NewProvider(t.TempDir(), 10*time.Second, 30*time.Minute, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := &recordSink{}
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, sink) }()

	// Wait for the listener to bind (initial poll runs first).
	time.Sleep(200 * time.Millisecond)

	const sf = "/h/sessions/-tmp-x/2026-06-19T10-00-00-000Z_round1.jsonl"
	send := func(json string) {
		conn, err := net.Dial("unix", omp.HookSocketPath())
		if err != nil {
			t.Fatalf("dial hook socket: %v", err)
		}
		defer conn.Close()
		if err := omp.WriteFrame(conn, []byte(json)); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	waitFor := func(check func(provider.SessionUpdate) bool) provider.SessionUpdate {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if u, ok := sink.latestForSession("round1"); ok && check(u) {
				return u
			}
			time.Sleep(20 * time.Millisecond)
		}
		u, _ := sink.latestForSession("round1")
		t.Fatalf("timed out; last update: %+v", u)
		return provider.SessionUpdate{}
	}

	send(`{"hook_event_name":"turn_start","cwd":"/tmp/x","session_file":"` + sf + `"}`)
	if u := waitFor(func(u provider.SessionUpdate) bool { return u.StatusType == "busy" }); u.SessionID != "round1" {
		t.Errorf("SessionID = %q, want round1", u.SessionID)
	}

	send(`{"hook_event_name":"tool_call","tool_name":"ask","cwd":"/tmp/x","session_file":"` + sf + `"}`)
	waitFor(func(u provider.SessionUpdate) bool { return u.HasQuestion })

	send(`{"hook_event_name":"turn_end","cwd":"/tmp/x","session_file":"` + sf + `"}`)
	waitFor(func(u provider.SessionUpdate) bool { return u.StatusType == "idle" && !u.HasQuestion })

	cancel()
	<-done
}
