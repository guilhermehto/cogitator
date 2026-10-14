package agentfeed

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/mux"
)

type fakeNextFeed struct {
	list    []agents.Agent
	listErr error
	focused []string
}

func (f *fakeNextFeed) List(context.Context) ([]agents.Agent, error) {
	return f.list, f.listErr
}

func (f *fakeNextFeed) Focus(_ context.Context, id, _ string) error {
	f.focused = append(f.focused, id)
	return nil
}

type fakeMessenger struct {
	shown []string
	err   error
}

func (m *fakeMessenger) Message(_, text string) error {
	m.shown = append(m.shown, text)
	return m.err
}

func needsYou(id, windowID string) agents.Agent {
	return agents.Agent{ID: id, WindowID: windowID, Group: agents.GroupNeedsYou}
}

// nextRun records what runNext showed, printed and how often it paused.
type nextRun struct {
	msgs    *fakeMessenger
	printed bytes.Buffer
	pauses  int
}

func runNextVia(t *testing.T, msgs *fakeMessenger, feed *fakeNextFeed, windowID, sessionID string) *nextRun {
	t.Helper()
	run := &nextRun{msgs: msgs}
	n := notifier{msgs: msgs, out: &run.printed, pause: func() { run.pauses++ }}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := runNext(context.Background(), feed, n, "c", windowID, sessionID, logger); err != nil {
		t.Fatalf("runNext returned %v, want nil", err)
	}
	return run
}

func runNextWith(t *testing.T, feed *fakeNextFeed, windowID, sessionID string) *fakeMessenger {
	t.Helper()
	run := runNextVia(t, &fakeMessenger{}, feed, windowID, sessionID)
	if run.printed.Len() != 0 || run.pauses != 0 {
		t.Fatalf("printed %q with %d pauses, want nothing when the multiplexer shows messages", run.printed.String(), run.pauses)
	}
	return run.msgs
}

func TestRunNext_FocusesSecondAgentFromFirstWindow(t *testing.T) {
	feed := &fakeNextFeed{list: []agents.Agent{needsYou("/a", "@1"), needsYou("/b", "@2")}}

	msgs := runNextWith(t, feed, "@1", "")

	if len(feed.focused) != 1 || feed.focused[0] != "/b" {
		t.Errorf("focused = %v, want [/b]", feed.focused)
	}
	if len(msgs.shown) != 0 {
		t.Errorf("shown = %v, want none", msgs.shown)
	}
}

func TestRunNext_SiblingWindowOfOwnedSessionAdvancesPastThatAgent(t *testing.T) {
	owned := needsYou("/a", "@1")
	owned.SessionOwned = true
	owned.MuxSessionID = "$1"
	feed := &fakeNextFeed{list: []agents.Agent{owned, needsYou("/b", "@2")}}

	runNextWith(t, feed, "@9", "$1")

	if len(feed.focused) != 1 || feed.focused[0] != "/b" {
		t.Errorf("focused = %v, want [/b]", feed.focused)
	}
}

func TestRunNext_ReportsNothingNeedsYouWhenAllWorking(t *testing.T) {
	feed := &fakeNextFeed{list: []agents.Agent{{ID: "/a", Group: agents.GroupWorking}}}

	msgs := runNextWith(t, feed, "", "")

	if len(feed.focused) != 0 {
		t.Errorf("focused = %v, want none", feed.focused)
	}
	if len(msgs.shown) != 1 || msgs.shown[0] != messageNothingNeedsYou {
		t.Errorf("shown = %v, want [%q]", msgs.shown, messageNothingNeedsYou)
	}
}

func TestRunNext_ReportsNotRunningWhenFeedMissing(t *testing.T) {
	feed := &fakeNextFeed{listErr: ErrNotRunning}

	msgs := runNextWith(t, feed, "", "")

	if len(msgs.shown) != 1 || msgs.shown[0] != messageNotRunning {
		t.Errorf("shown = %v, want [%q]", msgs.shown, messageNotRunning)
	}
}

func TestRunNext_ReportsUnexpectedErrorsWithoutFailing(t *testing.T) {
	feed := &fakeNextFeed{listErr: errors.New("boom")}

	msgs := runNextWith(t, feed, "", "")

	if len(msgs.shown) != 1 || msgs.shown[0] != messageNextFailed {
		t.Errorf("shown = %v, want [%q]", msgs.shown, messageNextFailed)
	}
}

func TestRunNext_PrintsAndPausesWhenMessagesUnsupported(t *testing.T) {
	feed := &fakeNextFeed{list: []agents.Agent{{ID: "/a", Group: agents.GroupWorking}}}

	run := runNextVia(t, &fakeMessenger{err: mux.ErrUnsupported}, feed, "", "")

	if got, want := run.printed.String(), messageNothingNeedsYou+"\n"; got != want {
		t.Errorf("printed = %q, want %q", got, want)
	}
	if run.pauses != 1 {
		t.Errorf("pauses = %d, want 1", run.pauses)
	}
}

func TestRunNext_FocusPrintsNothingWhenMessagesUnsupported(t *testing.T) {
	feed := &fakeNextFeed{list: []agents.Agent{needsYou("/a", "@1")}}

	run := runNextVia(t, &fakeMessenger{err: mux.ErrUnsupported}, feed, "", "")

	if run.printed.Len() != 0 || run.pauses != 0 {
		t.Errorf("printed %q with %d pauses, want a silent jump", run.printed.String(), run.pauses)
	}
}
