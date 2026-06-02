package codex

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
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
		return fmt.Errorf("codex-hook: dial %s: %w", sockPath, err)
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
// (step 10) so they share a single implementation.
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
// It is the counterpart to WriteFrame and is exported for use by the listener
// in step 10.
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(hdr[:])
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
