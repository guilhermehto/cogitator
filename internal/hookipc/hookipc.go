// Package hookipc provides a reusable Unix-domain socket transport for
// length-framed hook IPC. It is intentionally free of internal/ui, bubbletea,
// and any agent-specific logic so multiple agents can share the same wire
// protocol.
package hookipc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// dialTimeout is the maximum time allowed to dial a listener socket.
	// Kept short so a missing/hung listener never stalls the caller.
	dialTimeout = 2 * time.Second

	// writeTimeout is the maximum time allowed to write a framed message.
	writeTimeout = 2 * time.Second

	// readTimeout is the per-frame read deadline on the listener side.
	readTimeout = 5 * time.Second

	// maxFrameSize is the upper bound on a single frame payload. Payloads are
	// small JSON objects; 1 MiB is generous. Enforced before allocation so a
	// corrupt or malicious length prefix cannot cause a ~4 GiB allocation.
	maxFrameSize = 1 << 20 // 1 MiB
)

// ErrListenerOwned is returned by Listen when a live instance already owns the
// socket. The caller should run without the hook listener.
var ErrListenerOwned = errors.New("hook socket owned by another instance")

// ErrListenerUnavailable is returned (wrapped) by SendHook when the hook
// listener socket cannot be dialled — the expected, benign case where no
// listener is running. Callers typically treat this as success and exit 0 so
// the invoking tool never surfaces a "hook failed" banner for an absent monitor.
var ErrListenerUnavailable = errors.New("hook listener unavailable")

// RuntimePath returns a path under the runtime directory for the given
// filename. It is the single source of truth for where cogitator places
// ephemeral runtime files (hook sockets, the single-instance pidfile).
//
// Preference order:
//  1. $XDG_RUNTIME_DIR/<filename>
//  2. os.TempDir()/<filename>
func RuntimePath(filename string) string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, filename)
	}
	return filepath.Join(os.TempDir(), filename)
}

// SocketPath returns the Unix-domain socket path for the given filename. It is
// a thin wrapper over RuntimePath so the socket and pidfile always resolve to
// the same runtime directory.
func SocketPath(filename string) string {
	return RuntimePath(filename)
}

// Listen binds the Unix-domain socket at sockPath and calls handler for each
// framed message received. It returns ErrListenerOwned when another live
// instance already owns the socket (the caller should log and continue without
// a listener). Any other non-nil error is a fatal bind failure.
//
// Bind logic:
//  1. Attempt net.Listen("unix", sockPath).
//  2. On EADDRINUSE: dial the path.
//     - Dial succeeds → live owner exists → return ErrListenerOwned.
//     - Dial fails (stale socket) → os.Remove(sockPath) then bind again.
//
// The returned cleanup func stops the listener and unlinks the socket. It is
// safe to call multiple times. Listen blocks until ctx is cancelled or a fatal
// error occurs; cleanup is also triggered by ctx cancellation.
func Listen(ctx context.Context, sockPath string, handler func(raw []byte), logger *slog.Logger) (cleanup func(), err error) {
	if logger == nil {
		logger = slog.Default()
	}

	ln, err := bindSocket(ctx, sockPath)
	if err != nil {
		return noop, err
	}

	cleanup, done := superviseListener(ctx, ln, sockPath)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				logAcceptError(err, done, logger)
				return
			}
			// serveConn is called synchronously (not in a goroutine) so that
			// frames are read and dispatched in the order connections arrive.
			// Each sender writes exactly one frame and closes the connection,
			// so the read completes quickly (bounded by readTimeout).
			// Serialising here prevents a later hook event from being processed
			// before an earlier one when two senders connect in rapid succession.
			serveConn(conn, handler, logger)
		}
	}()

	return cleanup, nil
}

// ListenRPC binds the Unix-domain socket at sockPath and answers each framed
// request with the framed bytes returned by handler. Bind, stale-socket
// detection, permissions, and cleanup semantics match Listen. Connections are
// served concurrently so a slow client cannot block others.
func ListenRPC(ctx context.Context, sockPath string, handler func(req []byte) []byte, logger *slog.Logger) (cleanup func(), err error) {
	if logger == nil {
		logger = slog.Default()
	}

	ln, err := bindSocket(ctx, sockPath)
	if err != nil {
		return noop, err
	}

	cleanup, done := superviseListener(ctx, ln, sockPath)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				logAcceptError(err, done, logger)
				return
			}
			go serveRPCConn(conn, handler, logger)
		}
	}()

	return cleanup, nil
}

// bindSocket listens on sockPath, replacing a stale socket file and returning
// ErrListenerOwned when a live instance answers. The socket is restricted to
// the owner only.
func bindSocket(ctx context.Context, sockPath string) (net.Listener, error) {
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		if !isAddrInUse(err) {
			return nil, fmt.Errorf("hookipc: listen %s: %w", sockPath, err)
		}
		// EADDRINUSE — check if the existing socket is live or stale.
		dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
		conn, dialErr := (&net.Dialer{}).DialContext(dialCtx, "unix", sockPath)
		cancel()
		if dialErr == nil {
			// A live owner answered — back off.
			conn.Close()
			return nil, ErrListenerOwned
		}
		// Stale socket — remove and retry.
		if rmErr := os.Remove(sockPath); rmErr != nil && !os.IsNotExist(rmErr) {
			return nil, fmt.Errorf("hookipc: remove stale socket %s: %w", sockPath, rmErr)
		}
		ln, err = net.Listen("unix", sockPath)
		if err != nil {
			return nil, fmt.Errorf("hookipc: listen after stale removal %s: %w", sockPath, err)
		}
	}

	// Restrict the socket to the owner only. Without this, any local user can
	// connect and inject framed JSON when the socket falls back to
	// os.TempDir() (the macOS default when $XDG_RUNTIME_DIR is unset).
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("hookipc: chmod socket %s: %w", sockPath, err)
	}
	return ln, nil
}

// superviseListener returns a cleanup func that closes ln and unlinks sockPath
// exactly once, and a channel closed when cleanup has started. Cleanup is also
// triggered by ctx cancellation.
func superviseListener(ctx context.Context, ln net.Listener, sockPath string) (cleanup func(), done <-chan struct{}) {
	doneCh := make(chan struct{})
	var once sync.Once

	cleanup = func() {
		once.Do(func() {
			// Signal done BEFORE closing the listener so the accept goroutine
			// sees the closed channel and skips the "unexpected error" log.
			close(doneCh)
			ln.Close()
			os.Remove(sockPath) //nolint:errcheck // best-effort unlink
		})
	}

	go func() {
		select {
		case <-ctx.Done():
			cleanup()
		case <-doneCh:
		}
	}()

	return cleanup, doneCh
}

func logAcceptError(err error, done <-chan struct{}, logger *slog.Logger) {
	select {
	case <-done:
	default:
		logger.Warn("hookipc: accept error", "err", err)
	}
}

// serveRPCConn reads one request frame, writes the handler's reply frame, and
// closes conn.
func serveRPCConn(conn net.Conn, handler func(req []byte) []byte, logger *slog.Logger) {
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		logger.Warn("hookipc: set read deadline", "err", err)
		return
	}
	req, err := ReadFrame(conn)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			logger.Warn("hookipc: read frame", "err", err)
		}
		return
	}

	reply := handler(req)

	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		logger.Warn("hookipc: set write deadline", "err", err)
		return
	}
	if err := WriteFrame(conn, reply); err != nil {
		logger.Warn("hookipc: write reply", "err", err)
	}
}

// Call sends req as one frame to the RPC listener at sockPath and returns the
// reply frame. A dial failure wraps ErrListenerUnavailable.
func Call(ctx context.Context, sockPath string, req []byte) ([]byte, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("hookipc: dial %s: %w: %w", sockPath, ErrListenerUnavailable, err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return nil, fmt.Errorf("hookipc: set write deadline: %w", err)
	}
	if err := WriteFrame(conn, req); err != nil {
		return nil, fmt.Errorf("hookipc: write frame: %w", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return nil, fmt.Errorf("hookipc: set read deadline: %w", err)
	}
	reply, err := ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("hookipc: read reply: %w", err)
	}
	return reply, nil
}

// serveConn reads a single framed message from conn and calls handler.
func serveConn(conn net.Conn, handler func(raw []byte), logger *slog.Logger) {
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		logger.Warn("hookipc: set read deadline", "err", err)
		return
	}
	payload, err := ReadFrame(conn)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			logger.Warn("hookipc: read frame", "err", err)
		}
		return
	}
	handler(payload)
}

// SendHook reads all bytes from stdin, dials the Unix-domain socket at
// sockPath, and writes a length-framed message. It returns a non-nil error if
// the dial or write fails. The function always returns within
// dialTimeout + writeTimeout so it never blocks the caller.
func SendHook(ctx context.Context, sockPath string, stdin io.Reader) error {
	payload, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("hookipc: read stdin: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "unix", sockPath)
	if err != nil {
		// No live listener (socket missing, connection refused, or timeout).
		// Mark with ErrListenerUnavailable so the caller can exit 0 — a closed
		// monitor is not a failure the invoking tool should ever surface.
		return fmt.Errorf("hookipc: dial %s: %w: %w", sockPath, ErrListenerUnavailable, err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("hookipc: set write deadline: %w", err)
	}

	if err := WriteFrame(conn, payload); err != nil {
		return fmt.Errorf("hookipc: write frame: %w", err)
	}
	return nil
}

// WriteFrame writes a length-prefixed frame to w. The frame format is:
//
//	[4 bytes big-endian uint32 length][payload bytes]
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
// It is the counterpart to WriteFrame. Returns an error if the declared frame
// size exceeds maxFrameSize (1 MiB) to prevent a corrupt or malicious length
// prefix from causing a huge allocation.
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxFrameSize {
		return nil, fmt.Errorf("hookipc: frame size %d exceeds maximum %d", size, maxFrameSize)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// isAddrInUse reports whether err is an "address already in use" error.
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Err.Error() == "bind: address already in use" ||
			opErr.Err.Error() == "listen: address already in use"
	}
	return err != nil && containsStr(err.Error(), "address already in use")
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// noop is a no-op cleanup function.
func noop() {}
