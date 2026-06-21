package ui

import (
	"context"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/omp"
	"github.com/guilhermehto/cogitator/internal/provider"
	"github.com/guilhermehto/cogitator/internal/singleinstance"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/supervisor"
	"github.com/guilhermehto/cogitator/internal/taskwarrior"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

func RunTUI(cfg *config.Config, logger *slog.Logger, bellEnabled, debug bool) error {
	if cfg == nil {
		cfg = config.Default()
	}
	if logger == nil {
		logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Enforce a single live cogitator: a newer process evicts an older one so a
	// freshly built binary always becomes the hook-socket owner instead of
	// degrading to poll-only mode behind a stale instance. A failure here is
	// non-fatal — the TUI still runs, just without the single-instance guarantee.
	if release, err := singleinstance.New(logger).Acquire(); err != nil {
		logger.Warn("single-instance acquire failed; continuing without it", "err", err)
	} else {
		defer release()
	}

	store := state.New(ctx, cfg, logger)

	// Seed the store with last-known attention from the persisted roster so
	// badges (finished, errored, permission, question) survive restarts.
	// workspace.Load prunes missing dirs and returns an empty map when the
	// file is absent; on any other error we fall back to an empty seed rather
	// than aborting startup.
	if roster, err := workspace.Load(); err == nil {
		store.RestoreSessions(rosterToRestored(roster))
	} else {
		logger.Warn("roster load failed; starting without restored badges", "err", err)
	}

	// Boot providers through the generic manager. The opencode provider owns
	// its own mDNS discovery loop (discovery.Browse) and feeds the supervisor
	// via OnAdd/OnRemove. The manager starts each provider in its own goroutine.
	sup := supervisor.New(store, cfg, logger)
	ocProvider := supervisor.NewOpenCodeProvider(sup, cfg, logger)
	var mgr provider.Manager
	mgr.Register(ocProvider)
	if cfg.CodexEnabled {
		codexProvider := codex.NewProvider(cfg.CodexHome, cfg.CodexPollInterval, cfg.CodexRecencyWindow, logger)
		mgr.Register(codexProvider)
	}
	if cfg.ClaudeCodeEnabled {
		claudeProvider := claudecode.NewProvider(cfg.ClaudeCodeHome, cfg.ClaudeCodePollInterval, cfg.ClaudeCodeRecencyWindow, logger)
		mgr.Register(claudeProvider)
	}
	if cfg.OmpEnabled {
		ompProvider := omp.NewProvider(cfg.OmpHome, cfg.OmpPollInterval, cfg.OmpRecencyWindow, logger)
		mgr.Register(ompProvider)
	}
	mgr.Start(ctx, store)

	// Start the roster recorder as a distinct subscriber. It drains snapshots
	// in its own goroutine and writes roster.json atomically off the hot path.
	// Cancelled by the existing defer cancel() above.
	rec := workspace.NewRecorder()
	rec.Run(ctx, store.Subscribe())

	m := newModel(store.Subscribe(), cfg, bellEnabled, debug, taskwarrior.NewClient())
	// Wire the recorder's Upserts channel so the model can inject create-time
	// roster entries (e.g. for Codex worktrees that are never live-discovered).
	m.rosterUpserts = rec.Upserts
	// Wire the store so jump/resume clears the AttnFinished badge.
	m.viewMarker = store
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
