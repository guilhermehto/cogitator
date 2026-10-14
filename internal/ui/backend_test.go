package ui

import "testing"

func TestBackendForPrefersZellij(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"zellij only", map[string]string{"ZELLIJ": "0"}, "zellij"},
		{"zellij nested in tmux", map[string]string{"ZELLIJ": "0", "TMUX": "/tmp/tmux-501/default,1,0"}, "zellij"},
		{"tmux only", map[string]string{"TMUX": "/tmp/tmux-501/default,1,0"}, "tmux"},
		{"neither", map[string]string{}, "tmux"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string { return tc.env[key] }

			if got := backendFor(getenv).Name(); got != tc.want {
				t.Fatalf("backend = %q, want %q", got, tc.want)
			}
		})
	}
}
