package harness_test

import (
	"testing"

	"github.com/guilhermehto/cogitator/internal/harness"
)

func TestDefaultRegistry_GetCodex(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindCodex)
	if err != nil {
		t.Fatalf("Get(%q) returned unexpected error: %v", harness.KindCodex, err)
	}

	if got := h.Kind(); got != harness.KindCodex {
		t.Errorf("Kind() = %q, want %q", got, harness.KindCodex)
	}
}

func TestCodex_Capabilities_LiveStatus(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindCodex)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindCodex, err)
	}

	caps := h.Capabilities()
	if caps.LiveStatus {
		t.Error("Capabilities().LiveStatus = true, want false")
	}
}

func TestCodex_LaunchArgv(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindCodex)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindCodex, err)
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
			want:  []string{"codex"},
		},
		{
			name:  "resume with session UUID",
			wt:    "/some/worktree",
			token: "ses-123",
			want:  []string{"codex", "resume", "ses-123"},
		},
		{
			name:  "worktree path is ignored in argv",
			wt:    "/different/path",
			token: "",
			want:  []string{"codex"},
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
