package workspace_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// withConfigEnv sets XDG_CONFIG_HOME to dir for the duration of the test and
// restores the original value (or unsets it) on cleanup.
func withConfigEnv(t *testing.T, dir string) {
	t.Helper()
	orig, had := os.LookupEnv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatalf("setenv XDG_CONFIG_HOME: %v", err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("XDG_CONFIG_HOME", orig)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	})
}

// TestLoadConfig_NoFile verifies that LoadConfig returns an empty Config when
// the config file does not exist, and that SaveConfig creates the file.
func TestLoadConfig_NoFile(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	cfg, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with no file: %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("expected 0 repos, got %d", len(cfg.Repos))
	}
	if cfg.DefaultHarness != "" {
		t.Errorf("expected empty DefaultHarness, got %q", cfg.DefaultHarness)
	}

	// Saving should create the file.
	if err := workspace.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	path, err := workspace.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file not created after SaveConfig: %v", err)
	}
}

// TestLoadConfig_WithReposAndHarness verifies that a config file listing two
// repo paths and a defaultHarness is loaded correctly.
func TestLoadConfig_WithReposAndHarness(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	// Create two real directories so they are not flagged missing.
	repo1 := filepath.Join(tmp, "repo1")
	repo2 := filepath.Join(tmp, "repo2")
	for _, d := range []string{repo1, repo2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// Write the config file manually.
	cfgDir := filepath.Join(tmp, "cogitator")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	raw := map[string]interface{}{
		"repos":          []string{repo1, repo2},
		"defaultHarness": "opencode",
	}
	data, _ := json.Marshal(raw)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if len(cfg.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(cfg.Repos))
	}
	if cfg.DefaultHarness != "opencode" {
		t.Errorf("expected defaultHarness %q, got %q", "opencode", cfg.DefaultHarness)
	}
	for _, r := range cfg.Repos {
		if r.Missing {
			t.Errorf("repo %q should not be flagged missing (it exists on disk)", r.Path)
		}
	}
}

// TestLoadConfig_MissingRepoPaths verifies that a configured repo path absent
// from disk is loaded but flagged Missing, without returning an error.
func TestLoadConfig_MissingRepoPaths(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	// Use a path that does not exist on disk.
	absentPath := filepath.Join(tmp, "does-not-exist")

	cfgDir := filepath.Join(tmp, "cogitator")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	raw := map[string]interface{}{
		"repos": []string{absentPath},
	}
	data, _ := json.Marshal(raw)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with absent repo: %v", err)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(cfg.Repos))
	}
	if !cfg.Repos[0].Missing {
		t.Errorf("expected repo %q to be flagged Missing", cfg.Repos[0].Path)
	}
}

// TestLoadConfig_XDGFallback verifies that when $XDG_CONFIG_HOME is unset,
// ConfigPath falls back to ~/.config/cogitator/config.json.
func TestLoadConfig_XDGFallback(t *testing.T) {
	// Unset XDG_CONFIG_HOME for this test.
	orig, had := os.LookupEnv("XDG_CONFIG_HOME")
	if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
		t.Fatalf("unsetenv XDG_CONFIG_HOME: %v", err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("XDG_CONFIG_HOME", orig)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	})

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	expected := filepath.Join(home, ".config", "cogitator", "config.json")

	got, err := workspace.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if got != expected {
		t.Errorf("ConfigPath fallback: got %q, want %q", got, expected)
	}
}

// TestSaveConfig_RoundTrip verifies that SaveConfig followed by LoadConfig
// returns the same repos and harness.
func TestSaveConfig_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	// Create real directories so they are not flagged missing.
	repo1 := filepath.Join(tmp, "alpha")
	repo2 := filepath.Join(tmp, "beta")
	for _, d := range []string{repo1, repo2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	original := workspace.Config{
		Repos: []workspace.RepoConfig{
			{Path: repo1},
			{Path: repo2},
		},
		DefaultHarness: "opencode",
		LaunchMode:     workspace.LaunchSession,
	}

	if err := workspace.SaveConfig(original); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	loaded, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig after save: %v", err)
	}

	if len(loaded.Repos) != 2 {
		t.Fatalf("expected 2 repos after round-trip, got %d", len(loaded.Repos))
	}
	if loaded.DefaultHarness != "opencode" {
		t.Errorf("DefaultHarness: got %q, want %q", loaded.DefaultHarness, "opencode")
	}
	if loaded.LaunchMode != workspace.LaunchSession {
		t.Errorf("LaunchMode: got %q, want %q", loaded.LaunchMode, workspace.LaunchSession)
	}
	for i, r := range loaded.Repos {
		if r.Missing {
			t.Errorf("repo[%d] %q should not be Missing", i, r.Path)
		}
	}
}

// TestAddRepo_AppendsAndDedups verifies that AddRepo persists a new repo and
// reports duplicates without growing the config.
func TestAddRepo_AppendsAndDedups(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	repo := filepath.Join(tmp, "myrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	added, err := workspace.AddRepo(repo)
	if err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if !added {
		t.Fatalf("AddRepo: expected added=true on first add")
	}

	// Second add of the same path is a no-op duplicate.
	added, err = workspace.AddRepo(repo)
	if err != nil {
		t.Fatalf("AddRepo (dup): %v", err)
	}
	if added {
		t.Fatalf("AddRepo: expected added=false on duplicate")
	}

	loaded, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(loaded.Repos) != 1 {
		t.Fatalf("expected 1 repo after dedup, got %d", len(loaded.Repos))
	}
	want, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("pathnorm.Canonical: %v", err)
	}
	if loaded.Repos[0].Path != want {
		t.Errorf("persisted repo path: got %q, want %q", loaded.Repos[0].Path, want)
	}
}

// TestAddRepo_PreservesExisting verifies that AddRepo keeps already-configured
// repos when appending a new one.
func TestAddRepo_PreservesExisting(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	first := filepath.Join(tmp, "first")
	second := filepath.Join(tmp, "second")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	if _, err := workspace.AddRepo(first); err != nil {
		t.Fatalf("AddRepo first: %v", err)
	}
	if _, err := workspace.AddRepo(second); err != nil {
		t.Fatalf("AddRepo second: %v", err)
	}

	loaded, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(loaded.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(loaded.Repos))
	}
}

// TestRemoveRepo_DropsAndReportsMissing verifies that RemoveRepo untracks a
// configured repo, preserves the others, and reports a no-op when the path was
// never tracked.
func TestRemoveRepo_DropsAndReportsMissing(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	first := filepath.Join(tmp, "first")
	second := filepath.Join(tmp, "second")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		if _, err := workspace.AddRepo(d); err != nil {
			t.Fatalf("AddRepo %s: %v", d, err)
		}
	}

	removed, err := workspace.RemoveRepo(first)
	if err != nil {
		t.Fatalf("RemoveRepo: %v", err)
	}
	if !removed {
		t.Fatalf("RemoveRepo: expected removed=true for tracked repo")
	}

	loaded, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(loaded.Repos) != 1 {
		t.Fatalf("expected 1 repo after removal, got %d", len(loaded.Repos))
	}
	want, err := pathnorm.Canonical(second)
	if err != nil {
		t.Fatalf("pathnorm.Canonical: %v", err)
	}
	if loaded.Repos[0].Path != want {
		t.Errorf("surviving repo: got %q, want %q", loaded.Repos[0].Path, want)
	}

	// Removing the same path again is a no-op.
	removed, err = workspace.RemoveRepo(first)
	if err != nil {
		t.Fatalf("RemoveRepo (already gone): %v", err)
	}
	if removed {
		t.Errorf("RemoveRepo: expected removed=false for untracked path")
	}
}

func TestLoadConfigDefaultsLaunchMode(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	// No launchMode key on disk → empty (caller treats as window).
	if err := workspace.SaveConfig(workspace.Config{}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	loaded, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.LaunchMode != "" {
		t.Errorf("LaunchMode default: got %q, want empty", loaded.LaunchMode)
	}
}

func TestLoadConfigUnknownLaunchModeFallsBackToSession(t *testing.T) {
	tmp := t.TempDir()
	withConfigEnv(t, tmp)

	path, err := workspace.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"repos":[],"launchMode":"bogus"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := workspace.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.LaunchMode != workspace.LaunchSession {
		t.Errorf("LaunchMode: got %q, want %q (fallback)", loaded.LaunchMode, workspace.LaunchSession)
	}
}
