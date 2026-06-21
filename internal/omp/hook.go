package omp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/guilhermehto/cogitator/internal/hookipc"
)

// hookSocketFilename is the filename of the Unix-domain socket used by the
// omp hook IPC. It is passed to hookipc.SocketPath to form the full path.
const hookSocketFilename = "cogitator-omp-hook.sock"

// HookSocketPath returns the Unix-domain socket path used by both the
// omp-hook sender and the listener. It is the single source of truth for the
// path so both sides always agree.
//
// Preference order:
//  1. $XDG_RUNTIME_DIR/cogitator-omp-hook.sock
//  2. os.TempDir()/cogitator-omp-hook.sock
func HookSocketPath() string {
	return hookipc.SocketPath(hookSocketFilename)
}

// ErrListenerOwned is returned by hookipc.Listen when a live cogitator instance
// already owns the socket. Re-exported here so callers in the omp package do
// not need to import hookipc directly.
var ErrListenerOwned = hookipc.ErrListenerOwned

// ErrListenerUnavailable is returned (wrapped) by hookipc.SendHook when the
// hook listener socket cannot be dialled. Re-exported here so cmd/cogitator
// can check it without importing hookipc directly.
var ErrListenerUnavailable = hookipc.ErrListenerUnavailable

// SendHook reads all bytes from stdin, dials the omp hook socket, and writes a
// length-framed message. It delegates to hookipc.SendHook with the omp socket
// path.
func SendHook(ctx context.Context, stdin io.Reader) error {
	return hookipc.SendHook(ctx, HookSocketPath(), stdin)
}

// WriteFrame writes a length-prefixed frame to w. It delegates to
// hookipc.WriteFrame and is retained here so in-package callers (e.g. tests)
// do not need to import hookipc directly.
func WriteFrame(w io.Writer, payload []byte) error {
	return hookipc.WriteFrame(w, payload)
}

// ReadFrame reads a length-prefixed frame from r. It delegates to
// hookipc.ReadFrame and is retained here so callers do not need to import
// hookipc directly.
func ReadFrame(r io.Reader) ([]byte, error) {
	return hookipc.ReadFrame(r)
}

// HookEvent is the parsed representation of an omp lifecycle hook payload sent
// by the shipped cogitator omp extension (see extensions/cogitator.ts).
//
// Wire field names (snake_case), emitted by the extension:
//   - hook_event_name — the lifecycle event name
//   - session_id      — the session id (matches the header id on disk)
//   - cwd             — the working directory for the session
//   - tool_name       — the tool name (only set for tool_call/tool_result)
//   - is_error        — true when a tool_result failed
//
// The parser is defensive: it tries multiple candidate field names for each
// logical field so a single correction here fixes all callers.
type HookEvent struct {
	// EventName is the normalised event name (snake_case).
	EventName string

	// SessionID is the session identifier (may be empty; callers fall back to CWD).
	SessionID string

	// CWD is the working directory for the session.
	CWD string

	// ToolName is the tool name carried by tool_call/tool_result events.
	ToolName string

	// IsError is true when the event carries an error indicator.
	IsError bool
}

// hookEventNames maps wire names to a canonical snake_case form. Unknown names
// are passed through as-is. omp emits snake_case natively; the alias entries
// guard against a future PascalCase emitter.
var hookEventNames = map[string]string{
	"session_start":    "session_start",
	"SessionStart":     "session_start",
	"turn_start":       "turn_start",
	"TurnStart":        "turn_start",
	"turn_end":         "turn_end",
	"TurnEnd":          "turn_end",
	"agent_start":      "agent_start",
	"AgentStart":       "agent_start",
	"agent_end":        "agent_end",
	"AgentEnd":         "agent_end",
	"tool_call":        "tool_call",
	"ToolCall":         "tool_call",
	"tool_result":      "tool_result",
	"ToolResult":       "tool_result",
	"session_shutdown": "session_shutdown",
	"SessionShutdown":  "session_shutdown",
}

// ParseHookEvent parses a raw hook JSON payload into a HookEvent. Unknown
// fields are ignored; an unrecognised event name is stored as-is (no panic, no
// error). Returns an error only for malformed JSON.
func ParseHookEvent(raw []byte) (HookEvent, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return HookEvent{}, fmt.Errorf("omp hook: parse event JSON: %w", err)
	}

	ev := HookEvent{}

	// Event name: hook_event_name, then event, then type.
	for _, key := range []string{"hook_event_name", "event", "type"} {
		if s, ok := stringField(m, key); ok {
			if canonical, found := hookEventNames[s]; found {
				ev.EventName = canonical
			} else {
				ev.EventName = s
			}
			break
		}
	}

	// Session id: session_id, sessionId, id.
	for _, key := range []string{"session_id", "sessionId", "id"} {
		if s, ok := stringField(m, key); ok {
			ev.SessionID = s
			break
		}
	}

	// CWD: cwd, directory.
	for _, key := range []string{"cwd", "directory"} {
		if s, ok := stringField(m, key); ok {
			ev.CWD = s
			break
		}
	}

	// Tool name: tool_name, toolName.
	for _, key := range []string{"tool_name", "toolName"} {
		if s, ok := stringField(m, key); ok {
			ev.ToolName = s
			break
		}
	}

	// Error indicator: is_error (bool) or a non-empty "error" string.
	if v, ok := m["is_error"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err == nil && b {
			ev.IsError = true
		}
	}
	if !ev.IsError {
		if s, ok := stringField(m, "error"); ok && s != "" {
			ev.IsError = true
		}
	}

	return ev, nil
}

// stringField returns the non-empty string value of m[key], if present and a
// JSON string.
func stringField(m map[string]json.RawMessage, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil || s == "" {
		return "", false
	}
	return s, true
}
