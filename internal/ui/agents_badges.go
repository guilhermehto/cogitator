package ui

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/mux"
)

const shutdownClearTimeout = 2 * time.Second

var globalBadgeOptions = []string{agents.OptNeeds, agents.OptDone, agents.OptWorking}

type focusTickMsg struct{}

// focusPollMsg carries one tmux read: panes, client focus and the global
// @cog_* counters. ok is false when any of the reads failed.
type focusPollMsg struct {
	panes   []mux.Pane
	clients []mux.ClientFocus
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

// badgeBackend is a backend that can also publish @cog_* badges.
type badgeBackend interface {
	mux.Backend
	mux.Badger
}

func pollBadgesCmd(b badgeBackend) tea.Cmd {
	return func() tea.Msg {
		panes, err := b.ListPanes()
		if err != nil {
			return focusPollMsg{}
		}

		clients, err := b.ListClientFocus()
		if err != nil {
			return focusPollMsg{}
		}

		globals, err := b.ReadGlobalOptions(globalBadgeOptions)
		if err != nil {
			return focusPollMsg{}
		}

		return focusPollMsg{panes: panes, clients: clients, globals: globals, ok: true}
	}
}

func applyBadgesCmd(b mux.Badger, writes []mux.OptionWrite) tea.Cmd {
	return func() tea.Msg {
		return badgeWriteMsg{err: b.ApplyOptions(writes)}
	}
}

// attachBadges enables the Agents tab preview and, when the backend publishes
// badges, the focus poll that publishes @cog_* options and clears Done agents
// the user visited and left.
func (m *model) attachBadges(b mux.Backend) {
	m.agentPreview.backend = b
	if badges, ok := b.(badgeBackend); ok {
		m.badges = badges
	}
}

// handleFocusTick polls focus and badges, which refreshes the panes too. A
// backend without badges (zellij) still refreshes its panes on the tick, so a
// renamed tab reaches the agent list without waiting for a session event.
func (m *model) handleFocusTick() tea.Cmd {
	next := focusTickCmd(m.cfg.FocusPollEvery)
	if m.badges == nil {
		return tea.Batch(next, m.requestPanes())
	}
	if m.focusPolling {
		return next
	}

	m.focusPolling = true

	return tea.Batch(next, pollBadgesCmd(m.badges), m.syncAgentPreview())
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

	return applyBadgesCmd(m.badges, writes)
}

// clearBadges removes every @cog_* option cogitator owns, giving up after
// timeout so a wedged tmux server cannot hold up shutdown.
func clearBadges(b badgeBackend, timeout time.Duration) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		var panes []mux.Pane
		if listed, err := b.ListPanes(); err == nil {
			panes = listed
		}

		_ = b.ApplyOptions(agents.ClearAll(panes, nil))
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
	if m.badges == nil && m.listPanes == nil {
		return nil
	}

	return focusTickCmd(m.cfg.FocusPollEvery)
}
