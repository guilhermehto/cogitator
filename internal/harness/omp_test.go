package harness_test

import (
	"testing"

	"github.com/guilhermehto/cogitator/internal/harness"
)

func TestDefaultRegistry_GetOMP(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOMP)
	if err != nil {
		t.Fatalf("Get(%q) returned unexpected error: %v", harness.KindOMP, err)
	}

	if got := h.Kind(); got != harness.KindOMP {
		t.Errorf("Kind() = %q, want %q", got, harness.KindOMP)
	}
}

func TestOMP_Capabilities_LiveStatus(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOMP)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindOMP, err)
	}

	caps := h.Capabilities()
	if !caps.LiveStatus {
		t.Error("Capabilities().LiveStatus = false, want true (hook bridge provides live attention)")
	}
}

func TestOMP_LaunchArgv(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOMP)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindOMP, err)
	}

	tests := []struct {
		name  string
		wt    string
		token harness.ResumeToken
		want  []string
	}{
		{
			name:  "empty token continues the per-directory session",
			wt:    "/some/worktree",
			token: "",
			want:  []string{"omp", "--continue"},
		},
		{
			name:  "resume with session id",
			wt:    "/some/worktree",
			token: "019edf7d",
			want:  []string{"omp", "--resume", "019edf7d"},
		},
		{
			name:  "worktree path is ignored in argv",
			wt:    "/different/path",
			token: "",
			want:  []string{"omp", "--continue"},
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
