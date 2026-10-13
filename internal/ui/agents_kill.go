package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

// agentKilledMsg reports the outcome of closing an agent's tmux window.
type agentKilledMsg struct {
	agent agents.Agent
	err   error
}

// startAgentKill opens the kill confirmation for agent, dismisses it when it
// has no window left to close, or explains in the hint line why its window
// cannot be closed from here.
func (m *model) startAgentKill(agent agents.Agent) {
	if agent.WindowID == "" {
		m.dismissAgent(agent)

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

	return m, killAgentWindowCmd(m.tmux, target)
}

func killAgentWindowCmd(ops tmuxOps, agent agents.Agent) tea.Cmd {
	return func() tea.Msg {
		if ops == nil || !ops.Available() {
			return agentKilledMsg{agent: agent, err: tmuxctl.ErrNotAvailable}
		}

		return agentKilledMsg{agent: agent, err: ops.KillWindow(tmuxctl.Target(agent.WindowID))}
	}
}

// applyAgentKilled reports a failed kill, or dismisses the agent and refreshes
// the pane listing so it leaves the list right away instead of lingering as
// stopped.
func (m *model) applyAgentKilled(msg agentKilledMsg) tea.Cmd {
	if msg.err != nil {
		m.tmuxHint = fmt.Sprintf("kill %s: %v", agentName(msg.agent), msg.err)

		return nil
	}

	m.dismissAgent(msg.agent)

	return m.requestPanes()
}

// dismissAgent marks agent seen and hides it from the agent list until it
// gets a tmux window or new activity.
func (m *model) dismissAgent(agent agents.Agent) {
	m.markAgentSeen(agent)
	if m.dismissedAgents == nil {
		m.dismissedAgents = map[string]time.Time{}
	}

	m.dismissedAgents[agent.ID] = time.Now()
	m.refreshAgents()
}

func (m model) agentKillPromptLine() string {
	return wtHintStyle.Render(fmt.Sprintf(
		"kill agent [%s]? closes its tmux window · y to confirm · any other key cancels",
		agentName(m.killAgentTarget)))
}
