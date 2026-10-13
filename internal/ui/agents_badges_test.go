package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

// fakeTmuxServer is a one-window tmux server that stores @cog_* options the
// way the real one does, so polls diff against what earlier writes left.
type fakeTmuxServer struct {
	dir            string
	window         map[string]string
	globals        map[string]string
	clientActivity int64
	// clientAt is the "session\twindow" the one client is looking at.
	clientAt string
	setCalls int
}

func newFakeTmuxServer(dir string) *fakeTmuxServer {
	return &fakeTmuxServer{dir: dir, window: map[string]string{}, globals: map[string]string{}, clientAt: "$1\t@1"}
}

func (s *fakeTmuxServer) Run(args ...string) (string, error) {
	switch args[0] {
	case "list-panes":
		return fmt.Sprintf("%%1\t@1\t$1\tmain\t%s\tnode\t%s\t%s\t%s\t%s\n",
			s.dir, s.dir, s.window["@cog_status"], s.window["@cog_reason"], s.window["@cog_since"]), nil
	case "list-clients":
		return fmt.Sprintf("/dev/ttys001\t%s\t%d\n", s.clientAt, s.clientActivity), nil
	case "show-options":
		return s.showGlobals(args), nil
	case "set-option":
		s.setCalls++
		s.applySets(args)

		return "", nil
	}

	return "", fmt.Errorf("unexpected tmux command %v", args)
}

func (s *fakeTmuxServer) showGlobals(args []string) string {
	var out strings.Builder
	for i, arg := range args {
		if arg == "-gqv" {
			if value, set := s.globals[args[i+1]]; set {
				out.WriteString(value + "\n")
			}
			out.WriteString("\x1f-cog-end-\x1f\n")
		}
	}

	return out.String()
}

func (s *fakeTmuxServer) applySets(args []string) {
	var cmd []string
	flush := func() {
		if len(cmd) > 0 {
			s.applySet(cmd)
		}
		cmd = nil
	}

	for _, arg := range args {
		if arg == ";" {
			flush()

			continue
		}
		cmd = append(cmd, arg)
	}
	flush()
}

func (s *fakeTmuxServer) applySet(cmd []string) {
	global := cmd[1] == "-g"
	unset := cmd[2] == "-u"
	rest := cmd[2:]
	if unset {
		rest = cmd[3:]
	}
	if !global {
		rest = rest[2:]
	}

	target := s.window
	if global {
		target = s.globals
	}
	if unset {
		delete(target, rest[0])

		return
	}
	target[rest[0]] = rest[1]
}

func (s *fakeTmuxServer) paneOption(name string) string { return s.window[name] }

func badgeTestModel(t *testing.T, attn state.Attention, server *fakeTmuxServer) (model, *recordingViewMarker) {
	t.Helper()
	t.Setenv("TMUX", "/tmp/tmux-test,1,0")

	marker := &recordingViewMarker{}
	m := feedTestModel(&fakeTmuxOps{available: true}, nil)
	m.viewMarker = marker
	m.attachBadges(server)

	return rebuildAgents(t, m, []settings.Row{worktreeRow(server.dir, settings.StateRunning, attn)}), marker
}

func pollOnce(t *testing.T, m model) model {
	t.Helper()

	updated, cmd := m.Update(focusTickMsg{})
	m = updated.(model)
	for _, msg := range runBatchWithTimeout(cmd) {
		if poll, ok := msg.(focusPollMsg); ok {
			updated, write := m.Update(poll)
			m = updated.(model)
			if write != nil {
				m.Update(write())
			}
		}
	}

	return m
}

func runBatchWithTimeout(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}

	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, inner := range batch {
			if inner == nil {
				continue
			}
			msgs = append(msgs, runWithTimeout(inner))
		}

		return msgs
	}

	return []tea.Msg{msg}
}

func runWithTimeout(cmd tea.Cmd) tea.Msg {
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()

	select {
	case msg := <-result:
		return msg
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

func TestBadgesPublishDoneStatusAndGlobalCount(t *testing.T) {
	server := newFakeTmuxServer(t.TempDir())
	m, _ := badgeTestModel(t, state.AttnFinished, server)

	pollOnce(t, m)

	if got := server.paneOption("@cog_status"); got != "done" {
		t.Fatalf("@cog_status = %q, want done", got)
	}
	if got := server.globals["@cog_done"]; got != "1" {
		t.Fatalf("@cog_done = %q, want 1", got)
	}
}

func TestBadgesSkipWritesWhenNothingChanged(t *testing.T) {
	server := newFakeTmuxServer(t.TempDir())
	m, _ := badgeTestModel(t, state.AttnFinished, server)

	m = pollOnce(t, m)
	writesAfterFirstPoll := server.setCalls
	pollOnce(t, m)

	if server.setCalls != writesAfterFirstPoll {
		t.Fatalf("set-option calls = %d after steady poll, want %d", server.setCalls, writesAfterFirstPoll)
	}
}

func TestFocusKeepsAgentDoneWhileClientIsOnIt(t *testing.T) {
	server := newFakeTmuxServer(t.TempDir())
	server.clientActivity = time.Now().Add(time.Minute).Unix()
	m, marker := badgeTestModel(t, state.AttnFinished, server)

	m = pollOnce(t, m)
	pollOnce(t, m)

	if len(marker.viewed) != 0 {
		t.Fatalf("viewed = %v, want none while the client is on the agent", marker.viewed)
	}
	if got := server.paneOption("@cog_status"); got != "done" {
		t.Fatalf("@cog_status = %q, want done", got)
	}
}

func TestFocusMarksDoneAgentViewedOnceClientLeaves(t *testing.T) {
	server := newFakeTmuxServer(t.TempDir())
	m, marker := badgeTestModel(t, state.AttnFinished, server)
	m = pollOnce(t, m)

	server.clientAt = "$9\t@9"
	pollOnce(t, m)

	if len(marker.viewed) != 1 || marker.viewed[0] != "ses_1" {
		t.Fatalf("viewed = %v, want [ses_1]", marker.viewed)
	}
}

func TestFocusLeavesAgentUnseenWhenClientNeverVisited(t *testing.T) {
	server := newFakeTmuxServer(t.TempDir())
	server.clientAt = "$9\t@9"
	m, marker := badgeTestModel(t, state.AttnFinished, server)

	m = pollOnce(t, m)
	pollOnce(t, m)

	if len(marker.viewed) != 0 {
		t.Fatalf("viewed = %v, want none for an agent nobody looked at", marker.viewed)
	}
}

func TestBadgesShowIdleAfterAgentIsViewed(t *testing.T) {
	server := newFakeTmuxServer(t.TempDir())
	m, _ := badgeTestModel(t, state.AttnFinished, server)
	m = pollOnce(t, m)

	m = rebuildAgents(t, m, []settings.Row{worktreeRow(server.dir, settings.StateRunning, state.AttnInactive)})
	pollOnce(t, m)

	if got := server.paneOption("@cog_status"); got != "idle" {
		t.Fatalf("@cog_status = %q, want idle", got)
	}
	if _, set := server.globals["@cog_done"]; set {
		t.Fatalf("@cog_done still set: %q", server.globals["@cog_done"])
	}
}

func TestClearBadgesRemovesWindowAndGlobalOptions(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-test,1,0")
	server := newFakeTmuxServer(t.TempDir())
	server.window["@cog_status"] = "done"
	server.window["@cog_reason"] = "finished"
	server.globals["@cog_done"] = "1"
	server.globals["@cog_needs"] = "2"

	clearBadges(server, time.Second)

	if len(server.window) != 0 || len(server.globals) != 0 {
		t.Fatalf("leftover options: window=%v globals=%v", server.window, server.globals)
	}
}

func TestClearBadgesGivesUpWhenTmuxHangs(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-test,1,0")
	block := make(chan struct{})
	defer close(block)
	hung := tmuxctl.RunnerFunc(func(...string) (string, error) {
		<-block

		return "", nil
	})

	start := time.Now()
	clearBadges(hung, 20*time.Millisecond)

	if time.Since(start) > time.Second {
		t.Fatal("clearBadges did not honour its timeout")
	}
}
