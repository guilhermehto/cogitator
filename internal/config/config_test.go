package config

import (
	"testing"
)

// TestDefault_CodexEnabled verifies that CODEX_ENABLED is read from the
// environment: "true" and "1" enable it; anything else (including unset)
// leaves it disabled.
func TestDefault_CodexEnabled(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		wantOn bool
	}{
		{name: "unset → disabled", envVal: "", wantOn: false},
		{name: "true → enabled", envVal: "true", wantOn: true},
		{name: "1 → enabled", envVal: "1", wantOn: true},
		{name: "TRUE uppercase → enabled", envVal: "TRUE", wantOn: true},
		{name: "True mixed → enabled", envVal: "True", wantOn: true},
		{name: "false → disabled", envVal: "false", wantOn: false},
		{name: "0 → disabled", envVal: "0", wantOn: false},
		{name: "yes → disabled", envVal: "yes", wantOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEX_ENABLED", tc.envVal)
			cfg := Default()
			if cfg.CodexEnabled != tc.wantOn {
				t.Errorf("CodexEnabled = %v, want %v (CODEX_ENABLED=%q)", cfg.CodexEnabled, tc.wantOn, tc.envVal)
			}
		})
	}
}

// TestDefault_CodexHome verifies that CODEX_HOME is read from the environment.
func TestDefault_CodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex/home")
	cfg := Default()
	if cfg.CodexHome != "/custom/codex/home" {
		t.Errorf("CodexHome = %q, want %q", cfg.CodexHome, "/custom/codex/home")
	}
}

// TestDefault_CodexHome_Unset verifies that an unset CODEX_HOME yields an
// empty string (the provider resolves ~/.codex itself).
func TestDefault_CodexHome_Unset(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	cfg := Default()
	if cfg.CodexHome != "" {
		t.Errorf("CodexHome = %q, want empty string when CODEX_HOME unset", cfg.CodexHome)
	}
}
