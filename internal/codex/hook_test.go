package codex_test

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/codex"
)

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

// TestFrameRoundTrip verifies that WriteFrame + ReadFrame round-trips arbitrary
// payloads without corruption.
func TestFrameRoundTrip(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"hook_event_name":"SessionStart","session_id":"abc"}`),
		[]byte(`{}`),
		[]byte(strings.Repeat("x", 65536)), // large payload
		[]byte{},                            // empty payload
	}

	for _, payload := range cases {
		var buf bytes.Buffer
		if err := codex.WriteFrame(&buf, payload); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
		got, err := codex.ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("round-trip mismatch: got %d bytes, want %d bytes", len(got), len(payload))
		}
	}
}

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

	// Accept one connection in the background, read the frame, and signal done.
	received := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, err := codex.ReadFrame(conn)
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

	if err := codex.WriteFrame(conn, payload); err != nil {
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

// TestSendHook_NoListener verifies that SendHook returns a non-nil error
// quickly when no listener is present — it must not hang.
func TestSendHook_NoListener(t *testing.T) {
	// Point the socket path at a short temp dir where nothing is listening.
	t.Setenv("XDG_RUNTIME_DIR", shortXDGDir(t))

	start := time.Now()
	err := codex.SendHook(context.Background(), strings.NewReader(`{"hook_event_name":"Stop"}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Error("SendHook: expected non-nil error when no listener present, got nil")
	}
	// Must return well within the combined dial+write timeout (4s). Allow 3.5s
	// to avoid flakiness on slow CI while still catching hangs.
	if elapsed > 3500*time.Millisecond {
		t.Errorf("SendHook took %v, want < 3.5s (must not block Codex)", elapsed)
	}
}

// TestSendHook_DeliverPayload verifies that SendHook delivers the raw stdin
// bytes as a framed message to a listening socket.
func TestSendHook_DeliverPayload(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortXDGDir(t))

	sockPath := codex.HookSocketPath()
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
		data, err := codex.ReadFrame(conn)
		if err != nil {
			received <- nil
			return
		}
		received <- data
	}()

	if err := codex.SendHook(context.Background(), strings.NewReader(payload)); err != nil {
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
