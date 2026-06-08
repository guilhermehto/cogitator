package harness_test

import (
	"testing"

	"github.com/guilhermehto/cogitator/internal/harness"
)

func TestDefaultRegistry_GetClaudeCode(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindClaudeCode)
	if err != nil {
		t.Fatalf("Get(%q) returned unexpected error: %v", harness.KindClaudeCode, err)
	}

	if got := h.Kind(); got != harness.KindClaudeCode {
		t.Errorf("Kind() = %q, want %q", got, harness.KindClaudeCode)
	}
}

func TestClaudeCode_Capabilities_LiveStatus(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindClaudeCode)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindClaudeCode, err)
	}

	caps := h.Capabilities()
	if !caps.LiveStatus {
		t.Error("Capabilities().LiveStatus = false, want true (mDNS discovery provides live status)")
	}
}

func TestClaudeCode_LaunchArgv(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindClaudeCode)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindClaudeCode, err)
	}

	tests := []struct {
		name  string
		wt    string
		token harness.ResumeToken
		want  []string
	}{
		{
			name:  "fresh launch with empty token",
			wt:    "/some/worktree",
			token: "",
			want:  []string{"claude"},
		},
		{
			name:  "resume with session UUID",
			wt:    "/some/worktree",
			token: "ses-abc123",
			want:  []string{"claude", "--resume", "ses-abc123"},
		},
		{
			name:  "worktree path is ignored in argv",
			wt:    "/different/path",
			token: "",
			want:  []string{"claude"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := h.LaunchArgv(tc.wt, tc.token)
			if !equalSlice(got, tc.want) {
				t.Errorf("LaunchArgv(%q, %q) = %v, want %v", tc.wt, tc.token, got, tc.want)
			}
		})
	}
}
