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

	return m, m.syncAgentPreview()
}

// syncAgentPreview points the preview at the Agents cursor while the tab is
// shown wide enough to hold it, and stops it otherwise.
func (m *model) syncAgentPreview() tea.Cmd {
	if m.view != viewAgents || m.badgeRunner == nil || m.paneWidth()-2 < agentSplitMinWidth {
		m.agentPreview.Hide()

		return nil
	}

	m.agentsList.SetAgents(m.agents)
	current, ok := m.agentsList.Current()

	return m.agentPreview.Follow(current, ok)
}

func (m *model) handleAgentPreviewMsg(msg tea.Msg) tea.Cmd {
	m.agentsList.SetAgents(m.agents)
	current, ok := m.agentsList.Current()

	switch msg := msg.(type) {
	case previewDebounceMsg:
		return m.agentPreview.Debounced(msg, current, ok)
	case previewRefreshMsg:
		if m.view != viewAgents {
			m.agentPreview.Hide()

			return nil
		}

		return m.agentPreview.Refresh(msg, current, ok)
	case previewCaptureMsg:
		return m.agentPreview.Captured(msg, current, ok)
	}

	return nil
}

// renderAgentsView draws the grouped agent list beside the cursor agent's
// preview, reserving the last line for a transient hint (e.g. a failed jump).
func (m model) renderAgentsView(width, height int) string {
	now := m.tickNow
	if now.IsZero() {
		now = time.Now()
	}

	list := m.agentsList
	list.SetAgents(m.agents)

	// The pane's horizontal padding takes two columns, as for Repos rows.
	width = max(0, width-2)
	bodyHeight := max(1, height-1)
	if m.tmuxHint != "" {
		bodyHeight = max(1, bodyHeight-1)
	}

	preview := m.agentPreview
	body := list.View(now, width, bodyHeight)
	if m.badgeRunner != nil {
		body = renderAgentSplit(list, preview, now, width, bodyHeight)
	}

	view := headerStyle.Render("Agents") + "\n" + body
	if m.tmuxHint != "" {
		view += "\n" + wtHintStyle.Render(singleLineCell(m.tmuxHint))
	}

	return view
}

func (m model) agentKeyHints() []keyHint {
	if m.agentsList.Filtering() {
		return []keyHint{{"enter", "jump"}, {"esc", "clear"}}
	}

	return []keyHint{{"enter", "jump"}, {"x", "mark seen"}, {"/", "filter"}}
}
