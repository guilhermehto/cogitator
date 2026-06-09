package ui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/provider"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/supervisor"
)

// RunStatus is the one-shot path used by status bars and shell prompts.
func RunStatus(cfg *config.Config, logger *slog.Logger) error {
	if cfg == nil {
		cfg = config.Default()
	}
	if logger == nil {
		logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := state.New(ctx, cfg, logger)

	sup := supervisor.New(store, cfg, logger)
	ocProvider := supervisor.NewOpenCodeProvider(sup, cfg, logger)
	var mgr provider.Manager
	mgr.Register(ocProvider)
	mgr.Start(ctx, store)

	snaps := store.Subscribe()
	deadline := time.NewTimer(cfg.StatusDeadline)
	defer deadline.Stop()

	for {
		select {
		case snap, ok := <-snaps:
			if !ok {
				fmt.Println("")
				return nil
			}
			if len(snap.Sessions) == 0 {
				continue
			}
			fmt.Println(formatStatusLine(snap.Sessions))
			return nil
		case <-deadline.C:
			fmt.Println("")
			return nil
		}
	}
}

// formatStatusLine counts attention-bearing rows and renders a compact
// `<glyph> <count>` summary. Empty string means no session needs eyes.
func formatStatusLine(rows []state.SessionView) string {
	perm, question, errored, finished := 0, 0, 0, 0
	for _, sv := range rows {
		switch sv.Attention {
		case state.AttnPermissionPending:
			perm++
		case state.AttnQuestionPending:
			question++
		case state.AttnErrored:
			errored++
		case state.AttnFinished:
			finished++
		}
	}

	var parts []string
	if question > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", glyphQuestion, question))
	}
	if perm > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", glyphPermission, perm))
	}
	if errored > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", glyphError, errored))
	}
	if finished > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", glyphFinished, finished))
	}
	return strings.Join(parts, " ")
}
