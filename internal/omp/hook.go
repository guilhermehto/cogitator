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

// HookSocketPath returns the Unix-domain socket path used by both the omp-hook
// sender and the listener. It is the single source of truth for the path so
// both sides always agree.
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
// hook listener socket cannot be dialled. Re-exported here so cmd/cogitator can
// check it without importing hookipc directly.
var ErrListenerUnavailable = hookipc.ErrListenerUnavailable

// SendHook reads all bytes from stdin, dials the omp hook socket, and writes a
// length-framed message. It delegates to hookipc.SendHook with the omp socket
// path.
func SendHook(ctx context.Context, stdin io.Reader) error {
	return hookipc.SendHook(ctx, HookSocketPath(), stdin)
}

// WriteFrame writes a length-prefixed frame to w. It delegates to
// hookipc.WriteFrame and is retained here so tests do not need to import
// hookipc directly.
func WriteFrame(w io.Writer, payload []byte) error {
	return hookipc.WriteFrame(w, payload)
}

// ReadFrame reads a length-prefixed frame from r. It delegates to
// hookipc.ReadFrame and is retained here so tests do not need to import
// hookipc directly.
func ReadFrame(r io.Reader) ([]byte, error) {
	return hookipc.ReadFrame(r)
}

// HookEvent is the parsed representation of an omp lifecycle hook payload.
//
// The omp hook bridge (internal/omp/extension.js) forwards a small JSON object
// per event:
//
//	{
//	  "hook_event_name": "<event>",   // omp event name (snake_case)
//	  "cwd":             "<path>",    // ctx.cwd
//	  "session_file":    "<path>",    // ctx.sessionManager.getSessionFile()
//	  "tool_name":       "<name>"     // present for tool_call/tool_result
//	}
//
// The session id is not sent directly; it is recovered from the session_file
// basename (<timestamp>_<sessionId>.jsonl) and falls back to a cwd lookup in
// the provider when the file path is unavailable.
type HookEvent struct {
	// EventName is the normalised omp event name (snake_case).
	EventName string

	// SessionID is the session id extracted from session_file (may be empty;
	// the provider falls back to a cwd lookup).
	SessionID string

	// CWD is the working directory for the session.
	CWD string

	// ToolName is the tool involved (set for tool_call/tool_result events).
	ToolName string
}

// hookEventNames maps wire event names to a canonical snake_case form. omp
// emits snake_case natively; PascalCase aliases are accepted defensively.
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
// fields are ignored; an unrecognised event name is stored as-is (lowercased
// passthrough). Returns an error only for malformed JSON.
func ParseHookEvent(raw []byte) (HookEvent, error) {
	var m struct {
		HookEventName string `json:"hook_event_name"`
		Event         string `json:"event"`
		Type          string `json:"type"`
		SessionID     string `json:"session_id"`
		SessionFile   string `json:"session_file"`
		CWD           string `json:"cwd"`
		ToolName      string `json:"tool_name"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return HookEvent{}, fmt.Errorf("omp hook: parse event JSON: %w", err)
	}

	ev := HookEvent{
		SessionID: m.SessionID,
		CWD:       m.CWD,
		ToolName:  m.ToolName,
	}

	for _, name := range []string{m.HookEventName, m.Event, m.Type} {
		if name == "" {
			continue
		}
		if canonical, ok := hookEventNames[name]; ok {
			ev.EventName = canonical
		} else {
			ev.EventName = name
		}
		break
	}

	// Recover the session id from the session file path when not sent directly.
	if ev.SessionID == "" && m.SessionFile != "" {
		ev.SessionID = SessionIDFromFilename(m.SessionFile)
	}

	return ev, nil
}
