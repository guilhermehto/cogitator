package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/agents"
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

	if line, ok := statusFromFeed(ctx, agentfeed.NewClient(), logger); ok {
		fmt.Println(line)
		return nil
	}

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

// feedLister is the slice of agentfeed.Client that --status needs.
type feedLister interface {
	List(ctx context.Context) ([]agents.Agent, error)
}

// statusCounts holds the per-glyph tallies shown in the status line.
type statusCounts struct {
	question, permission, errored, finished int
}

// statusFromFeed renders the status line from the running TUI's feed. ok is
// false when the caller should fall back to mDNS discovery.
func statusFromFeed(ctx context.Context, feed feedLister, logger *slog.Logger) (line string, ok bool) {
	list, err := feed.List(ctx)
	if errors.Is(err, agentfeed.ErrNotRunning) {
		return "", false
	}
	if err != nil {
		logger.Warn("status: agent feed unavailable, falling back to discovery", "err", err)
		return "", false
	}

	counts := agents.Counts(list)
	return statusCounts{
		question:   counts.NeedsYou[state.AttnQuestionPending],
		permission: counts.NeedsYou[state.AttnPermissionPending],
		errored:    counts.NeedsYou[state.AttnErrored],
		finished:   counts.ByGroup[agents.GroupDone],
	}.render(), true
}

// formatStatusLine counts attention-bearing rows and renders a compact
// `<glyph> <count>` summary. Empty string means no session needs eyes.
func formatStatusLine(rows []state.SessionView) string {
	var counts statusCounts
	for _, sv := range rows {
		switch sv.Attention {
		case state.AttnPermissionPending:
			counts.permission++
		case state.AttnQuestionPending:
			counts.question++
		case state.AttnErrored:
			counts.errored++
		case state.AttnFinished:
			counts.finished++
		}
	}
	return counts.render()
}

func (c statusCounts) render() string {
	var parts []string
	if c.question > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", glyphQuestion, c.question))
	}
	if c.permission > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", glyphPermission, c.permission))
	}
	if c.errored > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", glyphError, c.errored))
	}
	if c.finished > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", glyphFinished, c.finished))
	}
	return strings.Join(parts, " ")
}
