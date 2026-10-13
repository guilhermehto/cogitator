package ui

import (
	"context"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/state"
)

const feedActionQueueSize = 16

// feedActionMsg carries a focus/seen request received over the feed socket.
type feedActionMsg agentfeed.Action

// panesMsg carries the latest tmux pane listing; ok is false when it failed.
type panesMsg struct {
	panes []mux.Pane
	ok    bool
}

// startAgentFeed binds the feed socket. A bind failure is non-fatal: the TUI
// runs without the feed and holder is nil.
func startAgentFeed(ctx context.Context, sockPath string, logger *slog.Logger) (holder *agentfeed.Holder, actions <-chan agentfeed.Action, cleanup func()) {
	queue := make(chan agentfeed.Action, feedActionQueueSize)
	feedHolder := &agentfeed.Holder{}

	stop, err := agentfeed.Serve(ctx, sockPath, feedHolder, queue, logger)
	if err != nil {
		logger.Warn("agent feed unavailable; pick/next/status will not work", "err", err)

		return nil, nil, func() {}
	}

	return feedHolder, queue, stop
}

// attachFeed wires the model to a running feed. listPanes is the backend seam
// for the pane refresh the agent projection joins against.
func (m *model) attachFeed(holder *agentfeed.Holder, actions <-chan agentfeed.Action, listPanes func() ([]mux.Pane, error)) {
	m.feed = holder
	m.feedActions = actions
	m.listPanes = listPanes
	m.panesLoading = true
}

func waitFeedAction(ch <-chan agentfeed.Action) tea.Cmd {
	if ch == nil {
		return nil
	}

	return func() tea.Msg {
		action, ok := <-ch
		if !ok {
			return nil
		}

		return feedActionMsg(action)
	}
}

func listPanesCmd(listPanes func() ([]mux.Pane, error)) tea.Cmd {
	if listPanes == nil {
		return nil
	}

	return func() tea.Msg {
		panes, err := listPanes()
		if err != nil {
			return panesMsg{}
		}

		return panesMsg{panes: panes, ok: true}
	}
}

// requestPanes starts a pane refresh, coalescing with one already in flight
// the same way rowsBuilding/wsBuilding do.
func (m *model) requestPanes() tea.Cmd {
	if m.listPanes == nil {
		return nil
	}

	if m.panesLoading {
		m.panesDirty = true

		return nil
	}

	m.panesLoading = true

	return listPanesCmd(m.listPanes)
}

func (m *model) applyPanes(msg panesMsg) tea.Cmd {
	m.panesLoading = false
	if msg.ok {
		m.panes = msg.panes
		m.refreshAgents()
	}

	if !m.panesDirty {
		return nil
	}

	m.panesDirty = false

	return m.requestPanes()
}

// refreshAgents recomputes the unified agent list and publishes it to the
// feed. It is pure and cheap, so it runs inline whenever an input changes.
func (m *model) refreshAgents() {
	if m.feed == nil {
		return
	}

	var live []state.SessionView
	for _, sv := range m.snap.Sessions {
		if !shouldHideSubagent(sv) && sv.ParentID == "" {
			live = append(live, sv)
		}
	}

	m.agents = agents.Build(agents.Input{
		Rows:          m.workspaceRows,
		Workspaces:    m.wsStatuses,
		Live:          live,
		Panes:         m.panes,
		WorkspaceRoot: m.workspaceRoot,
		Now:           time.Now(),
		RecentWindow:  m.cfg.RecentWindow,
		Dismissed:     m.dismissedAgents,
	})
	m.feed.Set(m.agents)
}

func (m model) agentByID(id string) (agents.Agent, bool) {
	for _, a := range m.agents {
		if a.ID == id {
			return a, true
		}
	}

	return agents.Agent{}, false
}

func (m *model) handleFeedAction(action agentfeed.Action) tea.Cmd {
	agent, ok := m.agentByID(action.AgentID)
	if !ok {
		return nil
	}

	switch action.Kind {
	case agentfeed.ActionSeen:
		m.markAgentSeen(agent)

		return nil
	case agentfeed.ActionFocus:
		return m.focusAgent(agent, action.Client)
	default:
		return nil
	}
}

func (m model) markAgentSeen(agent agents.Agent) {
	if m.viewMarker == nil || agent.SessionID == "" {
		return
	}

	provider := fallbackProvider(agent.Provider, agent.Harness)
	m.viewMarker.MarkViewed(harness.Kind(provider), "", agent.SessionID)
}

func (m *model) focusAgent(agent agents.Agent, client string) tea.Cmd {
	if focusesPane(agent) {
		m.recordSessionSwitch(agent.ID)

		return focusPaneCmd(m.mux, agent, client)
	}

	target, ok := m.managedLaunchTarget(agent)
	if !ok {
		m.tmuxHint = "focus: agent is no longer tracked"

		return nil
	}

	m.recordSessionSwitch(target.dir)

	return launchForClientCmd(m.mux, target, m.harnOp, m.launchMode, resolvedDefaultHarness(m.harnOp), client)
}

// focusesPane reports whether focusing a selects its own pane rather than
// launching its directory: strays have no launch target, and a sibling shares
// its directory's window with other sessions.
func focusesPane(a agents.Agent) bool {
	return a.Kind == agents.KindStray || (a.Sibling() && a.PaneID != "")
}

func (m model) managedLaunchTarget(agent agents.Agent) (launchTarget, bool) {
	for _, row := range m.workspaceRows {
		if sameDir(row.Worktree, agent.Dir) {
			return rowLaunchTarget(row), true
		}
	}

	for _, ws := range m.wsStatuses {
		for _, sess := range ws.Sessions {
			if sameDir(sess.Session.Dir, agent.Dir) {
				return wsSessionLaunchTarget(ws.Workspace.Name, sess), true
			}
		}
	}

	return launchTarget{}, false
}

func sameDir(dir, canonicalDir string) bool {
	if dir == "" {
		return false
	}

	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		canonical = dir
	}

	return canonical == canonicalDir
}

// focusPaneCmd selects the agent's own pane.
func focusPaneCmd(ops mux.Backend, agent agents.Agent, client string) tea.Cmd {
	return func() tea.Msg {
		res := launchResultMsg{
			dir:       agent.Dir,
			client:    client,
			provider:  fallbackProvider(agent.Provider, agent.Harness),
			sessionID: agent.SessionID,
		}
		if ops == nil || !ops.Available() {
			res.err = mux.ErrNotAvailable

			return res
		}

		res.err = ops.SelectPane(agent.PaneID, client)

		return res
	}
}

func displayMessageCmd(ops mux.Backend, client, text string) tea.Cmd {
	if ops == nil {
		return nil
	}

	return func() tea.Msg {
		_ = ops.Message(client, text)

		return nil
	}
}
