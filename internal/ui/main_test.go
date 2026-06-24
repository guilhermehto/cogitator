package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates config and roster reads from the developer's real
// ~/.config and ~/.local/state so the ui tests are deterministic regardless of
// any locally-configured default harness, repos, or roster. Individual tests
// may still override these with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cogitator-ui-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
