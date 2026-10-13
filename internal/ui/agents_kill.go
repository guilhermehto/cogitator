package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

// agentKilledMsg reports the outcome of closing an agent's tmux window.
type agentKilledMsg struct {
	name string
	err  error
}

// startAgentKill opens the kill confirmation for agent, or explains in the
// hint line why its window cannot be closed from here.
func (m *model) startAgentKill(agent agents.Agent) {
	if agent.WindowID == "" {
		m.tmuxHint = "kill: agent has no tmux window"

		return
	}

	// Closing the window cogitator itself runs in would end this TUI too.
	if m.selfWindowID() == agent.WindowID {
		m.tmuxHint = "kill: cogitator runs in this window — close it from tmux"

		return
	}

	m.killAgentTarget = agent
	m.prompt = promptConfirmKillAgent
}

// selfWindowID is the tmux window holding cogitator's own pane, or "" when
// cogitator is not in tmux or its pane is not in the latest listing.
func (m model) selfWindowID() string {
	if m.selfPaneID == "" {
		return ""
	}

	for _, p := range m.panes {
		if p.PaneID == m.selfPaneID {
			return p.WindowID
		}
	}

	return ""
}

// updateKillAgentConfirm handles the single kill gate: 'y' closes the window;
// any other key cancels. One gate suffices because the session transcript
// stays on disk and the agent can be resumed.
func (m model) updateKillAgentConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	target := m.killAgentTarget
	m.prompt = promptIdle
	m.killAgentTarget = agents.Agent{}

	if msg.String() != "y" && msg.String() != "Y" {
		return m, nil
	}

	return m, killAgentWindowCmd(m.tmux, agentName(target), target.WindowID)
}

func killAgentWindowCmd(ops tmuxOps, name, windowID string) tea.Cmd {
	return func() tea.Msg {
		if ops == nil || !ops.Available() {
			return agentKilledMsg{name: name, err: tmuxctl.ErrNotAvailable}
		}

		return agentKilledMsg{name: name, err: ops.KillWindow(tmuxctl.Target(windowID))}
	}
}

// applyAgentKilled reports a failed kill, or refreshes the pane listing so
// the agent drops its window right away instead of on the next snapshot.
func (m *model) applyAgentKilled(msg agentKilledMsg) tea.Cmd {
	if msg.err != nil {
		m.tmuxHint = fmt.Sprintf("kill %s: %v", msg.name, msg.err)

		return nil
	}

	return m.requestPanes()
}

func (m model) agentKillPromptLine() string {
	return wtHintStyle.Render(fmt.Sprintf(
		"kill agent [%s]? closes its tmux window · y to confirm · any other key cancels",
		agentName(m.killAgentTarget)))
}
