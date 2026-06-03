package state

import (
	"testing"
	"time"
)

func TestClassifyMarksOnlyBusyAndGeneratingAsActive(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		statusType string
		want       Attention
	}{
		{name: "busy", statusType: "busy", want: AttnActive},
		{name: "generating", statusType: "generating", want: AttnActive},
		{name: "idle", statusType: "idle", want: AttnInactive},
		{name: "empty", statusType: "", want: AttnInactive},
		{name: "retry", statusType: "retry", want: AttnInactive},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.statusType, false, false, time.Time{}, now)
			if got != tc.want {
				t.Fatalf("Classify(%q) = %q, want %q", tc.statusType, got, tc.want)
			}
		})
	}
}

// TestClassifyCodexStatusStrings verifies that every status string the Codex
// provider emits maps to the expected Attention without leaking opencode-only
// vocabulary ("generating") into the codex path.
func TestClassifyCodexStatusStrings(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		statusType string
		want       Attention
	}{
		// "busy" is emitted for active work (session_start, tool hooks, poll-live).
		{name: "busy→active", statusType: "busy", want: AttnActive},
		// "idle" is emitted by the stopped hook.
		{name: "idle→inactive", statusType: "idle", want: AttnInactive},
		// "" (empty) is emitted when no hook has fired and the session is not live.
		{name: "empty→inactive", statusType: "", want: AttnInactive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.statusType, false, false, time.Time{}, now)
			if got != tc.want {
				t.Fatalf("Classify(%q) = %q, want %q", tc.statusType, got, tc.want)
			}
		})
	}
}

// TestClassifyCodexPermissionAndError verifies that the hasPermission and
// lastError paths work correctly for Codex sessions (same logic as opencode,
// but exercised with the Codex-emitted status strings).
func TestClassifyCodexPermissionAndError(t *testing.T) {
	now := time.Now()

	// permission_request hook sets hasPermission=true; statusType stays "busy".
	if got := Classify("busy", true, false, time.Time{}, now); got != AttnPermissionPending {
		t.Fatalf("codex permission: got %q, want %q", got, AttnPermissionPending)
	}
	// error hook sets lastError; if lastError >= lastActivity → errored.
	errTime := now.Add(-time.Second)
	if got := Classify("busy", false, false, errTime, errTime); got != AttnErrored {
		t.Fatalf("codex error: got %q, want %q", got, AttnErrored)
	}
	// Newer activity clears the error.
	if got := Classify("busy", false, false, errTime, now); got != AttnActive {
		t.Fatalf("codex newer activity clears error: got %q, want %q", got, AttnActive)
	}
}

// TestClassifyOpencodeRegressionUnchanged asserts that the opencode status
// strings ("busy", "generating", "idle", "retry", "") still map exactly as
// before — no regression from the Codex additions.
func TestClassifyOpencodeRegressionUnchanged(t *testing.T) {
	now := time.Now()
	cases := []struct {
		statusType string
		want       Attention
	}{
		{"busy", AttnActive},
		{"generating", AttnActive},
		{"idle", AttnInactive},
		{"retry", AttnInactive},
		{"", AttnInactive},
	}
	for _, tc := range cases {
		got := Classify(tc.statusType, false, false, time.Time{}, now)
		if got != tc.want {
			t.Fatalf("opencode regression: Classify(%q) = %q, want %q", tc.statusType, got, tc.want)
		}
	}
}

func TestClassifyKeepsPermissionAndErrorPrecedence(t *testing.T) {
	now := time.Now()

	if got := Classify("busy", true, false, time.Time{}, now); got != AttnPermissionPending {
		t.Fatalf("permission precedence: got %q, want %q", got, AttnPermissionPending)
	}
	if got := Classify("busy", true, true, time.Time{}, now); got != AttnPermissionPending {
		t.Fatalf("permission should beat question: got %q, want %q", got, AttnPermissionPending)
	}
	if got := Classify("busy", false, true, time.Time{}, now); got != AttnQuestionPending {
		t.Fatalf("question precedence: got %q, want %q", got, AttnQuestionPending)
	}

	errorTime := now.Add(-time.Second)
	if got := Classify("busy", false, false, errorTime, errorTime); got != AttnErrored {
		t.Fatalf("error precedence: got %q, want %q", got, AttnErrored)
	}

	if got := Classify("busy", false, false, errorTime, now); got != AttnActive {
		t.Fatalf("newer activity should clear error: got %q, want %q", got, AttnActive)
	}
}
