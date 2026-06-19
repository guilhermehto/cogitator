package omp_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/omp"
)

func TestHookSocketPath_Consistency(t *testing.T) {
	first := omp.HookSocketPath()
	second := omp.HookSocketPath()
	if first != second {
		t.Errorf("HookSocketPath() not stable: %q != %q", first, second)
	}
	if !strings.HasSuffix(first, "cogitator-omp-hook.sock") {
		t.Errorf("HookSocketPath() = %q, want suffix cogitator-omp-hook.sock", first)
	}
}

func TestParseHookEvent_FieldExtraction(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantEvent string
		wantID    string
		wantCWD   string
		wantTool  string
		wantErr   bool
	}{
		{
			name:      "snake_case turn_start",
			raw:       `{"hook_event_name":"turn_start","session_id":"s1","cwd":"/tmp/wt"}`,
			wantEvent: "turn_start", wantID: "s1", wantCWD: "/tmp/wt",
		},
		{
			name:      "PascalCase alias",
			raw:       `{"hook_event_name":"SessionShutdown","session_id":"s2"}`,
			wantEvent: "session_shutdown", wantID: "s2",
		},
		{
			name:      "tool_call carries tool_name",
			raw:       `{"hook_event_name":"tool_call","session_id":"s3","tool_name":"ask"}`,
			wantEvent: "tool_call", wantID: "s3", wantTool: "ask",
		},
		{
			name:      "is_error bool",
			raw:       `{"hook_event_name":"tool_result","session_id":"s4","tool_name":"bash","is_error":true}`,
			wantEvent: "tool_result", wantID: "s4", wantTool: "bash", wantErr: true,
		},
		{
			name:      "error string sets IsError",
			raw:       `{"hook_event_name":"tool_result","session_id":"s5","error":"boom"}`,
			wantEvent: "tool_result", wantID: "s5", wantErr: true,
		},
		{
			name:      "unknown event passes through",
			raw:       `{"hook_event_name":"weird_event","session_id":"s6"}`,
			wantEvent: "weird_event", wantID: "s6",
		},
		{
			name:      "fallback id/cwd keys",
			raw:       `{"event":"turn_end","sessionId":"s7","directory":"/tmp/d"}`,
			wantEvent: "turn_end", wantID: "s7", wantCWD: "/tmp/d",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := omp.ParseHookEvent([]byte(tc.raw))
			if err != nil {
				t.Fatalf("ParseHookEvent: %v", err)
			}
			if ev.EventName != tc.wantEvent {
				t.Errorf("EventName = %q, want %q", ev.EventName, tc.wantEvent)
			}
			if ev.SessionID != tc.wantID {
				t.Errorf("SessionID = %q, want %q", ev.SessionID, tc.wantID)
			}
			if ev.CWD != tc.wantCWD {
				t.Errorf("CWD = %q, want %q", ev.CWD, tc.wantCWD)
			}
			if ev.ToolName != tc.wantTool {
				t.Errorf("ToolName = %q, want %q", ev.ToolName, tc.wantTool)
			}
			if ev.IsError != tc.wantErr {
				t.Errorf("IsError = %v, want %v", ev.IsError, tc.wantErr)
			}
		})
	}
}

func TestParseHookEvent_MalformedJSON(t *testing.T) {
	if _, err := omp.ParseHookEvent([]byte(`{not json`)); err == nil {
		t.Error("ParseHookEvent(malformed) = nil error, want error")
	}
}

func TestWriteReadFrame_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"hook_event_name":"turn_start","session_id":"s1"}`)
	if err := omp.WriteFrame(&buf, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	got, err := omp.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("ReadFrame = %q, want %q", got, payload)
	}
}

func TestSendHook_NoListenerIsUnavailable(t *testing.T) {
	// Point the socket at a short tmp dir with no listener; SendHook must
	// report ErrListenerUnavailable rather than hang or error fatally.
	dir, err := os.MkdirTemp("", "cog-omp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)

	err = omp.SendHook(context.Background(), strings.NewReader(`{"hook_event_name":"turn_start"}`))
	if err == nil {
		t.Fatal("SendHook with no listener = nil, want ErrListenerUnavailable")
	}
	if !strings.Contains(err.Error(), omp.ErrListenerUnavailable.Error()) {
		t.Errorf("SendHook err = %v, want wrapping ErrListenerUnavailable", err)
	}
}
