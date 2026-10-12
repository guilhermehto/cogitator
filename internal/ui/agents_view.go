package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// updateAgentsView routes a key to the Agents tab's list. Global keys (tab,
// ctrl+P, ?, S, and q/esc/ctrl+c outside the filter) are handled before this.
func (m model) updateAgentsView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.tmuxHint = ""
	m.agentsList.SetAgents(m.agents)

	result := m.agentsList.HandleKey(msg)

	switch result.intent {
	case agentIntentQuit:
		return m, tea.Quit
	case agentIntentSeen:
		if agent, ok := m.agentByID(result.agentID); ok {
			m.markAgentSeen(agent)
		}
	case agentIntentJump:
		if agent, ok := m.agentByID(result.agentID); ok {
			return m, m.focusAgent(agent, "")
		}
	}

	return m, nil
}

// renderAgentsView draws the grouped agent list, reserving the last line for
// a transient hint (e.g. a failed jump) when one is set.
func (m model) renderAgentsView(width, height int) string {
	now := m.tickNow
	if now.IsZero() {
		now = time.Now()
	}

	list := m.agentsList
	list.SetAgents(m.agents)

	if m.tmuxHint == "" {
		return list.View(now, width, height)
	}

	return list.View(now, width, max(1, height-1)) + "\n" + wtHintStyle.Render(singleLineCell(m.tmuxHint))
}

func (m model) agentKeyHints() []keyHint {
	if m.agentsList.Filtering() {
		return []keyHint{{"enter", "jump"}, {"esc", "clear"}}
	}

	return []keyHint{{"enter", "jump"}, {"x", "mark seen"}, {"/", "filter"}}
}
