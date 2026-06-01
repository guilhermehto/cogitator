package harness_test

import (
	"testing"

	"github.com/guilhermehto/cogitator/internal/harness"
)

func TestDefaultRegistry_GetOpenCode(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOpenCode)
	if err != nil {
		t.Fatalf("Get(%q) returned unexpected error: %v", harness.KindOpenCode, err)
	}

	if got := h.Kind(); got != harness.KindOpenCode {
		t.Errorf("Kind() = %q, want %q", got, harness.KindOpenCode)
	}
}

func TestOpenCode_Capabilities_LiveStatus(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOpenCode)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindOpenCode, err)
	}

	caps := h.Capabilities()
	if !caps.LiveStatus {
		t.Error("Capabilities().LiveStatus = false, want true")
	}
}

func TestOpenCode_LaunchArgv_noToken(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOpenCode)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindOpenCode, err)
	}

	const wt = "/wt"
	got := h.LaunchArgv(wt, "")
	want := []string{"opencode", "--mdns", wt}

	if !equalSlice(got, want) {
		t.Errorf("LaunchArgv(%q, %q) = %v, want %v", wt, "", got, want)
	}
}

func TestOpenCode_LaunchArgv_withToken(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOpenCode)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindOpenCode, err)
	}

	const wt = "/wt"
	const token = "sess-abc123"
	got := h.LaunchArgv(wt, token)
	want := []string{"opencode", "--mdns", "--session", token, wt}

	if !equalSlice(got, want) {
		t.Errorf("LaunchArgv(%q, %q) = %v, want %v", wt, token, got, want)
	}
}

func TestDefaultRegistry_Get_unknownKind(t *testing.T) {
	_, err := harness.DefaultRegistry.Get("nope")
	if err == nil {
		t.Error("Get(\"nope\") returned nil error, want non-nil")
	}
}

// equalSlice reports whether a and b contain the same elements in the same order.
func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
