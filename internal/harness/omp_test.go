package harness_test

import (
	"testing"

	"github.com/guilhermehto/cogitator/internal/harness"
)

func TestDefaultRegistry_GetOmp(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOmp)
	if err != nil {
		t.Fatalf("Get(%q) returned unexpected error: %v", harness.KindOmp, err)
	}
	if got := h.Kind(); got != harness.KindOmp {
		t.Errorf("Kind() = %q, want %q", got, harness.KindOmp)
	}
}

func TestOmp_Capabilities_LiveStatus(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOmp)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindOmp, err)
	}
	if !h.Capabilities().LiveStatus {
		t.Error("Capabilities().LiveStatus = false, want true (hook listener provides live attention)")
	}
}

func TestOmp_LaunchArgv(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindOmp)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindOmp, err)
	}

	tests := []struct {
		name  string
		wt    string
		token harness.ResumeToken
		want  []string
	}{
		{name: "empty token continues most-recent session", wt: "/some/worktree", token: "", want: []string{"omp", "--continue"}},
		{name: "resume with session id", wt: "/some/worktree", token: "1f9d2a6b", want: []string{"omp", "--resume", "1f9d2a6b"}},
		{name: "worktree path is ignored in argv", wt: "/different/path", token: "", want: []string{"omp", "--continue"}},
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
