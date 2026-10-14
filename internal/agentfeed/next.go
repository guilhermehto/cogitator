package agentfeed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/mux"
)

const (
	messageNothingNeedsYou = "cogitator: nothing needs you"
	messageNotRunning      = "cogitator isn't running"
	messageNextFailed      = "cogitator: next failed"

	// printedMessagePause keeps a printed message on screen long enough to
	// read before next exits and its floating pane closes.
	printedMessagePause = 1500 * time.Millisecond
)

type nextFeed interface {
	List(ctx context.Context) ([]agents.Agent, error)
	Focus(ctx context.Context, id, client string) error
}

type messenger interface {
	Message(client, text string) error
}

// notifier shows next's outcome through the multiplexer, or prints it to out
// and pauses when the multiplexer cannot show messages (zellij).
type notifier struct {
	msgs  messenger
	out   io.Writer
	pause func()
}

func (n notifier) show(client, text string) error {
	err := n.msgs.Message(client, text)
	if !errors.Is(err, mux.ErrUnsupported) {
		return err
	}

	if _, err := fmt.Fprintln(n.out, text); err != nil {
		return err
	}
	n.pause()

	return nil
}

// RunNext jumps client to the most urgent agent, or to the next one after the
// agent the client is looking at. Outcomes the user should see are shown via
// backend.Message, or printed to stdout when the backend has no messages, and
// return nil: run-shell overlays any output and reports a non-zero exit as a
// failure.
func RunNext(ctx context.Context, backend mux.Backend, client, windowID, sessionID string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	n := notifier{msgs: backend, out: os.Stdout, pause: func() { time.Sleep(printedMessagePause) }}

	return runNext(ctx, NewClient(), n, client, windowID, sessionID, logger)
}

func runNext(ctx context.Context, feed nextFeed, n notifier, client, windowID, sessionID string, logger *slog.Logger) error {
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

	if displayErr := n.show(client, text); displayErr != nil {
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
