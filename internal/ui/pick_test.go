package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/agents"
)

type fakeAgentFeed struct {
	list       []agents.Agent
	listErr    error
	focusedID  string
	focusedFor string
	seenIDs    []string
}

func (f *fakeAgentFeed) List(context.Context) ([]agents.Agent, error) {
	return f.list, f.listErr
}

func (f *fakeAgentFeed) Focus(_ context.Context, id, client string) error {
	f.focusedID, f.focusedFor = id, client

	return nil
}

func (f *fakeAgentFeed) Seen(_ context.Context, id string) error {
	f.seenIDs = append(f.seenIDs, id)

	return nil
}

// drive feeds msg to the model and follows resulting commands until none remain,
// skipping tick commands (they sleep) and reporting whether tea.Quit was issued.
func drive(t *testing.T, m pickModel, msg tea.Msg) (pickModel, bool) {
	t.Helper()

	quit := false
	pending := []tea.Msg{msg}
	for len(pending) > 0 {
		next := pending[0]
		pending = pending[1:]

		model, cmd := m.Update(next)
		m = model.(pickModel)
		if cmd == nil {
			continue
		}

		out := cmd()
		switch out := out.(type) {
		case tea.QuitMsg:
			quit = true
		case tea.BatchMsg:
			for _, c := range out {
				if c == nil {
					continue
				}
				if res := c(); res != nil {
					if _, isTick := res.(pickTickMsg); !isTick {
						pending = append(pending, res)
					}
				}
			}
		default:
			pending = append(pending, out)
		}
	}

	return m, quit
}

func newTestPick(feed agentFeed) pickModel {
	return newPickModel(feed, "/dev/ttys001", func() time.Time { return agentListNow })
}

func TestPickRendersFirstListReply(t *testing.T) {
	m := newTestPick(&fakeAgentFeed{list: agentsInEveryGroup()})

	m, _ = drive(t, m, pickListMsg{agents: agentsInEveryGroup()})

	view := m.View()
	for _, want := range []string{"Needs you", "Done", "Working", "Idle", "Stopped"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestPickEnterFocusesCursorAgentForClientThenQuits(t *testing.T) {
	feed := &fakeAgentFeed{list: agentsInEveryGroup()}
	m, _ := drive(t, newTestPick(feed), pickListMsg{agents: feed.list})

	m, _ = drive(t, m, keyMsg("/"))
	for _, k := range []string{"a", "p", "i"} {
		m, _ = drive(t, m, keyMsg(k))
	}
	_, quit := drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if feed.focusedID != "/a" || feed.focusedFor != "/dev/ttys001" {
		t.Fatalf("focus = (%q, %q), want (/a, /dev/ttys001)", feed.focusedID, feed.focusedFor)
	}
	if !quit {
		t.Fatal("pick should quit after focusing")
	}
}

func TestPickXMarksCursorAgentSeenAndStaysOpen(t *testing.T) {
	feed := &fakeAgentFeed{list: agentsInEveryGroup()}
	m, _ := drive(t, newTestPick(feed), pickListMsg{agents: feed.list})

	_, quit := drive(t, m, keyMsg("x"))

	if len(feed.seenIDs) != 1 || feed.seenIDs[0] != "/a" {
		t.Fatalf("seen = %v, want [/a]", feed.seenIDs)
	}
	if quit {
		t.Fatal("pick should stay open after marking seen")
	}
}

func TestPickShowsNotRunningMessageAndWaitsForKey(t *testing.T) {
	m, quit := drive(t, newTestPick(&fakeAgentFeed{}), pickListMsg{err: agentfeed.ErrNotRunning})

	if quit {
		t.Fatal("pick must not quit before a key press")
	}
	if !strings.Contains(m.View(), notRunningText) {
		t.Fatalf("view missing not-running message:\n%s", m.View())
	}

	_, quit = drive(t, m, keyMsg("z"))
	if !quit {
		t.Fatal("any key should quit from the not-running screen")
	}
}

func TestPickKeepsLastListWhenRefreshFails(t *testing.T) {
	feed := &fakeAgentFeed{list: agentsInEveryGroup()}
	m, _ := drive(t, newTestPick(feed), pickListMsg{agents: feed.list})

	m, _ = drive(t, m, pickListMsg{err: agentfeed.ErrBusy})

	view := m.View()
	if !strings.Contains(view, "api·fix-auth") || !strings.Contains(view, agentfeed.ErrBusy.Error()) {
		t.Fatalf("expected stale list plus error:\n%s", view)
	}
}
