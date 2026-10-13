package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
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

type captureRecorder struct {
	targets []string
}

func (c *captureRecorder) Run(args ...string) (string, error) {
	target := args[len(args)-1]
	c.targets = append(c.targets, target)

	return "building…\nAllow edit of main.go? [y/n]\n", nil
}

func newPreviewPick(t *testing.T, width int) (pickModel, *captureRecorder) {
	t.Helper()
	t.Setenv("TMUX", "/tmp/tmux-test,1,0")

	list := agentsInEveryGroup()
	for i := range list {
		list[i].WindowID = "@" + list[i].ID
	}
	recorder := &captureRecorder{}
	m := newTestPick(&fakeAgentFeed{list: list})
	m.preview.backend = tmuxctl.Backend{Runner: recorder}

	model, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	m = model.(pickModel)
	model, _ = m.Update(pickListMsg{agents: list})

	return model.(pickModel), recorder
}

func step(m pickModel, msg tea.Msg) (pickModel, tea.Cmd) {
	model, cmd := m.Update(msg)

	return model.(pickModel), cmd
}

func TestPreviewShowsReasonAndPaneTailForCursorAgent(t *testing.T) {
	m, recorder := newPreviewPick(t, 120)

	m, cmd := step(m, previewDebounceMsg{seq: m.preview.seq})
	m, _ = step(m, cmd())

	view := m.View()
	for _, want := range []string{"needs permission", "Allow edit of main.go? [y/n]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if len(recorder.targets) != 1 || recorder.targets[0] != "@/a" {
		t.Fatalf("capture targets = %v, want [@/a]", recorder.targets)
	}
}

func TestPreviewKeepsOneCaptureInFlightAndFinalSelectionWins(t *testing.T) {
	m, recorder := newPreviewPick(t, 120)

	m, cmd := step(m, previewDebounceMsg{seq: m.preview.seq})
	staleSeq := m.preview.seq
	firstCapture := cmd

	m, _ = step(m, keyMsg("j"))
	m, _ = step(m, keyMsg("j"))

	m, cmd = step(m, previewDebounceMsg{seq: staleSeq})
	if cmd != nil {
		t.Fatal("stale debounce must not start a capture")
	}

	m, cmd = step(m, previewDebounceMsg{seq: m.preview.seq})
	if cmd != nil {
		t.Fatal("debounce must not start a second capture while one is in flight")
	}

	m, cmd = step(m, firstCapture())
	if cmd == nil {
		t.Fatal("finished capture should chase the final selection")
	}
	m, _ = step(m, cmd())

	if got := recorder.targets; len(got) != 2 || got[1] != "@/c" {
		t.Fatalf("capture targets = %v, want second capture of @/c", got)
	}
	if m.preview.agentID != "/c" {
		t.Fatalf("preview agent = %q, want /c", m.preview.agentID)
	}
}

func TestPreviewToggleHidesPreviewAndWidensList(t *testing.T) {
	m, _ := newPreviewPick(t, 120)
	m, cmd := step(m, previewDebounceMsg{seq: m.preview.seq})
	m, _ = step(m, cmd())
	if !strings.Contains(m.View(), "needs permission") {
		t.Fatal("preview should be visible by default on a wide popup")
	}

	m, _ = step(m, keyMsg("p"))

	view := m.View()
	if strings.Contains(view, "needs permission") || strings.Contains(view, "Allow edit") {
		t.Fatalf("preview should be hidden:\n%s", view)
	}
}

func TestPreviewDefaultsOffOnNarrowPopup(t *testing.T) {
	m, _ := newPreviewPick(t, 80)

	if m.showPreview {
		t.Fatal("preview should default off below 100 columns")
	}
}
