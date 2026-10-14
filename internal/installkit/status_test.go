package installkit

import "testing"

func TestStatusLocationNamesLineOnlyWhenKnown(t *testing.T) {
	cases := map[string]struct {
		status Status
		want   string
	}{
		"with line":    {Status{Path: "/home/u/.tmux.conf", Line: 12}, "/home/u/.tmux.conf:12"},
		"without line": {Status{Path: "/home/u/.tmux.conf"}, "/home/u/.tmux.conf"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.status.Location(); got != tc.want {
				t.Fatalf("Location() = %q, want %q", got, tc.want)
			}
		})
	}
}
