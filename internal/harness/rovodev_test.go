package harness_test

import (
	"testing"

	"github.com/guilhermehto/cogitator/internal/harness"
)

func TestDefaultRegistry_GetRovodev(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindRovodev)
	if err != nil {
		t.Fatalf("Get(%q) returned unexpected error: %v", harness.KindRovodev, err)
	}
	if got := h.Kind(); got != harness.KindRovodev {
		t.Errorf("Kind() = %q, want %q", got, harness.KindRovodev)
	}
}

func TestRovodev_Capabilities_LiveStatus(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindRovodev)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindRovodev, err)
	}
	if !h.Capabilities().LiveStatus {
		t.Error("Capabilities().LiveStatus = false, want true (polled monitor reports live sessions)")
	}
}

func TestRovodev_LaunchArgv(t *testing.T) {
	h, err := harness.DefaultRegistry.Get(harness.KindRovodev)
	if err != nil {
		t.Fatalf("Get(%q): %v", harness.KindRovodev, err)
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
			want:  []string{"acli", "rovodev", "run"},
		},
		{
			name:  "resume with session UUID",
			wt:    "/some/worktree",
			token: "3291a3ef-09a0-494e-b2d6-a53292702724",
			want:  []string{"acli", "rovodev", "run", "--restore", "3291a3ef-09a0-494e-b2d6-a53292702724"},
		},
		{
			name:  "worktree path is ignored in argv",
			wt:    "/different/path",
			token: "",
			want:  []string{"acli", "rovodev", "run"},
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
