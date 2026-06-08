package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/guilhermehto/cogitator/internal/hookipc"
)

// hookSocketFilename is the filename of the Unix-domain socket used by the
// Claude Code hook IPC. It is passed to hookipc.SocketPath to form the full
// path. Kept separate from the codex socket so both agents can run side-by-side.
const hookSocketFilename = "cogitator-claude-hook.sock"

// HookSocketPath returns the Unix-domain socket path used by both the
// claude-hook sender and the listener. It is the single source of truth for
// the path so both sides always agree.
//
// Preference order:
//  1. $XDG_RUNTIME_DIR/cogitator-claude-hook.sock
//  2. os.TempDir()/cogitator-claude-hook.sock
func HookSocketPath() string {
	return hookipc.SocketPath(hookSocketFilename)
}

// ErrListenerOwned is returned by hookipc.Listen when a live cogitator instance
// already owns the socket. Re-exported here so callers in the claudecode package
// do not need to import hookipc directly.
var ErrListenerOwned = hookipc.ErrListenerOwned

// ErrListenerUnavailable is returned (wrapped) by hookipc.SendHook when the
// hook listener socket cannot be dialled. Re-exported here so cmd/cogitator
// can check it without importing hookipc directly.
var ErrListenerUnavailable = hookipc.ErrListenerUnavailable

// SendHook reads all bytes from stdin, dials the Claude Code hook socket, and
// writes a length-framed message. It delegates to hookipc.SendHook with the
// claude socket path.
func SendHook(ctx context.Context, stdin io.Reader) error {
	return hookipc.SendHook(ctx, HookSocketPath(), stdin)
}

// ListenHooks binds the Claude Code hook socket and calls handler for each
// framed message received. It returns ErrListenerOwned when another live
// cogitator instance already owns the socket. Any other non-nil error is a
// fatal bind failure.
//
// The returned cleanup func stops the listener and unlinks the socket. It is
// safe to call multiple times.
func ListenHooks(ctx context.Context, handler func(raw []byte), logger *slog.Logger) (cleanup func(), err error) {
	return hookipc.Listen(ctx, HookSocketPath(), handler, logger)
}

// HookEvent is the parsed representation of a Claude Code lifecycle hook payload.
//
// Canonical stdin field names per the Claude Code hooks documentation:
//   - hook_event_name    — the lifecycle event name (PascalCase)
//   - session_id         — the session identifier
//   - cwd               — the working directory for the session
//   - notification_type  — present on Notification events (e.g. "permission_prompt")
//   - tool_name          — present on PreToolUse/PostToolUse events
//
// The parser is defensive: it tries multiple candidate field names for each
// logical field so that a single correction here fixes all callers.
//
// The authoritative event→status mapping lives in provider.handleHookFrame.
// Known event names (PascalCase wire format from Claude Code):
// SessionStart, UserPromptSubmit, PreToolUse, PostToolUse → busy/active
// Stop → idle (teardown: clear busy→idle, NOT row removal)
// SessionEnd → idle (teardown: transcript persists; poll would resurrect a removed row)
// Notification with notification_type=permission_prompt → permission
// PermissionRequest → permission
type HookEvent struct {
	// EventName is the normalised event name (PascalCase, as sent by Claude Code).
	EventName string

	// SessionID is the session identifier (may be empty; callers fall back to CWD).
	SessionID string

	// CWD is the working directory for the session.
	CWD string

	// NotificationType is the value of the notification_type field, present on
	// Notification events (e.g. "permission_prompt").
	NotificationType string

	// ToolName is the name of the tool being invoked, present on PreToolUse and
	// PostToolUse events (e.g. "AskUserQuestion").
	ToolName string
}

// hookEventNames maps both PascalCase and snake_case wire names to a canonical
// PascalCase form. Unknown names are passed through as-is.
var hookEventNames = map[string]string{
	"SessionStart":       "SessionStart",
	"session_start":      "SessionStart",
	"UserPromptSubmit":   "UserPromptSubmit",
	"user_prompt_submit": "UserPromptSubmit",
	"PreToolUse":         "PreToolUse",
	"pre_tool_use":       "PreToolUse",
	"PostToolUse":        "PostToolUse",
	"post_tool_use":      "PostToolUse",
	"Stop":               "Stop",
	"stopped":            "Stop",
	"SessionEnd":         "SessionEnd",
	"session_end":        "SessionEnd",
	"Notification":       "Notification",
	"notification":       "Notification",
	"PermissionRequest":  "PermissionRequest",
	"permission_request": "PermissionRequest",
}

// ParseHookEvent parses a raw hook JSON payload into a HookEvent.
// Unknown fields are ignored; an unrecognised event name is stored as-is
// (no panic, no error). Returns an error only for malformed JSON.
//
// Field extraction is centralised here so live verification can correct field
// names in ONE place.
func ParseHookEvent(raw []byte) (HookEvent, error) {
	// Use a loose map to tolerate any field layout.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return HookEvent{}, fmt.Errorf("claudecode hook: parse event JSON: %w", err)
	}

	ev := HookEvent{}

	// Event name: try hook_event_name, event, type (in that order).
	// hook_event_name is the canonical key per the Claude Code hooks docs.
	// Fallback candidates are kept for defensive tolerance.
	for _, key := range []string{"hook_event_name", "event", "type"} {
		if v, ok := m[key]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				if canonical, ok := hookEventNames[s]; ok {
					ev.EventName = canonical
				} else {
					ev.EventName = s
				}
				break
			}
		}
	}

	// Session ID: try session_id, sessionId, id.
	// session_id is the canonical key per the Claude Code hooks docs.
	// Fallback candidates are kept for defensive tolerance.
	for _, key := range []string{"session_id", "sessionId", "id"} {
		if v, ok := m[key]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				ev.SessionID = s
				break
			}
		}
	}

	// CWD: try cwd, directory.
	// cwd is the canonical key per the Claude Code hooks docs.
	// Fallback candidates are kept for defensive tolerance.
	for _, key := range []string{"cwd", "directory"} {
		if v, ok := m[key]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				ev.CWD = s
				break
			}
		}
	}

	// notification_type: present on Notification events.
	if v, ok := m["notification_type"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			ev.NotificationType = s
		}
	}

	// tool_name: present on PreToolUse/PostToolUse events.
	// Try tool_name, tool, toolName (first non-empty wins).
	for _, key := range []string{"tool_name", "tool", "toolName"} {
		if v, ok := m[key]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				ev.ToolName = s
				break
			}
		}
	}

	return ev, nil
}

// HasPermission reports whether this event signals a pending permission request.
// True for PermissionRequest events and Notification events with
// notification_type=permission_prompt.
func (e HookEvent) HasPermission() bool {
	switch e.EventName {
	case "PermissionRequest":
		return true
	case "Notification":
		return e.NotificationType == "permission_prompt"
	default:
		return false
	}
}

// IsQuestionTool reports whether this event is a PreToolUse for the
// AskUserQuestion tool — Claude's mechanism for asking the user a question
// mid-session. This is distinct from a real permission prompt.
func (e HookEvent) IsQuestionTool() bool {
	return e.EventName == "PreToolUse" && e.ToolName == "AskUserQuestion"
}
