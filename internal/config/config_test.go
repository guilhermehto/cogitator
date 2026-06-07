package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCodexEnabled_ExplicitOverride verifies that CODEX_ENABLED wins over
// auto-detection regardless of whether the Codex home directory exists.
func TestCodexEnabled_ExplicitOverride(t *testing.T) {
	// A real directory that exists — used to confirm explicit "false" still wins.
	existingDir := t.TempDir()

	// A path that does not exist — used to confirm explicit "true" still wins.
	absentDir := filepath.Join(t.TempDir(), "no-such-subdir")

	tests := []struct {
		name      string
		envVal    string
		codexHome string // set as CODEX_HOME so auto-detect uses this path
		wantOn    bool
	}{
		// Explicit ON overrides absent directory.
		{name: "explicit true, absent dir → ON", envVal: "true", codexHome: absentDir, wantOn: true},
		{name: "explicit 1, absent dir → ON", envVal: "1", codexHome: absentDir, wantOn: true},
		{name: "explicit TRUE uppercase, absent dir → ON", envVal: "TRUE", codexHome: absentDir, wantOn: true},
		// Explicit OFF overrides existing directory.
		{name: "explicit false, existing dir → OFF", envVal: "false", codexHome: existingDir, wantOn: false},
		{name: "explicit 0, existing dir → OFF", envVal: "0", codexHome: existingDir, wantOn: false},
		{name: "explicit FALSE uppercase, existing dir → OFF", envVal: "FALSE", codexHome: existingDir, wantOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEX_ENABLED", tc.envVal)
			t.Setenv("CODEX_HOME", tc.codexHome)
			cfg := Default()
			if cfg.CodexEnabled != tc.wantOn {
				t.Errorf("CodexEnabled = %v, want %v (CODEX_ENABLED=%q, CODEX_HOME=%q)",
					cfg.CodexEnabled, tc.wantOn, tc.envVal, tc.codexHome)
			}
		})
	}
}

// TestCodexEnabled_AutoDetect verifies that when CODEX_ENABLED is unset (or
// unrecognized), CodexEnabled is derived from whether the Codex home directory
// exists on disk.
func TestCodexEnabled_AutoDetect(t *testing.T) {
	existingDir := t.TempDir()
	absentDir := filepath.Join(t.TempDir(), "no-such-subdir")

	tests := []struct {
		name      string
		envVal    string // "" means unset; "yes" is an unrecognized value
		codexHome string
		wantOn    bool
	}{
		{name: "unset, existing dir → ON", envVal: "", codexHome: existingDir, wantOn: true},
		{name: "unset, absent dir → OFF", envVal: "", codexHome: absentDir, wantOn: false},
		{name: "unrecognized value, existing dir → ON", envVal: "yes", codexHome: existingDir, wantOn: true},
		{name: "unrecognized value, absent dir → OFF", envVal: "yes", codexHome: absentDir, wantOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEX_ENABLED", tc.envVal)
			t.Setenv("CODEX_HOME", tc.codexHome)
			cfg := Default()
			if cfg.CodexEnabled != tc.wantOn {
				t.Errorf("CodexEnabled = %v, want %v (CODEX_ENABLED=%q, CODEX_HOME=%q)",
					cfg.CodexEnabled, tc.wantOn, tc.envVal, tc.codexHome)
			}
		})
	}
}

// TestCodexEnabled_AutoDetect_FileNotDir verifies that a path pointing to a
// regular file (not a directory) is treated as absent.
func TestCodexEnabled_AutoDetect_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "codex-file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_ENABLED", "")
	t.Setenv("CODEX_HOME", filePath)
	cfg := Default()
	if cfg.CodexEnabled {
		t.Errorf("CodexEnabled = true, want false when CODEX_HOME points to a file, not a directory")
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

// TestClaudeEnabled_ExplicitOverride verifies that CLAUDE_ENABLED wins over
// auto-detection regardless of whether the Claude projects directory exists.
func TestClaudeEnabled_ExplicitOverride(t *testing.T) {
	// A real projects directory that exists — used to confirm explicit "false" still wins.
	existingBase := t.TempDir()
	if err := os.Mkdir(filepath.Join(existingBase, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}

	// A path whose projects subdir does not exist — used to confirm explicit "true" still wins.
	absentBase := filepath.Join(t.TempDir(), "no-such-subdir")

	tests := []struct {
		name       string
		envVal     string
		claudeHome string // set as CLAUDE_HOME so auto-detect uses this path
		wantOn     bool
	}{
		// Explicit ON overrides absent directory.
		{name: "explicit true, absent dir → ON", envVal: "true", claudeHome: absentBase, wantOn: true},
		{name: "explicit 1, absent dir → ON", envVal: "1", claudeHome: absentBase, wantOn: true},
		{name: "explicit TRUE uppercase, absent dir → ON", envVal: "TRUE", claudeHome: absentBase, wantOn: true},
		// Explicit OFF overrides existing directory.
		{name: "explicit false, existing dir → OFF", envVal: "false", claudeHome: existingBase, wantOn: false},
		{name: "explicit 0, existing dir → OFF", envVal: "0", claudeHome: existingBase, wantOn: false},
		{name: "explicit FALSE uppercase, existing dir → OFF", envVal: "FALSE", claudeHome: existingBase, wantOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_ENABLED", tc.envVal)
			t.Setenv("CLAUDE_HOME", tc.claudeHome)
			cfg := Default()
			if cfg.ClaudeCodeEnabled != tc.wantOn {
				t.Errorf("ClaudeCodeEnabled = %v, want %v (CLAUDE_ENABLED=%q, CLAUDE_HOME=%q)",
					cfg.ClaudeCodeEnabled, tc.wantOn, tc.envVal, tc.claudeHome)
			}
		})
	}
}

// TestClaudeEnabled_AutoDetect verifies that when CLAUDE_ENABLED is unset (or
// unrecognized), ClaudeCodeEnabled is derived from whether the Claude projects
// directory exists on disk.
func TestClaudeEnabled_AutoDetect(t *testing.T) {
	existingBase := t.TempDir()
	if err := os.Mkdir(filepath.Join(existingBase, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	absentBase := filepath.Join(t.TempDir(), "no-such-subdir")

	tests := []struct {
		name       string
		envVal     string // "" means unset; "yes" is an unrecognized value
		claudeHome string
		wantOn     bool
	}{
		{name: "unset, existing projects dir → ON", envVal: "", claudeHome: existingBase, wantOn: true},
		{name: "unset, absent dir → OFF", envVal: "", claudeHome: absentBase, wantOn: false},
		{name: "unrecognized value, existing projects dir → ON", envVal: "yes", claudeHome: existingBase, wantOn: true},
		{name: "unrecognized value, absent dir → OFF", envVal: "yes", claudeHome: absentBase, wantOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_ENABLED", tc.envVal)
			t.Setenv("CLAUDE_HOME", tc.claudeHome)
			cfg := Default()
			if cfg.ClaudeCodeEnabled != tc.wantOn {
				t.Errorf("ClaudeCodeEnabled = %v, want %v (CLAUDE_ENABLED=%q, CLAUDE_HOME=%q)",
					cfg.ClaudeCodeEnabled, tc.wantOn, tc.envVal, tc.claudeHome)
			}
		})
	}
}

// TestClaudeEnabled_AutoDetect_FileNotDir verifies that a path pointing to a
// regular file (not a directory) is treated as absent.
func TestClaudeEnabled_AutoDetect_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "projects")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_ENABLED", "")
	t.Setenv("CLAUDE_HOME", dir)
	cfg := Default()
	if cfg.ClaudeCodeEnabled {
		t.Errorf("ClaudeCodeEnabled = true, want false when CLAUDE_HOME/projects points to a file, not a directory")
	}
}

// TestDefault_ClaudeCodeHome verifies that CLAUDE_HOME is read from the environment.
func TestDefault_ClaudeCodeHome(t *testing.T) {
	t.Setenv("CLAUDE_HOME", "/custom/claude/home")
	cfg := Default()
	if cfg.ClaudeCodeHome != "/custom/claude/home" {
		t.Errorf("ClaudeCodeHome = %q, want %q", cfg.ClaudeCodeHome, "/custom/claude/home")
	}
}

// TestDefault_ClaudeCodeHome_Unset verifies that an unset CLAUDE_HOME yields
// an empty string (the provider resolves ~/.claude itself).
func TestDefault_ClaudeCodeHome_Unset(t *testing.T) {
	t.Setenv("CLAUDE_HOME", "")
	cfg := Default()
	if cfg.ClaudeCodeHome != "" {
		t.Errorf("ClaudeCodeHome = %q, want empty string when CLAUDE_HOME unset", cfg.ClaudeCodeHome)
	}
}
