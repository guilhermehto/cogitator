package ui

import (
	"context"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/provider"
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

	store := state.New(ctx, cfg, logger)

	// Boot providers through the generic manager. The opencode provider owns
	// its own mDNS discovery loop (discovery.Browse) and feeds the supervisor
	// via OnAdd/OnRemove. The manager starts each provider in its own goroutine.
	sup := supervisor.New(store, cfg, logger)
	ocProvider := supervisor.NewOpenCodeProvider(sup, cfg, logger)
	var mgr provider.Manager
	mgr.Register(ocProvider)
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
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
