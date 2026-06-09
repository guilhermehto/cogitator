package codex_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/codex"
)

// shortXDGDir creates a short directory path suitable for use as XDG_RUNTIME_DIR
// so that the resulting socket path stays within macOS's 104-char sun_path limit.
func shortXDGDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cog-xdg")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestHookSocketPath_Consistency verifies that repeated calls return the same
// path and that the path contains the expected filename.
func TestHookSocketPath_Consistency(t *testing.T) {
	first := codex.HookSocketPath()
	second := codex.HookSocketPath()
	if first != second {
		t.Errorf("HookSocketPath() not stable: %q != %q", first, second)
	}
	if !strings.HasSuffix(first, "cogitator-codex-hook.sock") {
		t.Errorf("HookSocketPath() = %q, want suffix cogitator-codex-hook.sock", first)
	}
}

// TestHookSocketPath_XDGRuntimeDir verifies that $XDG_RUNTIME_DIR is preferred
// over os.TempDir() when set.
func TestHookSocketPath_XDGRuntimeDir(t *testing.T) {
	dir := shortXDGDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)

	got := codex.HookSocketPath()
	want := filepath.Join(dir, "cogitator-codex-hook.sock")
	if got != want {
		t.Errorf("HookSocketPath() = %q, want %q", got, want)
	}
}

// TestHookSocketPath_TempDirFallback verifies that os.TempDir() is used when
// $XDG_RUNTIME_DIR is unset.
func TestHookSocketPath_TempDirFallback(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	got := codex.HookSocketPath()
	want := filepath.Join(os.TempDir(), "cogitator-codex-hook.sock")
	if got != want {
		t.Errorf("HookSocketPath() = %q, want %q", got, want)
	}
}

// TestParseHookEvent_EventMapping verifies that the defensive parser maps
// known event names (both PascalCase and snake_case) to their canonical form
// and extracts session_id and cwd from multiple candidate field names.
func TestParseHookEvent_EventMapping(t *testing.T) {
	tests := []struct {
		name          string
		json          string
		wantEvent     string
		wantSessionID string
		wantCWD       string
		wantIsError   bool
	}{
		{
			name:          "SessionStart PascalCase",
			json:          `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/tmp/a"}`,
			wantEvent:     "session_start",
			wantSessionID: "s1",
			wantCWD:       "/tmp/a",
		},
		{
			name:          "session_start snake_case",
			json:          `{"hook_event_name":"session_start","session_id":"s2","cwd":"/tmp/b"}`,
			wantEvent:     "session_start",
			wantSessionID: "s2",
			wantCWD:       "/tmp/b",
		},
		{
			name:          "Stop → stopped",
			json:          `{"hook_event_name":"Stop","session_id":"s3"}`,
			wantEvent:     "stopped",
			wantSessionID: "s3",
		},
		{
			name:          "stopped snake_case",
			json:          `{"hook_event_name":"stopped","session_id":"s4"}`,
			wantEvent:     "stopped",
			wantSessionID: "s4",
		},
		{
			name:          "PermissionRequest PascalCase",
			json:          `{"hook_event_name":"PermissionRequest","session_id":"s5","cwd":"/tmp/c"}`,
			wantEvent:     "permission_request",
			wantSessionID: "s5",
			wantCWD:       "/tmp/c",
		},
		{
			name:          "permission_request snake_case",
			json:          `{"hook_event_name":"permission_request","session_id":"s6"}`,
			wantEvent:     "permission_request",
			wantSessionID: "s6",
		},
		{
			name:          "event field fallback",
			json:          `{"event":"UserPromptSubmit","session_id":"s7"}`,
			wantEvent:     "user_prompt_submit",
			wantSessionID: "s7",
		},
		{
			name:          "type field fallback",
			json:          `{"type":"PreToolUse","session_id":"s8"}`,
			wantEvent:     "pre_tool_use",
			wantSessionID: "s8",
		},
		{
			name:          "sessionId camelCase fallback",
			json:          `{"hook_event_name":"PostToolUse","sessionId":"s9"}`,
			wantEvent:     "post_tool_use",
			wantSessionID: "s9",
		},
		{
			name:          "id fallback for session id",
			json:          `{"hook_event_name":"Notification","id":"s10"}`,
			wantEvent:     "notification",
			wantSessionID: "s10",
		},
		{
			name:          "directory fallback for cwd",
			json:          `{"hook_event_name":"SessionStart","session_id":"s11","directory":"/tmp/d"}`,
			wantEvent:     "session_start",
			wantSessionID: "s11",
			wantCWD:       "/tmp/d",
		},
		{
			name:          "error indicator",
			json:          `{"hook_event_name":"SessionStart","session_id":"s12","error":"something went wrong"}`,
			wantEvent:     "session_start",
			wantSessionID: "s12",
			wantIsError:   true,
		},
		{
			name:          "unknown event name passes through",
			json:          `{"hook_event_name":"SomeFutureEvent","session_id":"s13"}`,
			wantEvent:     "SomeFutureEvent",
			wantSessionID: "s13",
		},
		{
			name:      "empty JSON is graceful",
			json:      `{}`,
			wantEvent: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := codex.ParseHookEvent([]byte(tc.json))
			if err != nil {
				t.Fatalf("ParseHookEvent: %v", err)
			}
			if ev.EventName != tc.wantEvent {
				t.Errorf("EventName = %q, want %q", ev.EventName, tc.wantEvent)
			}
			if ev.SessionID != tc.wantSessionID {
				t.Errorf("SessionID = %q, want %q", ev.SessionID, tc.wantSessionID)
			}
			if ev.CWD != tc.wantCWD {
				t.Errorf("CWD = %q, want %q", ev.CWD, tc.wantCWD)
			}
			if ev.IsError != tc.wantIsError {
				t.Errorf("IsError = %v, want %v", ev.IsError, tc.wantIsError)
			}
		})
	}
}

// TestParseHookEvent_MalformedJSON verifies that malformed JSON returns an error.
func TestParseHookEvent_MalformedJSON(t *testing.T) {
	_, err := codex.ParseHookEvent([]byte(`not json`))
	if err == nil {
		t.Error("ParseHookEvent(malformed): expected error, got nil")
	}
}
