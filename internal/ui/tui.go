package ui

import (
	"context"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/discovery"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/supervisor"
	"github.com/guilhermehto/cogitator/internal/taskwarrior"
)

func RunTUI(cfg *config.Config, logger *slog.Logger, bellEnabled bool) error {
	if cfg == nil {
		cfg = config.Default()
	}
	if logger == nil {
		logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := state.New(ctx, cfg, logger)
	if err := bootDiscovery(ctx, store, cfg, logger); err != nil {
		return err
	}

	m := newModel(store.Subscribe(), cfg, bellEnabled, taskwarrior.NewClient())
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func bootDiscovery(ctx context.Context, store *state.Store, cfg *config.Config, logger *slog.Logger) error {
	sup := supervisor.New(store, cfg, logger)

	events, err := discovery.Browse(ctx, cfg)
	if err != nil {
		return err
	}

	go func() {
		for ev := range events {
			switch {
			case ev.Added != nil:
				sup.OnAdd(ctx, *ev.Added)
			case ev.Removed != nil:
				sup.OnRemove(ev.Removed.ID)
			}
		}
	}()

	return nil
}
