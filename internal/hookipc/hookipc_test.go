package hookipc_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/hookipc"
)

// shortSockPath creates a short Unix-domain socket path under os.TempDir()
// to stay within macOS's 104-character sun_path limit.
func shortSockPath(t *testing.T, name string) string {
	t.Helper()
	f, err := os.CreateTemp("", name+"*.sock")
	if err != nil {
		t.Fatalf("create temp socket path: %v", err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path) // remove the placeholder file; net.Listen will create the socket
	t.Cleanup(func() { os.Remove(path) })
	return path
}

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

// TestSocketPath_Consistency verifies that repeated calls return the same path
// and that the path contains the expected filename.
func TestSocketPath_Consistency(t *testing.T) {
	const filename = "cogitator-codex-hook.sock"
	first := hookipc.SocketPath(filename)
	second := hookipc.SocketPath(filename)
	if first != second {
		t.Errorf("SocketPath() not stable: %q != %q", first, second)
	}
	if !strings.HasSuffix(first, filename) {
		t.Errorf("SocketPath() = %q, want suffix %q", first, filename)
	}
}

// TestSocketPath_XDGRuntimeDir verifies that $XDG_RUNTIME_DIR is preferred
// over os.TempDir() when set.
func TestSocketPath_XDGRuntimeDir(t *testing.T) {
	dir := shortXDGDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)

	const filename = "cogitator-codex-hook.sock"
	got := hookipc.SocketPath(filename)
	want := filepath.Join(dir, filename)
	if got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
}

// TestSocketPath_TempDirFallback verifies that os.TempDir() is used when
// $XDG_RUNTIME_DIR is unset.
func TestSocketPath_TempDirFallback(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	const filename = "cogitator-codex-hook.sock"
	got := hookipc.SocketPath(filename)
	want := filepath.Join(os.TempDir(), filename)
	if got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
}

// TestFrameRoundTrip verifies that WriteFrame + ReadFrame round-trips arbitrary
// payloads without corruption.
func TestFrameRoundTrip(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"hook_event_name":"SessionStart","session_id":"abc"}`),
		[]byte(`{}`),
		[]byte(strings.Repeat("x", 65536)), // large payload
		[]byte{},                           // empty payload
	}

	for _, payload := range cases {
		var buf bytes.Buffer
		if err := hookipc.WriteFrame(&buf, payload); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
		got, err := hookipc.ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("round-trip mismatch: got %d bytes, want %d bytes", len(got), len(payload))
		}
	}
}

// TestFrameRoundTrip_ViaUnixSocket verifies that WriteFrame + ReadFrame work
// correctly over a real Unix-domain socket (not just an in-memory buffer).
func TestFrameRoundTrip_ViaUnixSocket(t *testing.T) {
	sockPath := shortSockPath(t, "cog-hook-rt")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	payload := []byte(`{"hook_event_name":"PostToolUse","session_id":"test-123"}`)

	received := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, err := hookipc.ReadFrame(conn)
		if err != nil {
			received <- nil
			return
		}
		received <- data
	}()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	defer conn.Close()

	if err := hookipc.WriteFrame(conn, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	select {
	case got := <-received:
		if !bytes.Equal(got, payload) {
			t.Errorf("socket round-trip: got %q, want %q", got, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for frame to be received")
	}
}

// TestSendHook_NoListener verifies that SendHook returns a non-nil error
// quickly when no listener is present — it must not hang.
func TestSendHook_NoListener(t *testing.T) {
	dir := shortXDGDir(t)
	sockPath := filepath.Join(dir, "no-listener.sock")

	start := time.Now()
	err := hookipc.SendHook(context.Background(), sockPath, strings.NewReader(`{"hook_event_name":"Stop"}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Error("SendHook: expected non-nil error when no listener present, got nil")
	}
	if !errors.Is(err, hookipc.ErrListenerUnavailable) {
		t.Errorf("SendHook error = %v, want wrapping ErrListenerUnavailable", err)
	}
	// Must return well within the combined dial+write timeout (4s). Allow 3.5s
	// to avoid flakiness on slow CI while still catching hangs.
	if elapsed > 3500*time.Millisecond {
		t.Errorf("SendHook took %v, want < 3.5s (must not block caller)", elapsed)
	}
}

// TestSendHook_DeliverPayload verifies that SendHook delivers the raw stdin
// bytes as a framed message to a listening socket.
func TestSendHook_DeliverPayload(t *testing.T) {
	dir := shortXDGDir(t)
	sockPath := filepath.Join(dir, "deliver.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"/tmp"}`

	received := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, err := hookipc.ReadFrame(conn)
		if err != nil {
			received <- nil
			return
		}
		received <- data
	}()

	if err := hookipc.SendHook(context.Background(), sockPath, strings.NewReader(payload)); err != nil {
		t.Fatalf("SendHook: %v", err)
	}

	select {
	case got := <-received:
		if string(got) != payload {
			t.Errorf("received %q, want %q", got, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for payload")
	}
}

// TestReadFrame_OversizedLength verifies that ReadFrame returns a clean error
// when the length prefix exceeds maxFrameSize (1 MiB) without allocating a
// huge buffer or panicking.
func TestReadFrame_OversizedLength(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 0xFFFFFFFF)

	r := bytes.NewReader(hdr[:])
	_, err := hookipc.ReadFrame(r)
	if err == nil {
		t.Fatal("ReadFrame with oversized length: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "frame size") && !strings.Contains(err.Error(), "maximum") {
		t.Errorf("ReadFrame oversized error = %q; want it to mention frame size or maximum", err.Error())
	}
}

// TestListen_StaleSocket verifies that Listen unlinks a stale socket file
// (no listener behind it) and successfully binds.
func TestListen_StaleSocket(t *testing.T) {
	dir := shortXDGDir(t)
	sockPath := filepath.Join(dir, "cog-stale.sock")

	// Create a stale socket file (no listener).
	f, err := os.Create(sockPath)
	if err != nil {
		t.Fatalf("create stale file: %v", err)
	}
	f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan []byte, 1)
	cleanup, err := hookipc.Listen(ctx, sockPath, func(raw []byte) {
		received <- raw
	}, nil)
	if err != nil {
		t.Fatalf("Listen on stale socket: %v", err)
	}
	defer cleanup()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial after stale removal: %v", err)
	}
	payload := []byte(`{"hook_event_name":"SessionStart","session_id":"s1"}`)
	if err := hookipc.WriteFrame(conn, payload); err != nil {
		conn.Close()
		t.Fatalf("WriteFrame: %v", err)
	}
	conn.Close()

	select {
	case got := <-received:
		if string(got) != string(payload) {
			t.Errorf("received %q, want %q", got, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for frame after stale-socket rebind")
	}
}

// TestListen_LiveSocket verifies that Listen returns ErrListenerOwned when
// another live listener already owns the socket.
func TestListen_LiveSocket(t *testing.T) {
	dir := shortXDGDir(t)
	sockPath := filepath.Join(dir, "cog-live.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen (first instance): %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, listenErr := hookipc.Listen(ctx, sockPath, func(_ []byte) {}, nil)
	if !errors.Is(listenErr, hookipc.ErrListenerOwned) {
		t.Errorf("Listen with live socket: got %v, want ErrListenerOwned", listenErr)
	}
}

// TestListen_CleanupUnlinks verifies that the cleanup function unlinks the
// socket file.
func TestListen_CleanupUnlinks(t *testing.T) {
	dir := shortXDGDir(t)
	sockPath := filepath.Join(dir, "cog-cleanup.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cleanup, err := hookipc.Listen(ctx, sockPath, func(_ []byte) {}, nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket file should exist before cleanup: %v", err)
	}

	cleanup()

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file should be removed after cleanup, got: %v", err)
	}
}

// TestListen_SocketPermissions verifies that the bound Unix socket is
// accessible only by the owner (mode 0600).
func TestListen_SocketPermissions(t *testing.T) {
	dir := shortXDGDir(t)
	sockPath := filepath.Join(dir, "cog-perms.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cleanup, err := hookipc.Listen(ctx, sockPath, func(_ []byte) {}, nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer cleanup()

	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("socket permissions = %04o, want 0600", perm)
	}
}
