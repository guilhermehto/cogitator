package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCodexEnabled verifies that CodexEnabled is derived purely from whether
// the Codex home directory exists on disk (auto-detection only).
func TestCodexEnabled(t *testing.T) {
	existingDir := t.TempDir()
	absentDir := filepath.Join(t.TempDir(), "no-such-subdir")

	tests := []struct {
		name      string
		codexHome string
		wantOn    bool
	}{
		{name: "existing dir → ON", codexHome: existingDir, wantOn: true},
		{name: "absent dir → OFF", codexHome: absentDir, wantOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEX_HOME", tc.codexHome)
			cfg := Default()
			if cfg.CodexEnabled != tc.wantOn {
				t.Errorf("CodexEnabled = %v, want %v (CODEX_HOME=%q)",
					cfg.CodexEnabled, tc.wantOn, tc.codexHome)
			}
		})
	}
}

// TestCodexEnabled_FileNotDir verifies that a path pointing to a regular file
// (not a directory) is treated as absent.
func TestCodexEnabled_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "codex-file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

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

// TestClaudeEnabled verifies that ClaudeCodeEnabled is derived purely from
// whether the Claude projects directory exists on disk (auto-detection only).
func TestClaudeEnabled(t *testing.T) {
	existingBase := t.TempDir()
	if err := os.Mkdir(filepath.Join(existingBase, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	absentBase := filepath.Join(t.TempDir(), "no-such-subdir")

	tests := []struct {
		name       string
		claudeHome string
		wantOn     bool
	}{
		{name: "existing projects dir → ON", claudeHome: existingBase, wantOn: true},
		{name: "absent dir → OFF", claudeHome: absentBase, wantOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_HOME", tc.claudeHome)
			cfg := Default()
			if cfg.ClaudeCodeEnabled != tc.wantOn {
				t.Errorf("ClaudeCodeEnabled = %v, want %v (CLAUDE_HOME=%q)",
					cfg.ClaudeCodeEnabled, tc.wantOn, tc.claudeHome)
			}
		})
	}
}

// TestClaudeEnabled_FileNotDir verifies that a path pointing to a regular file
// (not a directory) is treated as absent.
func TestClaudeEnabled_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "projects")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

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
