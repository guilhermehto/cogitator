package codex_test

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
		[]byte{},                           // empty payload
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

// ── Step-10 tests: Listen, ParseHookEvent, stale-vs-live socket ──────────────

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
	cleanup, err := codex.Listen(ctx, sockPath, func(raw []byte) {
		received <- raw
	}, nil)
	if err != nil {
		t.Fatalf("Listen on stale socket: %v", err)
	}
	defer cleanup()

	// Send a frame to confirm the listener is working.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial after stale removal: %v", err)
	}
	payload := []byte(`{"hook_event_name":"SessionStart","session_id":"s1"}`)
	if err := codex.WriteFrame(conn, payload); err != nil {
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

	// Start a live listener (the "first instance").
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen (first instance): %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, listenErr := codex.Listen(ctx, sockPath, func(_ []byte) {}, nil)
	if !errors.Is(listenErr, codex.ErrListenerOwned) {
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

	cleanup, err := codex.Listen(ctx, sockPath, func(_ []byte) {}, nil)
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

// TestParseHookEvent_EventMapping verifies that the defensive parser maps
// known event names (both PascalCase and snake_case) to their canonical form
// and extracts session_id and cwd from multiple candidate field names.
func TestParseHookEvent_EventMapping(t *testing.T) {
	tests := []struct {
		name          string
		json          string
		wantEvent     string
		wantSessionID string
		wantCWD       string
		wantIsError   bool
	}{
		{
			name:          "SessionStart PascalCase",
			json:          `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/tmp/a"}`,
			wantEvent:     "session_start",
			wantSessionID: "s1",
			wantCWD:       "/tmp/a",
		},
		{
			name:          "session_start snake_case",
			json:          `{"hook_event_name":"session_start","session_id":"s2","cwd":"/tmp/b"}`,
			wantEvent:     "session_start",
			wantSessionID: "s2",
			wantCWD:       "/tmp/b",
		},
		{
			name:          "Stop → stopped",
			json:          `{"hook_event_name":"Stop","session_id":"s3"}`,
			wantEvent:     "stopped",
			wantSessionID: "s3",
		},
		{
			name:          "stopped snake_case",
			json:          `{"hook_event_name":"stopped","session_id":"s4"}`,
			wantEvent:     "stopped",
			wantSessionID: "s4",
		},
		{
			name:          "PermissionRequest PascalCase",
			json:          `{"hook_event_name":"PermissionRequest","session_id":"s5","cwd":"/tmp/c"}`,
			wantEvent:     "permission_request",
			wantSessionID: "s5",
			wantCWD:       "/tmp/c",
		},
		{
			name:          "permission_request snake_case",
			json:          `{"hook_event_name":"permission_request","session_id":"s6"}`,
			wantEvent:     "permission_request",
			wantSessionID: "s6",
		},
		{
			name:          "event field fallback",
			json:          `{"event":"UserPromptSubmit","session_id":"s7"}`,
			wantEvent:     "user_prompt_submit",
			wantSessionID: "s7",
		},
		{
			name:          "type field fallback",
			json:          `{"type":"PreToolUse","session_id":"s8"}`,
			wantEvent:     "pre_tool_use",
			wantSessionID: "s8",
		},
		{
			name:          "sessionId camelCase fallback",
			json:          `{"hook_event_name":"PostToolUse","sessionId":"s9"}`,
			wantEvent:     "post_tool_use",
			wantSessionID: "s9",
		},
		{
			name:          "id fallback for session id",
			json:          `{"hook_event_name":"Notification","id":"s10"}`,
			wantEvent:     "notification",
			wantSessionID: "s10",
		},
		{
			name:          "directory fallback for cwd",
			json:          `{"hook_event_name":"SessionStart","session_id":"s11","directory":"/tmp/d"}`,
			wantEvent:     "session_start",
			wantSessionID: "s11",
			wantCWD:       "/tmp/d",
		},
		{
			name:          "error indicator",
			json:          `{"hook_event_name":"SessionStart","session_id":"s12","error":"something went wrong"}`,
			wantEvent:     "session_start",
			wantSessionID: "s12",
			wantIsError:   true,
		},
		{
			name:          "unknown event name passes through",
			json:          `{"hook_event_name":"SomeFutureEvent","session_id":"s13"}`,
			wantEvent:     "SomeFutureEvent",
			wantSessionID: "s13",
		},
		{
			name:      "empty JSON is graceful",
			json:      `{}`,
			wantEvent: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := codex.ParseHookEvent([]byte(tc.json))
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
			if ev.IsError != tc.wantIsError {
				t.Errorf("IsError = %v, want %v", ev.IsError, tc.wantIsError)
			}
		})
	}
}

// TestParseHookEvent_MalformedJSON verifies that malformed JSON returns an error.
func TestParseHookEvent_MalformedJSON(t *testing.T) {
	_, err := codex.ParseHookEvent([]byte(`not json`))
	if err == nil {
		t.Error("ParseHookEvent(malformed): expected error, got nil")
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

// TestReadFrame_OversizedLength verifies that ReadFrame returns a clean error
// when the length prefix exceeds maxFrameSize (1 MiB) without allocating a
// huge buffer or panicking.
func TestReadFrame_OversizedLength(t *testing.T) {
	// Craft a frame header declaring 0xFFFFFFFF bytes (~4 GiB).
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 0xFFFFFFFF)

	// Provide only the 4-byte header; ReadFrame must reject it before reading
	// any payload bytes.
	r := bytes.NewReader(hdr[:])
	_, err := codex.ReadFrame(r)
	if err == nil {
		t.Fatal("ReadFrame with oversized length: expected error, got nil")
	}
	// The error must mention the size or "maximum" so it is diagnosable.
	if !strings.Contains(err.Error(), "frame size") && !strings.Contains(err.Error(), "maximum") {
		t.Errorf("ReadFrame oversized error = %q; want it to mention frame size or maximum", err.Error())
	}
}

// TestListen_SocketPermissions verifies that the bound Unix socket is
// accessible only by the owner (mode 0600).
func TestListen_SocketPermissions(t *testing.T) {
	dir := shortXDGDir(t)
	sockPath := filepath.Join(dir, "cog-perms.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cleanup, err := codex.Listen(ctx, sockPath, func(_ []byte) {}, nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer cleanup()

	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	// Mask to permission bits only (strip file-type bits).
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("socket permissions = %04o, want 0600", perm)
	}
}
