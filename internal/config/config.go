package config

import (
	"os"
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

		CodexEnabled:       false,
		CodexHome:          os.Getenv("CODEX_HOME"),
		CodexPollInterval:  5 * time.Second,
		CodexRecencyWindow: 30 * time.Minute,
	}
}
