// Package agents projects the Repos rows, workspace statuses, live sessions
// and tmux panes into one attention-ordered list of agents. It consumes the
// outputs of settings.Merge and workspace.MergeStatus rather than re-joining
// the snapshot, and must not import bubbletea or internal/ui.
package agents

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

type Kind string

const (
	KindWorktree  Kind = "worktree"
	KindWorkspace Kind = "workspace"
	KindStray     Kind = "stray"
)

// Group buckets agents by urgency; lower values sort first.
type Group int

const (
	GroupNeedsYou Group = iota
	GroupDone
	GroupWorking
	GroupIdle
	GroupStopped
)

// Agent is one session the user may want to look at or jump to. ID is the
// canonical session directory and identifies the agent everywhere.
type Agent struct {
	ID   string
	Kind Kind

	Repo   string
	Branch string

	Workspace   string
	SessionName string

	Harness   string
	Provider  string
	SessionID string
	Title     string

	State        settings.RowState
	Attention    state.Attention
	LastActivity time.Time
	Group        Group

	WindowID     string
	PaneID       string
	MuxSessionID string
	// SessionOwned is true when the agent's tmux session holds no other
	// @cog_dir window, so a client sitting in that session counts as being at
	// this agent even when its active window is a sibling.
	SessionOwned bool
}

type Input struct {
	Rows          []settings.Row
	Workspaces    []workspace.WorkspaceStatus
	Live          []state.SessionView
	Panes         []mux.Pane
	WorkspaceRoot string
	Now           time.Time
	RecentWindow  time.Duration
	// Dismissed maps agent ID to when the user dismissed it. A dismissed agent
	// stays hidden until it has a tmux window or activity after that time.
	Dismissed map[string]time.Time
}

// Build returns every agent worth surfacing, ordered by urgency.
func Build(in Input) []Agent {
	panes := indexPanes(in.Panes)
	out, managedDirs := managedAgents(in, panes)

	for _, a := range strayAgents(in, managedDirs) {
		if panes.attach(&a, panes.byCwd[a.ID]) {
			out = append(out, a)
		}
	}

	sortAgents(out)
	return out
}

// AgentAt returns the agent the tmux client is looking at: the agent on that
// window, else the session-owned agent of that tmux session.
func AgentAt(list []Agent, windowID, sessionID string) (string, bool) {
	if windowID != "" {
		for _, a := range list {
			if a.WindowID == windowID {
				return a.ID, true
			}
		}
	}
	if sessionID == "" {
		return "", false
	}
	for _, a := range list {
		if a.SessionOwned && a.MuxSessionID == sessionID {
			return a.ID, true
		}
	}
	return "", false
}

// Next returns the first agent needing attention after currentID, wrapping
// around the list. It never returns currentID itself.
func Next(list []Agent, currentID string) (Agent, bool) {
	current := indexOf(list, currentID)
	if current < 0 || !actionable(list[current]) {
		for _, a := range list {
			if actionable(a) {
				return a, true
			}
		}
		return Agent{}, false
	}

	for offset := 1; offset < len(list); offset++ {
		candidate := list[(current+offset)%len(list)]
		if actionable(candidate) {
			return candidate, true
		}
	}
	return Agent{}, false
}

type GroupCounts struct {
	ByGroup  map[Group]int
	NeedsYou map[state.Attention]int
}

func Counts(list []Agent) GroupCounts {
	counts := GroupCounts{
		ByGroup:  map[Group]int{},
		NeedsYou: map[state.Attention]int{},
	}
	for _, a := range list {
		counts.ByGroup[a.Group]++
		if a.Group == GroupNeedsYou {
			counts.NeedsYou[a.Attention]++
		}
	}
	return counts
}

func actionable(a Agent) bool {
	return a.Group == GroupNeedsYou || a.Group == GroupDone
}

func indexOf(list []Agent, id string) int {
	if id == "" {
		return -1
	}
	for i, a := range list {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func managedAgents(in Input, panes paneIndex) (agents []Agent, managedDirs map[string]bool) {
	managedDirs = map[string]bool{}
	liveDirs := liveSessionDirs(in.Live)
	admit := func(a Agent) {
		panes.attach(&a, panes.byCogDir[a.ID])
		stopUnlessInFlight(&a, liveDirs)
		if keep(&a, in) {
			agents = append(agents, a)
		}
	}

	for _, row := range in.Rows {
		dir := canonical(row.Worktree)
		managedDirs[dir] = true
		if row.SessionID == "" && row.LastActivity.IsZero() {
			continue
		}
		admit(Agent{
			ID:           dir,
			Kind:         KindWorktree,
			Repo:         row.Repo,
			Branch:       row.Branch,
			Harness:      row.Harness,
			Provider:     row.Provider,
			SessionID:    row.SessionID,
			Title:        row.Title,
			State:        row.State,
			Attention:    row.Attention,
			LastActivity: row.LastActivity,
		})
	}

	for _, ws := range in.Workspaces {
		for _, sess := range ws.Sessions {
			if sess.Session.Dir == "" {
				continue
			}
			dir := canonical(sess.Session.Dir)
			managedDirs[dir] = true
			admit(Agent{
				ID:           dir,
				Kind:         KindWorkspace,
				Branch:       sess.Session.Branch,
				Workspace:    ws.Workspace.Name,
				SessionName:  sess.Session.Name,
				Harness:      sess.Session.Harness,
				Provider:     sess.Provider,
				SessionID:    sess.SessionID,
				Title:        sess.Title,
				State:        sess.State,
				Attention:    sess.Attention,
				LastActivity: sess.LastActivity,
			})
		}
	}
	return agents, managedDirs
}

func liveSessionDirs(views []state.SessionView) map[string]bool {
	dirs := map[string]bool{}
	for _, sv := range views {
		if sv.Source == state.SourceLive && sv.Directory != "" {
			dirs[canonical(sv.Directory)] = true
		}
	}

	return dirs
}

// stopUnlessInFlight stops a "running" agent that has neither a live session
// nor a tmux window: merge reports running for recent-history views too.
// Agents blocked on the user keep their badge until seen, as the Repos and
// Workspaces tabs show them; a finished agent with nothing left running is
// over, so it stops and ages out like any other.
func stopUnlessInFlight(a *Agent, liveDirs map[string]bool) {
	if a.State != settings.StateRunning || liveDirs[a.ID] || a.WindowID != "" || blockedOnUser(a.Attention) {
		return
	}
	a.State = settings.StateStopped
}

func blockedOnUser(attn state.Attention) bool {
	switch attn {
	case state.AttnPermissionPending, state.AttnQuestionPending, state.AttnErrored:
		return true
	}

	return false
}

func dismissed(a Agent, in Input) bool {
	at, ok := in.Dismissed[a.ID]

	return ok && a.WindowID == "" && !a.LastActivity.After(at)
}

func strayAgents(in Input, managedDirs map[string]bool) []Agent {
	root := ""
	if in.WorkspaceRoot != "" {
		root = canonical(in.WorkspaceRoot)
	}

	newest := map[string]state.SessionView{}
	for _, sv := range in.Live {
		if sv.Source != state.SourceLive || sv.Directory == "" {
			continue
		}
		dir := canonical(sv.Directory)
		if managedDirs[dir] || settings.PathUnderRoot(root, dir) {
			continue
		}
		if cur, ok := newest[dir]; !ok || sv.LastActivity.After(cur.LastActivity) {
			newest[dir] = sv
		}
	}

	var out []Agent
	for dir, sv := range newest {
		a := Agent{
			ID:           dir,
			Kind:         KindStray,
			Harness:      harnessOf(sv),
			Provider:     string(sv.Provider),
			SessionID:    sv.SessionID,
			Title:        sv.Title,
			State:        settings.StateRunning,
			Attention:    sv.Attention,
			LastActivity: sv.LastActivity,
		}
		a.Group = groupOf(a)
		out = append(out, a)
	}
	return out
}

func harnessOf(sv state.SessionView) string {
	if sv.Provider == "" {
		return "opencode"
	}
	return string(sv.Provider)
}

// keep assigns the agent's group and reports whether it should be surfaced.
func keep(a *Agent, in Input) bool {
	if dismissed(*a, in) {
		return false
	}

	switch a.State {
	case settings.StateRunning, settings.StateUnknown:
	case settings.StateStopped:
		if in.Now.Sub(a.LastActivity) > in.RecentWindow {
			return false
		}
	default:
		return false
	}
	a.Group = groupOf(*a)
	return true
}

func groupOf(a Agent) Group {
	if a.State == settings.StateStopped {
		return GroupStopped
	}
	if a.State != settings.StateRunning {
		return GroupIdle
	}
	switch a.Attention {
	case state.AttnPermissionPending, state.AttnQuestionPending, state.AttnErrored:
		return GroupNeedsYou
	case state.AttnFinished:
		return GroupDone
	case state.AttnActive:
		return GroupWorking
	default:
		return GroupIdle
	}
}

func sortAgents(list []Agent) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Group == GroupNeedsYou && a.Attention.Rank() != b.Attention.Rank() {
			return a.Attention.Rank() < b.Attention.Rank()
		}
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.Before(b.LastActivity)
		}
		return a.ID < b.ID
	})
}

type paneIndex struct {
	byCogDir map[string][]mux.Pane
	byCwd    map[string][]mux.Pane
	// taggedWindows maps tmux session id to the set of its @cog_dir windows.
	taggedWindows map[string]map[string]bool
}

func indexPanes(panes []mux.Pane) paneIndex {
	ix := paneIndex{
		byCogDir:      map[string][]mux.Pane{},
		byCwd:         map[string][]mux.Pane{},
		taggedWindows: map[string]map[string]bool{},
	}
	for _, p := range panes {
		if p.CogDir != "" {
			dir := canonical(p.CogDir)
			ix.byCogDir[dir] = append(ix.byCogDir[dir], p)
			if ix.taggedWindows[p.SessionID] == nil {
				ix.taggedWindows[p.SessionID] = map[string]bool{}
			}
			ix.taggedWindows[p.SessionID][p.WindowID] = true
		}
		if p.Cwd != "" {
			cwd := canonical(p.Cwd)
			ix.byCwd[cwd] = append(ix.byCwd[cwd], p)
		}
	}
	return ix
}

// attach joins the best of candidates onto a and reports whether one existed.
func (ix paneIndex) attach(a *Agent, candidates []mux.Pane) bool {
	p, ok := pickPane(candidates, a.Harness)
	if !ok {
		return false
	}
	a.WindowID = p.WindowID
	a.PaneID = p.PaneID
	a.MuxSessionID = p.SessionID
	a.SessionOwned = ix.ownsSession(p)
	return true
}

func (ix paneIndex) ownsSession(p mux.Pane) bool {
	for windowID := range ix.taggedWindows[p.SessionID] {
		if windowID != p.WindowID {
			return false
		}
	}
	return true
}

// pickPane prefers a pane running the harness, then the lowest pane id.
func pickPane(candidates []mux.Pane, harnessName string) (mux.Pane, bool) {
	var best mux.Pane
	found := false
	for _, p := range candidates {
		if !found || betterPane(p, best, harnessName) {
			best, found = p, true
		}
	}
	return best, found
}

func betterPane(p, best mux.Pane, harnessName string) bool {
	pRuns, bestRuns := runsHarness(p, harnessName), runsHarness(best, harnessName)
	if pRuns != bestRuns {
		return pRuns
	}
	return paneNumber(p.PaneID) < paneNumber(best.PaneID)
}

func runsHarness(p mux.Pane, harnessName string) bool {
	if harnessName == "" {
		return false
	}
	return strings.Contains(strings.ToLower(p.Command), strings.ToLower(harnessName))
}

// paneNumber orders tmux ids like "%12" numerically; unparseable ids sort last.
func paneNumber(paneID string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(paneID, "%"))
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return n
}

func canonical(path string) string {
	c, err := pathnorm.Canonical(path)
	if err != nil {
		return path
	}
	return c
}
