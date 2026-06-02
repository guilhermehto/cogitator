// Package workspace manages durable user configuration and the session roster
// for cogitator. Only facts that survive a reboot are persisted here: the list
// of configured repo roots, the preferred harness, and (in roster.go) the
// last-known state of each worktree.
//
// Config is stored as JSON under $XDG_CONFIG_HOME/cogitator/config.json,
// falling back to ~/.config/cogitator/config.json when $XDG_CONFIG_HOME is
// unset or empty.
//
// No import of bubbletea or internal/ui is permitted in this package.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// RepoConfig holds the user-supplied configuration for a single repository
// root. Missing is set by LoadConfig when the path is absent from disk at
// load time; it is not persisted.
type RepoConfig struct {
	// Path is the canonical absolute path to the repository root.
	Path string `json:"path"`
	// Missing is true when Path was not found on disk at load time.
	// The UI should render the repo but disable actions that require the
	// directory to exist (e.g. creating a new worktree).
	Missing bool `json:"-"`
}

// LaunchMode selects how cogitator opens a worktree in tmux: as a new window
// in the current session ("window", the default) or as a brand-new tmux
// session ("session").
type LaunchMode string

const (
	// LaunchWindow opens worktrees as new tmux windows (the default).
	LaunchWindow LaunchMode = "window"
	// LaunchSession opens worktrees as new tmux sessions.
	LaunchSession LaunchMode = "session"
)

// Config is the top-level user configuration for cogitator.
type Config struct {
	// Repos is the ordered list of repository roots the user has configured.
	Repos []RepoConfig `json:"repos"`
	// DefaultHarness is the harness kind used when launching a new worktree
	// (e.g. "opencode"). Empty means no default is set.
	DefaultHarness string `json:"defaultHarness,omitempty"`
	// LaunchMode selects whether worktrees open as a new tmux window or a new
	// tmux session. Empty defaults to LaunchWindow.
	LaunchMode LaunchMode `json:"launchMode,omitempty"`
}

// configFile is the on-disk JSON representation. It mirrors Config but uses
// raw string slices so the file stays human-editable without the Missing field.
type configFile struct {
	Repos          []string   `json:"repos"`
	DefaultHarness string     `json:"defaultHarness,omitempty"`
	LaunchMode     LaunchMode `json:"launchMode,omitempty"`
}

// configDir returns the directory that holds cogitator's config file.
// It honours $XDG_CONFIG_HOME and falls back to ~/.config/cogitator/.
func configDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cogitator"), nil
}

// normalizeLaunchMode coerces an arbitrary on-disk value to a known LaunchMode.
// Empty stays empty (callers treat empty as LaunchWindow); LaunchSession is
// preserved; any other value falls back to LaunchWindow so a typo can never
// produce an undefined launch path.
func normalizeLaunchMode(m LaunchMode) LaunchMode {
	switch m {
	case LaunchSession:
		return LaunchSession
	case "", LaunchWindow:
		return m
	default:
		return LaunchWindow
	}
}

// ConfigPath returns the absolute path to the config file.
func ConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// LoadConfig reads the config file and returns the parsed Config.
//
// If the file does not exist, LoadConfig returns an empty Config (no error).
// The caller should call SaveConfig to persist any changes, which will create
// the file and its parent directory on first write.
//
// Each repo path is passed through pathnorm.Canonical. If a repo path is
// absent from disk, RepoConfig.Missing is set to true so the UI can render
// the repo without crashing.
func LoadConfig() (Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No config yet — return an empty config. The file will be created
			// on the first SaveConfig call.
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var raw configFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg := Config{
		DefaultHarness: raw.DefaultHarness,
		LaunchMode:     normalizeLaunchMode(raw.LaunchMode),
		Repos:          make([]RepoConfig, 0, len(raw.Repos)),
	}

	for _, rawPath := range raw.Repos {
		canonical, err := pathnorm.Canonical(rawPath)
		if err != nil {
			// Propagate real OS errors (e.g. permission denied on a parent).
			return Config{}, fmt.Errorf("canonicalize repo path %q: %w", rawPath, err)
		}

		_, statErr := os.Stat(canonical)
		missing := statErr != nil && errors.Is(statErr, os.ErrNotExist)

		cfg.Repos = append(cfg.Repos, RepoConfig{
			Path:    canonical,
			Missing: missing,
		})
	}

	return cfg, nil
}

// SaveConfig writes cfg to the config file, creating the directory if needed.
// The write is not atomic (config edits are infrequent and human-initiated);
// for atomic writes see roster.go which uses a temp-file + rename pattern.
func SaveConfig(cfg Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	raw := configFile{
		DefaultHarness: cfg.DefaultHarness,
		LaunchMode:     normalizeLaunchMode(cfg.LaunchMode),
		Repos:          make([]string, 0, len(cfg.Repos)),
	}
	for _, r := range cfg.Repos {
		raw.Repos = append(raw.Repos, r.Path)
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// appendRepoPath appends canonical to repos when no existing entry shares the
// same Path. It returns the (possibly unchanged) slice and whether a new entry
// was added. Dedup is by exact canonical path, so callers must canonicalize
// before calling.
func appendRepoPath(repos []RepoConfig, canonical string) ([]RepoConfig, bool) {
	for _, r := range repos {
		if r.Path == canonical {
			return repos, false
		}
	}
	return append(repos, RepoConfig{Path: canonical}), true
}

// AddRepo canonicalizes path and appends it to the persisted config when it is
// not already configured, then saves. It returns whether the repo was newly
// added (false when it was already present).
//
// AddRepo does not verify that path is a git repository; callers should
// validate with git.RepoRoot first and pass the resolved root here.
func AddRepo(path string) (bool, error) {
	canonical, err := pathnorm.Canonical(path)
	if err != nil {
		return false, fmt.Errorf("canonicalize repo path %q: %w", path, err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		return false, err
	}

	repos, added := appendRepoPath(cfg.Repos, canonical)
	if !added {
		return false, nil
	}
	cfg.Repos = repos

	if err := SaveConfig(cfg); err != nil {
		return false, err
	}
	return true, nil
}
