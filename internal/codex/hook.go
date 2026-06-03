package codex

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	hookSocketFilename = "cogitator-codex-hook.sock"

	// hookDialTimeout is the maximum time allowed to dial the listener socket.
	// Kept short so a missing/hung listener never stalls Codex.
	hookDialTimeout = 2 * time.Second

	// hookWriteTimeout is the maximum time allowed to write the framed message.
	hookWriteTimeout = 2 * time.Second

	// hookReadTimeout is the per-frame read deadline on the listener side.
	hookReadTimeout = 5 * time.Second

	// maxFrameSize is the upper bound on a single hook frame payload. Hook
	// payloads are small JSON objects; 1 MiB is generous. Enforced before
	// allocation so a corrupt or malicious length prefix cannot cause a ~4 GiB
	// allocation.
	maxFrameSize = 1 << 20 // 1 MiB
)

// HookSocketPath returns the Unix-domain socket path used by both the
// codex-hook sender (this process) and the listener (step 10). It is the
// single source of truth for the path so both sides always agree.
//
// Preference order:
//  1. $XDG_RUNTIME_DIR/cogitator-codex-hook.sock
//  2. os.TempDir()/cogitator-codex-hook.sock
func HookSocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, hookSocketFilename)
	}
	return filepath.Join(os.TempDir(), hookSocketFilename)
}

// ErrListenerOwned is returned by Listen when a live cogitator instance already
// owns the socket. The caller should run without the hook listener.
var ErrListenerOwned = errors.New("codex hook socket owned by another instance")

// ErrListenerUnavailable is returned (wrapped) by SendHook when the hook
// listener socket cannot be dialled — the expected, benign case where no
// cogitator TUI is running. The codex-hook command treats this as success and
// exits 0 so Codex never surfaces a "hook failed" banner for an absent monitor.
var ErrListenerUnavailable = errors.New("codex hook listener unavailable")

// Listen binds the Unix-domain socket at HookSocketPath() and calls handler
// for each framed message received. It returns ErrListenerOwned when another
// live cogitator already owns the socket (the caller should log and continue
// without a listener). Any other non-nil error is a fatal bind failure.
//
// Bind logic:
//  1. Attempt net.Listen("unix", path).
//  2. On EADDRINUSE: dial the path.
//     - Dial succeeds → live owner exists → return ErrListenerOwned.
//     - Dial fails (stale socket) → os.Remove(path) then bind again.
//
// The returned cleanup func stops the listener and unlinks the socket. It is
// safe to call multiple times. Listen blocks until ctx is cancelled or a fatal
// error occurs; cleanup is also triggered by ctx cancellation.
func Listen(ctx context.Context, sockPath string, handler func(raw []byte), logger *slog.Logger) (cleanup func(), err error) {
	if logger == nil {
		logger = slog.Default()
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		if !isAddrInUse(err) {
			return noop, fmt.Errorf("codex hook: listen %s: %w", sockPath, err)
		}
		// EADDRINUSE — check if the existing socket is live or stale.
		dialCtx, cancel := context.WithTimeout(ctx, hookDialTimeout)
		conn, dialErr := (&net.Dialer{}).DialContext(dialCtx, "unix", sockPath)
		cancel()
		if dialErr == nil {
			// A live owner answered — back off.
			conn.Close()
			return noop, ErrListenerOwned
		}
		// Stale socket — remove and retry.
		if rmErr := os.Remove(sockPath); rmErr != nil && !os.IsNotExist(rmErr) {
			return noop, fmt.Errorf("codex hook: remove stale socket %s: %w", sockPath, rmErr)
		}
		ln, err = net.Listen("unix", sockPath)
		if err != nil {
			return noop, fmt.Errorf("codex hook: listen after stale removal %s: %w", sockPath, err)
		}
	}

	// Restrict the socket to the owner only. Without this, any local user can
	// connect and inject framed hook JSON when the socket falls back to
	// os.TempDir() (the macOS default when $XDG_RUNTIME_DIR is unset).
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		return noop, fmt.Errorf("codex hook: chmod socket %s: %w", sockPath, err)
	}

	done := make(chan struct{})
	cleanupOnce := make(chan struct{}, 1)
	cleanupOnce <- struct{}{}

	doCleanup := func() {
		select {
		case <-cleanupOnce:
			// Signal done BEFORE closing the listener so the accept goroutine
			// sees the closed channel and skips the "unexpected error" log.
			close(done)
			ln.Close()
			os.Remove(sockPath) //nolint:errcheck // best-effort unlink
		default:
		}
	}

	go func() {
		// Stop accepting when ctx is cancelled.
		go func() {
			select {
			case <-ctx.Done():
				doCleanup()
			case <-done:
			}
		}()

		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-done:
					return // clean shutdown
				default:
					logger.Warn("codex hook: accept error", "err", err)
					return
				}
			}
			// serveHookConn is called synchronously (not in a goroutine) so
			// that frames are read and dispatched in the order connections
			// arrive. Each hook sender writes exactly one frame and closes the
			// connection, so the read completes quickly (bounded by
			// hookReadTimeout). Serialising here prevents a later hook event
			// from being processed before an earlier one when two senders
			// connect in rapid succession.
			serveHookConn(conn, handler, logger)
		}
	}()

	return doCleanup, nil
}

// serveHookConn reads a single framed message from conn and calls handler.
func serveHookConn(conn net.Conn, handler func(raw []byte), logger *slog.Logger) {
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(hookReadTimeout)); err != nil {
		logger.Warn("codex hook: set read deadline", "err", err)
		return
	}
	payload, err := ReadFrame(conn)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			logger.Warn("codex hook: read frame", "err", err)
		}
		return
	}
	handler(payload)
}

// isAddrInUse reports whether err is an "address already in use" error.
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Err.Error() == "bind: address already in use" ||
			opErr.Err.Error() == "listen: address already in use"
	}
	// Fallback: check the string representation.
	return err != nil && (containsStr(err.Error(), "address already in use") ||
		containsStr(err.Error(), "bind: address already in use"))
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// noop is a no-op cleanup function.
func noop() {}

// SendHook reads all bytes from stdin, dials the Unix-domain socket at
// HookSocketPath(), and writes a length-framed message. It returns a non-nil
// error if the dial or write fails. The function always returns within
// hookDialTimeout + hookWriteTimeout so it never blocks Codex.
func SendHook(ctx context.Context, stdin io.Reader) error {
	payload, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("codex-hook: read stdin: %w", err)
	}

	sockPath := HookSocketPath()

	dialCtx, cancel := context.WithTimeout(ctx, hookDialTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "unix", sockPath)
	if err != nil {
		// No live listener (socket missing, connection refused, or timeout).
		// Mark with ErrListenerUnavailable so the caller can exit 0 — a closed
		// TUI is not a failure the Codex user should ever see.
		return fmt.Errorf("codex-hook: dial %s: %w: %w", sockPath, ErrListenerUnavailable, err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(time.Now().Add(hookWriteTimeout)); err != nil {
		return fmt.Errorf("codex-hook: set write deadline: %w", err)
	}

	if err := WriteFrame(conn, payload); err != nil {
		return fmt.Errorf("codex-hook: write frame: %w", err)
	}
	return nil
}

// WriteFrame writes a length-prefixed frame to w. The frame format is:
//
//	[4 bytes big-endian uint32 length][payload bytes]
//
// This is the framing used by both the sender (codex-hook) and the listener
// so they share a single implementation.
func WriteFrame(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return nil
}

// ReadFrame reads a length-prefixed frame from r and returns the payload.
// It is the counterpart to WriteFrame and is exported for use by the listener.
// Returns an error if the declared frame size exceeds maxFrameSize (1 MiB) to
// prevent a corrupt or malicious length prefix from causing a huge allocation.
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxFrameSize {
		return nil, fmt.Errorf("codex hook: frame size %d exceeds maximum %d", size, maxFrameSize)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
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
// Field extraction is centralised here so step-12 live verification can
// correct field names in ONE place.
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
