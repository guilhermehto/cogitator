package ui

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const shutdownClearTimeout = 2 * time.Second

var globalBadgeOptions = []string{agents.OptNeeds, agents.OptDone, agents.OptWorking}

type focusTickMsg struct{}

// focusPollMsg carries one tmux read: panes, client focus and the global
// @cog_* counters. ok is false when any of the reads failed.
type focusPollMsg struct {
	panes   []agents.Pane
	clients []tmuxctl.ClientFocus
	globals map[string]string
	ok      bool
}

// badgeWriteMsg reports the outcome of a badge write; a failure is retried
// implicitly because the next poll diffs against what tmux actually holds.
type badgeWriteMsg struct{ err error }

func focusTickCmd(every time.Duration) tea.Cmd {
	if every <= 0 {
		return nil
	}

	return tea.Tick(every, func(time.Time) tea.Msg { return focusTickMsg{} })
}

func pollTmuxCmd(r tmuxctl.Runner) tea.Cmd {
	return func() tea.Msg {
		panes, err := tmuxctl.ListPanes(r)
		if err != nil {
			return focusPollMsg{}
		}

		clients, err := tmuxctl.ListClientFocus(r)
		if err != nil {
			return focusPollMsg{}
		}

		globals, err := tmuxctl.ReadGlobalOptions(r, globalBadgeOptions)
		if err != nil {
			return focusPollMsg{}
		}

		return focusPollMsg{panes: toAgentPanes(panes), clients: clients, globals: globals, ok: true}
	}
}

func applyBadgesCmd(r tmuxctl.Runner, writes []tmuxctl.OptionWrite) tea.Cmd {
	return func() tea.Msg {
		return badgeWriteMsg{err: tmuxctl.ApplyOptions(r, writes)}
	}
}

// attachBadges enables the focus poll that publishes @cog_* options and
// clears Done agents the user visited and left, and the Agents tab preview.
func (m *model) attachBadges(r tmuxctl.Runner) {
	m.badgeRunner = r
	m.agentPreview.runner = r
}

func (m *model) handleFocusTick() tea.Cmd {
	next := focusTickCmd(m.cfg.FocusPollEvery)
	if m.badgeRunner == nil || m.focusPolling {
		return next
	}

	m.focusPolling = true

	return tea.Batch(next, pollTmuxCmd(m.badgeRunner), m.syncAgentPreview())
}

func (m *model) applyFocusPoll(msg focusPollMsg) tea.Cmd {
	m.focusPolling = false
	if !msg.ok {
		return nil
	}

	m.panes = msg.panes
	m.refreshAgents()

	visits, seen := agents.SeenOnLeave(m.agents, msg.clients, m.visits)
	m.visits = visits
	for _, agent := range seen {
		m.markAgentSeen(agent)
	}

	writes := agents.DiffOptions(m.agents, m.panes, msg.globals)
	if len(writes) == 0 {
		return nil
	}

	return applyBadgesCmd(m.badgeRunner, writes)
}

// clearBadges removes every @cog_* option cogitator owns, giving up after
// timeout so a wedged tmux server cannot hold up shutdown.
func clearBadges(r tmuxctl.Runner, timeout time.Duration) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		var panes []agents.Pane
		if listed, err := tmuxctl.ListPanes(r); err == nil {
			panes = toAgentPanes(listed)
		}

		_ = tmuxctl.ApplyOptions(r, agents.ClearAll(panes, nil))
	}()

	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// quitOnHangup stops the program when its tmux window is killed (SIGHUP),
// which Bubble Tea does not handle, so shutdown cleanup still runs.
func quitOnHangup(p *tea.Program) (stop func()) {
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)

	stopped := make(chan struct{})

	go func() {
		select {
		case <-hangups:
			p.Quit()
		case <-stopped:
		}
	}()

	return func() {
		signal.Stop(hangups)
		close(stopped)
	}
}

func (m model) initialFocusTick() tea.Cmd {
	if m.badgeRunner == nil {
		return nil
	}

	return focusTickCmd(m.cfg.FocusPollEvery)
}
