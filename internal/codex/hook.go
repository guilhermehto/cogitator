package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/guilhermehto/cogitator/internal/hookipc"
)

// hookSocketFilename is the filename of the Unix-domain socket used by the
// codex hook IPC. It is passed to hookipc.SocketPath to form the full path.
const hookSocketFilename = "cogitator-codex-hook.sock"

// HookSocketPath returns the Unix-domain socket path used by both the
// codex-hook sender and the listener. It is the single source of truth for
// the path so both sides always agree.
//
// Preference order:
//  1. $XDG_RUNTIME_DIR/cogitator-codex-hook.sock
//  2. os.TempDir()/cogitator-codex-hook.sock
func HookSocketPath() string {
	return hookipc.SocketPath(hookSocketFilename)
}

// ErrListenerOwned is returned by hookipc.Listen when a live cogitator instance
// already owns the socket. Re-exported here so callers in the codex package do
// not need to import hookipc directly.
var ErrListenerOwned = hookipc.ErrListenerOwned

// ErrListenerUnavailable is returned (wrapped) by hookipc.SendHook when the
// hook listener socket cannot be dialled. Re-exported here so cmd/cogitator
// can check it without importing hookipc directly.
var ErrListenerUnavailable = hookipc.ErrListenerUnavailable

// SendHook reads all bytes from stdin, dials the codex hook socket, and writes
// a length-framed message. It delegates to hookipc.SendHook with the codex
// socket path.
func SendHook(ctx context.Context, stdin io.Reader) error {
	return hookipc.SendHook(ctx, HookSocketPath(), stdin)
}

// WriteFrame writes a length-prefixed frame to w. It delegates to
// hookipc.WriteFrame and is retained here so existing callers (e.g.
// provider_test.go) do not need to import hookipc directly.
func WriteFrame(w io.Writer, payload []byte) error {
	return hookipc.WriteFrame(w, payload)
}

// ReadFrame reads a length-prefixed frame from r. It delegates to
// hookipc.ReadFrame and is retained here so existing callers do not need to
// import hookipc directly.
func ReadFrame(r io.Reader) ([]byte, error) {
	return hookipc.ReadFrame(r)
}

// HookEvent is the parsed representation of a Codex lifecycle hook payload.
//
// Canonical stdin field names are verified against the official Codex hooks
// docs (developers.openai.com/codex/hooks, codex-cli 0.136.0):
//   - hook_event_name — the lifecycle event name
//   - session_id      — the session identifier
//   - cwd             — the working directory for the session
//
// The parser is defensive: it tries multiple candidate field names for each
// logical field so that a single correction here fixes all callers.
//
// Confirmed event names (snake_case wire): session_start, user_prompt_submit,
// pre_tool_use, post_tool_use, permission_request, stopped, notification.
// PascalCase aliases (SessionStart, Stop, …) are also accepted.
type HookEvent struct {
	// EventName is the normalised event name (snake_case).
	EventName string

	// SessionID is the session identifier (may be empty; callers fall back to CWD).
	SessionID string

	// CWD is the working directory for the session.
	CWD string

	// IsError is true when the event carries an error indicator.
	IsError bool
}

// hookEventNames maps both PascalCase and snake_case wire names to a canonical
// snake_case form. Unknown names are passed through as-is (lowercased).
var hookEventNames = map[string]string{
	"SessionStart":       "session_start",
	"session_start":      "session_start",
	"UserPromptSubmit":   "user_prompt_submit",
	"user_prompt_submit": "user_prompt_submit",
	"PreToolUse":         "pre_tool_use",
	"pre_tool_use":       "pre_tool_use",
	"PostToolUse":        "post_tool_use",
	"post_tool_use":      "post_tool_use",
	"PermissionRequest":  "permission_request",
	"permission_request": "permission_request",
	"Notification":       "notification",
	"notification":       "notification",
	"Stop":               "stopped",
	"stopped":            "stopped",
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
		return HookEvent{}, fmt.Errorf("codex hook: parse event JSON: %w", err)
	}

	ev := HookEvent{}

	// Event name: try hook_event_name, event, type (in that order).
	// hook_event_name is the canonical key per the official Codex hooks docs
	// (developers.openai.com/codex/hooks, verified against codex-cli 0.136.0).
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
	// session_id is the canonical key per the official Codex hooks docs
	// (developers.openai.com/codex/hooks, verified against codex-cli 0.136.0).
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
	// cwd is the canonical key per the official Codex hooks docs
	// (developers.openai.com/codex/hooks, verified against codex-cli 0.136.0).
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

	// Error indicator: look for a non-empty "error" field.
	if v, ok := m["error"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			ev.IsError = true
		}
	}

	return ev, nil
}
