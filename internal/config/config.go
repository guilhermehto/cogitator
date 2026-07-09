package config

import (
	"os"
	"path/filepath"
	"time"
)

// Config centralizes runtime tunables. Values are currently static defaults;
// no env or file-based overrides are wired yet.
type Config struct {
	RecentWindow            time.Duration
	PollEvery               time.Duration
	RecencyPollEvery        time.Duration
	StatusDeadline          time.Duration
	MessageActivityDebounce time.Duration
	DiscoveryBrowseTimeout  time.Duration
	DiscoverySleep          time.Duration
	HTTPTimeout             time.Duration
	EventBackoffMax         time.Duration
	PermissionSyncTimeout   time.Duration
	TaskwarriorTimeout      time.Duration
	RecentSyncTimeout       time.Duration
	SessionLookupTimeout    time.Duration
	UnreachableThreshold    int
	// InactiveHideAfter hides idle sessions from the sessions pane once
	// their last activity is older than this threshold. Sessions needing
	// attention (permission, question, error) are never hidden regardless
	// of age. A non-positive value disables the rule.
	InactiveHideAfter time.Duration

	// CodexEnabled enables the polled Codex session monitor. Default false.
	// When false, no Codex provider is started and cogitator behaves exactly
	// as before Codex support was added.
	CodexEnabled bool

	// CodexHome is the path to the Codex home directory (CODEX_HOME). When
	// empty the provider defaults to ~/.codex (resolved by the reader).
	// Populated from the CODEX_HOME environment variable in Default().
	CodexHome string

	// CodexPollInterval is how often the Codex provider polls CODEX_HOME.
	CodexPollInterval time.Duration

	// CodexRecencyWindow is the duration within which a session's last
	// activity is considered "live" (SourceLive). Sessions older than this
	// window are labelled SourceRecent.
	CodexRecencyWindow time.Duration

	// ClaudeCodeEnabled enables the polled Claude Code session monitor.
	// When false, no Claude Code provider is started and cogitator behaves
	// exactly as before Claude Code support was added.
	ClaudeCodeEnabled bool

	// ClaudeCodeHome is the path to the Claude home directory (CLAUDE_HOME).
	// When empty the provider defaults to ~/.claude (resolved by the reader).
	// Populated from the CLAUDE_HOME environment variable in Default().
	ClaudeCodeHome string

	// ClaudeCodePollInterval is how often the Claude Code provider polls
	// ClaudeCodeHome.
	ClaudeCodePollInterval time.Duration

	// ClaudeCodeRecencyWindow is the duration within which a session's last
	// activity is considered "live" (SourceLive). Sessions older than this
	// window are labelled SourceRecent.
	ClaudeCodeRecencyWindow time.Duration

	// OmpEnabled enables the polled Oh My Pi (omp) session monitor. When
	// false, no omp provider is started and cogitator behaves exactly as
	// before omp support was added.
	OmpEnabled bool

	// OmpHome is the omp agent directory (the parent of sessions/). When empty
	// the reader resolves $PI_CODING_AGENT_DIR, then $PI_CONFIG_DIR/agent, then
	// ~/.omp/agent. Populated from PI_CODING_AGENT_DIR in Default().
	OmpHome string

	// OmpPollInterval is how often the omp provider polls OmpHome.
	OmpPollInterval time.Duration

	// OmpRecencyWindow is the duration within which a session's last activity
	// is considered "live" (SourceLive). Sessions older than this window are
	// labelled SourceRecent.
	OmpRecencyWindow time.Duration

	// RovodevEnabled enables the polled Atlassian Rovo Dev CLI session monitor.
	// When false, no Rovo Dev provider is started and cogitator behaves exactly
	// as before Rovo Dev support was added.
	RovodevEnabled bool

	// RovodevHome is the Rovo Dev home directory (the parent of sessions/). When
	// empty the reader defaults to ~/.rovodev. Populated from the ROVODEV_HOME
	// environment variable in Default() as a cogitator-side override for the
	// poll path.
	RovodevHome string

	// RovodevPollInterval is how often the Rovo Dev provider polls RovodevHome.
	RovodevPollInterval time.Duration

	// RovodevRecencyWindow is the duration within which a session's last
	// activity is considered "live" (SourceLive). Sessions older than this
	// window are labelled SourceRecent.
	RovodevRecencyWindow time.Duration
}

// codexHomeDirExists reports whether the resolved Codex home directory exists
// and is a directory. It mirrors the resolution logic used by the Codex
// provider: $CODEX_HOME when set, otherwise ~/.codex.
func codexHomeDirExists() bool {
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".codex")
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// claudeProjectsDirExists reports whether the resolved Claude home projects
// directory exists and is a directory. It mirrors the resolution logic used by
// the Claude Code provider: $CLAUDE_HOME when set, otherwise ~/.claude.
// Detection checks the projects subdirectory to avoid false positives from an
// empty ~/.claude directory.
func claudeProjectsDirExists() bool {
	dir := os.Getenv("CLAUDE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".claude")
	}
	info, err := os.Stat(filepath.Join(dir, "projects"))
	return err == nil && info.IsDir()
}

// ompAgentDirExists reports whether the resolved omp agent directory exists
// and is a directory. It mirrors the resolution logic used by the omp provider:
// $PI_CODING_AGENT_DIR when set, otherwise $PI_CONFIG_DIR/agent, otherwise
// ~/.omp/agent.
func ompAgentDirExists() bool {
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		if cfg := os.Getenv("PI_CONFIG_DIR"); cfg != "" {
			dir = filepath.Join(cfg, "agent")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return false
			}
			dir = filepath.Join(home, ".omp", "agent")
		}
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// rovodevSessionsDirExists reports whether the resolved Rovo Dev sessions
// directory exists and is a directory. It mirrors the resolution logic used by
// the Rovo Dev provider: $ROVODEV_HOME when set, otherwise ~/.rovodev. Detection
// checks the sessions subdirectory to avoid false positives from an empty
// ~/.rovodev directory (e.g. one holding only config.yml).
func rovodevSessionsDirExists() bool {
	dir := os.Getenv("ROVODEV_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".rovodev")
	}
	info, err := os.Stat(filepath.Join(dir, "sessions"))
	return err == nil && info.IsDir()
}

func Default() *Config {
	return &Config{
		RecentWindow:            30 * time.Minute,
		PollEvery:               5 * time.Second,
		RecencyPollEvery:        30 * time.Second,
		StatusDeadline:          3 * time.Second,
		MessageActivityDebounce: 250 * time.Millisecond,
		DiscoveryBrowseTimeout:  4 * time.Second,
		DiscoverySleep:          3 * time.Second,
		HTTPTimeout:             10 * time.Second,
		EventBackoffMax:         5 * time.Second,
		PermissionSyncTimeout:   5 * time.Second,
		TaskwarriorTimeout:      5 * time.Second,
		RecentSyncTimeout:       8 * time.Second,
		SessionLookupTimeout:    5 * time.Second,
		UnreachableThreshold:    3,
		InactiveHideAfter:       5 * time.Minute,

		CodexEnabled:       codexHomeDirExists(),
		CodexHome:          os.Getenv("CODEX_HOME"),
		CodexPollInterval:  5 * time.Second,
		CodexRecencyWindow: 30 * time.Minute,

		ClaudeCodeEnabled:       claudeProjectsDirExists(),
		ClaudeCodeHome:          os.Getenv("CLAUDE_HOME"),
		ClaudeCodePollInterval:  5 * time.Second,
		ClaudeCodeRecencyWindow: 30 * time.Minute,

		OmpEnabled:       ompAgentDirExists(),
		OmpHome:          os.Getenv("PI_CODING_AGENT_DIR"),
		OmpPollInterval:  5 * time.Second,
		OmpRecencyWindow: 30 * time.Minute,

		RovodevEnabled:       rovodevSessionsDirExists(),
		RovodevHome:          os.Getenv("ROVODEV_HOME"),
		RovodevPollInterval:  5 * time.Second,
		RovodevRecencyWindow: 30 * time.Minute,
	}
}
