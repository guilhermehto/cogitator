package claudecode_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/claudecode"
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
	first := claudecode.HookSocketPath()
	second := claudecode.HookSocketPath()
	if first != second {
		t.Errorf("HookSocketPath() not stable: %q != %q", first, second)
	}
	if !strings.HasSuffix(first, "cogitator-claude-hook.sock") {
		t.Errorf("HookSocketPath() = %q, want suffix cogitator-claude-hook.sock", first)
	}
}

// TestHookSocketPath_XDGRuntimeDir verifies that $XDG_RUNTIME_DIR is preferred
// over os.TempDir() when set.
func TestHookSocketPath_XDGRuntimeDir(t *testing.T) {
	dir := shortXDGDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)

	got := claudecode.HookSocketPath()
	want := filepath.Join(dir, "cogitator-claude-hook.sock")
	if got != want {
		t.Errorf("HookSocketPath() = %q, want %q", got, want)
	}
}

// TestHookSocketPath_TempDirFallback verifies that os.TempDir() is used when
// $XDG_RUNTIME_DIR is unset.
func TestHookSocketPath_TempDirFallback(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	got := claudecode.HookSocketPath()
	want := filepath.Join(os.TempDir(), "cogitator-claude-hook.sock")
	if got != want {
		t.Errorf("HookSocketPath() = %q, want %q", got, want)
	}
}

// TestHookSocketPath_DifferentFromCodex verifies that the Claude Code socket
// path is distinct from the Codex socket path so both agents can run side-by-side.
func TestHookSocketPath_DifferentFromCodex(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	got := claudecode.HookSocketPath()
	if strings.Contains(got, "codex") {
		t.Errorf("HookSocketPath() = %q, must not contain 'codex'", got)
	}
	if !strings.Contains(got, "claude") {
		t.Errorf("HookSocketPath() = %q, want 'claude' in path", got)
	}
}

// TestParseHookEvent_EventMapping verifies that the defensive parser maps
// known event names (both PascalCase and snake_case) to their canonical form
// and extracts session_id, cwd, and notification_type correctly.
func TestParseHookEvent_EventMapping(t *testing.T) {
	tests := []struct {
		name             string
		json             string
		wantEvent        string
		wantSessionID    string
		wantCWD          string
		wantNotifType    string
		wantStatusType   string
		wantHasPermission bool
	}{
		{
			name:           "SessionStart PascalCase → busy",
			json:           `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/tmp/a"}`,
			wantEvent:      "SessionStart",
			wantSessionID:  "s1",
			wantCWD:        "/tmp/a",
			wantStatusType: "busy",
		},
		{
			name:           "session_start snake_case → busy",
			json:           `{"hook_event_name":"session_start","session_id":"s2","cwd":"/tmp/b"}`,
			wantEvent:      "SessionStart",
			wantSessionID:  "s2",
			wantCWD:        "/tmp/b",
			wantStatusType: "busy",
		},
		{
			name:           "UserPromptSubmit → busy",
			json:           `{"hook_event_name":"UserPromptSubmit","session_id":"s3","cwd":"/tmp/c"}`,
			wantEvent:      "UserPromptSubmit",
			wantSessionID:  "s3",
			wantCWD:        "/tmp/c",
			wantStatusType: "busy",
		},
		{
			name:           "PreToolUse → busy",
			json:           `{"hook_event_name":"PreToolUse","session_id":"s4"}`,
			wantEvent:      "PreToolUse",
			wantSessionID:  "s4",
			wantStatusType: "busy",
		},
		{
			name:           "PostToolUse → busy",
			json:           `{"hook_event_name":"PostToolUse","session_id":"s5"}`,
			wantEvent:      "PostToolUse",
			wantSessionID:  "s5",
			wantStatusType: "busy",
		},
		{
			name:           "Stop → idle (teardown, not row removal)",
			json:           `{"hook_event_name":"Stop","session_id":"s6"}`,
			wantEvent:      "Stop",
			wantSessionID:  "s6",
			wantStatusType: "idle",
		},
		{
			name:           "stopped snake_case → idle",
			json:           `{"hook_event_name":"stopped","session_id":"s7"}`,
			wantEvent:      "Stop",
			wantSessionID:  "s7",
			wantStatusType: "idle",
		},
		{
			name:           "SessionEnd → idle (teardown, transcript persists)",
			json:           `{"hook_event_name":"SessionEnd","session_id":"s8","cwd":"/tmp/d"}`,
			wantEvent:      "SessionEnd",
			wantSessionID:  "s8",
			wantCWD:        "/tmp/d",
			wantStatusType: "idle",
		},
		{
			name:              "Notification permission_prompt → permission",
			json:              `{"hook_event_name":"Notification","session_id":"s9","notification_type":"permission_prompt"}`,
			wantEvent:         "Notification",
			wantSessionID:     "s9",
			wantNotifType:     "permission_prompt",
			wantStatusType:    "busy",
			wantHasPermission: true,
		},
		{
			name:           "Notification other type → busy, no permission",
			json:           `{"hook_event_name":"Notification","session_id":"s10","notification_type":"info"}`,
			wantEvent:      "Notification",
			wantSessionID:  "s10",
			wantNotifType:  "info",
			wantStatusType: "busy",
		},
		{
			name:              "PermissionRequest → permission",
			json:              `{"hook_event_name":"PermissionRequest","session_id":"s11","cwd":"/tmp/e"}`,
			wantEvent:         "PermissionRequest",
			wantSessionID:     "s11",
			wantCWD:           "/tmp/e",
			wantStatusType:    "busy",
			wantHasPermission: true,
		},
		{
			name:              "permission_request snake_case → permission",
			json:              `{"hook_event_name":"permission_request","session_id":"s12"}`,
			wantEvent:         "PermissionRequest",
			wantSessionID:     "s12",
			wantStatusType:    "busy",
			wantHasPermission: true,
		},
		{
			name:           "event field fallback",
			json:           `{"event":"UserPromptSubmit","session_id":"s13"}`,
			wantEvent:      "UserPromptSubmit",
			wantSessionID:  "s13",
			wantStatusType: "busy",
		},
		{
			name:           "type field fallback",
			json:           `{"type":"PreToolUse","session_id":"s14"}`,
			wantEvent:      "PreToolUse",
			wantSessionID:  "s14",
			wantStatusType: "busy",
		},
		{
			name:           "sessionId camelCase fallback",
			json:           `{"hook_event_name":"PostToolUse","sessionId":"s15"}`,
			wantEvent:      "PostToolUse",
			wantSessionID:  "s15",
			wantStatusType: "busy",
		},
		{
			name:           "id fallback for session id",
			json:           `{"hook_event_name":"Notification","id":"s16","notification_type":"info"}`,
			wantEvent:      "Notification",
			wantSessionID:  "s16",
			wantNotifType:  "info",
			wantStatusType: "busy",
		},
		{
			name:           "directory fallback for cwd",
			json:           `{"hook_event_name":"SessionStart","session_id":"s17","directory":"/tmp/f"}`,
			wantEvent:      "SessionStart",
			wantSessionID:  "s17",
			wantCWD:        "/tmp/f",
			wantStatusType: "busy",
		},
		{
			name:           "unknown event name passes through as idle",
			json:           `{"hook_event_name":"SomeFutureEvent","session_id":"s18"}`,
			wantEvent:      "SomeFutureEvent",
			wantSessionID:  "s18",
			wantStatusType: "idle",
		},
		{
			name:           "empty JSON is graceful",
			json:           `{}`,
			wantEvent:      "",
			wantStatusType: "idle",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := claudecode.ParseHookEvent([]byte(tc.json))
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
			if ev.NotificationType != tc.wantNotifType {
				t.Errorf("NotificationType = %q, want %q", ev.NotificationType, tc.wantNotifType)
			}
			if got := ev.StatusType(); got != tc.wantStatusType {
				t.Errorf("StatusType() = %q, want %q", got, tc.wantStatusType)
			}
			if got := ev.HasPermission(); got != tc.wantHasPermission {
				t.Errorf("HasPermission() = %v, want %v", got, tc.wantHasPermission)
			}
		})
	}
}

// TestParseHookEvent_MalformedJSON verifies that malformed JSON returns an error.
func TestParseHookEvent_MalformedJSON(t *testing.T) {
	_, err := claudecode.ParseHookEvent([]byte(`not json`))
	if err == nil {
		t.Error("ParseHookEvent(malformed): expected error, got nil")
	}
}

// TestHookEvent_SessionEnd_IdleNotRemoval verifies that SessionEnd maps to
// "idle" (not a removal signal) so the transcript row persists and a poll
// would resurrect a removed row.
func TestHookEvent_SessionEnd_IdleNotRemoval(t *testing.T) {
	ev, err := claudecode.ParseHookEvent([]byte(`{"hook_event_name":"SessionEnd","session_id":"ses-end","cwd":"/tmp/proj"}`))
	if err != nil {
		t.Fatalf("ParseHookEvent: %v", err)
	}
	if ev.StatusType() != "idle" {
		t.Errorf("SessionEnd StatusType() = %q, want %q", ev.StatusType(), "idle")
	}
	if ev.HasPermission() {
		t.Error("SessionEnd HasPermission() = true, want false")
	}
}

// TestHookEvent_Stop_IdleNotRemoval verifies that Stop maps to "idle" (teardown
// clears busy→idle, does NOT remove the row).
func TestHookEvent_Stop_IdleNotRemoval(t *testing.T) {
	ev, err := claudecode.ParseHookEvent([]byte(`{"hook_event_name":"Stop","session_id":"ses-stop"}`))
	if err != nil {
		t.Fatalf("ParseHookEvent: %v", err)
	}
	if ev.StatusType() != "idle" {
		t.Errorf("Stop StatusType() = %q, want %q", ev.StatusType(), "idle")
	}
}

// TestParseHookEvent_ToolName verifies that ParseHookEvent extracts the tool
// name from the tool_name, tool, and toolName fields (first non-empty wins).
func TestParseHookEvent_ToolName(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantToolName string
	}{
		{
			name:         "tool_name canonical key",
			json:         `{"hook_event_name":"PreToolUse","session_id":"s1","tool_name":"AskUserQuestion"}`,
			wantToolName: "AskUserQuestion",
		},
		{
			name:         "tool fallback key",
			json:         `{"hook_event_name":"PreToolUse","session_id":"s2","tool":"Bash"}`,
			wantToolName: "Bash",
		},
		{
			name:         "toolName camelCase fallback",
			json:         `{"hook_event_name":"PreToolUse","session_id":"s3","toolName":"Read"}`,
			wantToolName: "Read",
		},
		{
			name:         "tool_name wins over tool when both present",
			json:         `{"hook_event_name":"PreToolUse","session_id":"s4","tool_name":"AskUserQuestion","tool":"Bash"}`,
			wantToolName: "AskUserQuestion",
		},
		{
			name:         "no tool field → empty",
			json:         `{"hook_event_name":"PreToolUse","session_id":"s5"}`,
			wantToolName: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := claudecode.ParseHookEvent([]byte(tc.json))
			if err != nil {
				t.Fatalf("ParseHookEvent: %v", err)
			}
			if ev.ToolName != tc.wantToolName {
				t.Errorf("ToolName = %q, want %q", ev.ToolName, tc.wantToolName)
			}
		})
	}
}

// TestHookEvent_IsQuestionTool verifies that IsQuestionTool returns true only
// for PreToolUse events with ToolName=="AskUserQuestion".
func TestHookEvent_IsQuestionTool(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{
			name: "PreToolUse + AskUserQuestion → true",
			json: `{"hook_event_name":"PreToolUse","session_id":"s1","tool_name":"AskUserQuestion"}`,
			want: true,
		},
		{
			name: "PreToolUse + other tool → false",
			json: `{"hook_event_name":"PreToolUse","session_id":"s2","tool_name":"Bash"}`,
			want: false,
		},
		{
			name: "PreToolUse + no tool → false",
			json: `{"hook_event_name":"PreToolUse","session_id":"s3"}`,
			want: false,
		},
		{
			name: "PostToolUse + AskUserQuestion → false (wrong event)",
			json: `{"hook_event_name":"PostToolUse","session_id":"s4","tool_name":"AskUserQuestion"}`,
			want: false,
		},
		{
			name: "Notification + AskUserQuestion → false (wrong event)",
			json: `{"hook_event_name":"Notification","session_id":"s5","tool_name":"AskUserQuestion"}`,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := claudecode.ParseHookEvent([]byte(tc.json))
			if err != nil {
				t.Fatalf("ParseHookEvent: %v", err)
			}
			if got := ev.IsQuestionTool(); got != tc.want {
				t.Errorf("IsQuestionTool() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHookEvent_Notification_PermissionPrompt verifies the permission_prompt
// notification type is correctly classified as a permission event.
func TestHookEvent_Notification_PermissionPrompt(t *testing.T) {
	ev, err := claudecode.ParseHookEvent([]byte(`{"hook_event_name":"Notification","session_id":"ses-perm","notification_type":"permission_prompt"}`))
	if err != nil {
		t.Fatalf("ParseHookEvent: %v", err)
	}
	if ev.StatusType() != "busy" {
		t.Errorf("Notification/permission_prompt StatusType() = %q, want %q", ev.StatusType(), "busy")
	}
	if !ev.HasPermission() {
		t.Error("Notification/permission_prompt HasPermission() = false, want true")
	}
}
