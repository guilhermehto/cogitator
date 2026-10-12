package agentfeed

import (
	"context"
	"errors"
	"log/slog"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const (
	messageNothingNeedsYou = "cogitator: nothing needs you"
	messageNotRunning      = "cogitator isn't running"
	messageNextFailed      = "cogitator: next failed"
)

type nextFeed interface {
	List(ctx context.Context) ([]agents.Agent, error)
	Focus(ctx context.Context, id, client string) error
}

type messenger interface {
	Display(client, text string) error
}

type tmuxMessenger struct{}

func (tmuxMessenger) Display(client, text string) error {
	return tmuxctl.DisplayMessage(tmuxctl.DefaultRunner, client, text)
}

// RunNext jumps client to the most urgent agent, or to the next one after the
// agent the client is looking at. Outcomes the user should see are shown in
// tmux and return nil: run-shell overlays any output and reports a non-zero
// exit as a failure.
func RunNext(ctx context.Context, client, windowID, sessionID string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	return runNext(ctx, NewClient(), tmuxMessenger{}, client, windowID, sessionID, logger)
}

func runNext(ctx context.Context, feed nextFeed, msgs messenger, client, windowID, sessionID string, logger *slog.Logger) error {
	text, err := jumpToNext(ctx, feed, client, windowID, sessionID)
	if errors.Is(err, ErrNotRunning) {
		text = messageNotRunning
	} else if err != nil {
		logger.Error("agentfeed: next failed", "err", err)
		text = messageNextFailed
	}

	if text == "" {
		return nil
	}

	if displayErr := msgs.Display(client, text); displayErr != nil {
		logger.Error("agentfeed: display message failed", "err", displayErr)
	}
	return nil
}

// jumpToNext returns the message to show, empty when the focus succeeded.
func jumpToNext(ctx context.Context, feed nextFeed, client, windowID, sessionID string) (string, error) {
	list, err := feed.List(ctx)
	if err != nil {
		return "", err
	}

	currentID, _ := agents.AgentAt(list, windowID, sessionID)
	target, ok := agents.Next(list, currentID)
	if !ok {
		return messageNothingNeedsYou, nil
	}

	if err := feed.Focus(ctx, target.ID, client); err != nil {
		return "", err
	}
	return "", nil
}
