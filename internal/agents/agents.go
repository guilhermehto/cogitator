// Package agents projects the Repos rows, workspace statuses, live sessions
// and tmux panes into one attention-ordered list of agents. It consumes the
// outputs of settings.Merge and workspace.MergeStatus rather than re-joining
// the snapshot, and must not import bubbletea or internal/ui.
package agents

import (
	"cmp"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
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

// Agent is one session the user may want to look at or jump to. ID identifies
// the agent everywhere: the canonical session directory, suffixed with the
// session id when several live sessions share that directory.
type Agent struct {
	ID   string
	Dir  string
	Kind Kind

	Repo   string
	Branch string

	Workspace   string
	SessionName string

	Harness   string
	Provider  string
	SessionID string
	Title     string
	// Tab tells the agent apart from its neighbours: the name the user gave
	// its tab, else, for siblings, the session title or the tab number.
	Tab string

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

// Sibling reports whether a shares its directory with other live sessions, so
// only its own pane, not the directory's window, leads to it.
func (a Agent) Sibling() bool { return a.ID != a.Dir }

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
	live := liveByDir(in.Live)
	out, managedDirs := managedAgents(in, panes, live)

	for _, base := range strayAgents(in, live, managedDirs) {
		for _, a := range panes.sessionAgents(base, live[base.Dir], panes.byCwd[base.Dir]) {
			if a.WindowID == "" {
				continue
			}
			a.Group = groupOf(a)
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

func managedAgents(in Input, panes paneIndex, live map[string][]state.SessionView) (agents []Agent, managedDirs map[string]bool) {
	managedDirs = map[string]bool{}
	admit := func(base Agent) {
		for _, a := range panes.sessionAgents(base, live[base.Dir], panes.byCogDir[base.Dir]) {
			stopUnlessInFlight(&a, live)
			if keep(&a, in) {
				agents = append(agents, a)
			}
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
			Dir:          dir,
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
				Dir:          dir,
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

// liveByDir groups live sessions by canonical directory, oldest first so
// sessions sharing a directory keep a stable order.
func liveByDir(views []state.SessionView) map[string][]state.SessionView {
	dirs := map[string][]state.SessionView{}
	for _, sv := range views {
		if sv.Source == state.SourceLive && sv.Directory != "" {
			dir := canonical(sv.Directory)
			dirs[dir] = append(dirs[dir], sv)
		}
	}

	for _, list := range dirs {
		sort.Slice(list, func(i, j int) bool {
			if !list[i].Created.Equal(list[j].Created) {
				return list[i].Created.Before(list[j].Created)
			}
			return list[i].SessionID < list[j].SessionID
		})
	}
	return dirs
}

// stopUnlessInFlight stops a "running" agent that has neither a live session
// nor a tmux window: merge reports running for recent-history views too.
// Agents blocked on the user keep their badge until seen, as the Repos and
// Workspaces tabs show them; a finished agent with nothing left running is
// over, so it stops and ages out like any other.
func stopUnlessInFlight(a *Agent, live map[string][]state.SessionView) {
	if a.State != settings.StateRunning || len(live[a.Dir]) > 0 || a.WindowID != "" || blockedOnUser(a.Attention) {
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

// strayAgents returns one agent per directory with live sessions that no
// Repos row or workspace session manages, carrying its newest session.
func strayAgents(in Input, live map[string][]state.SessionView, managedDirs map[string]bool) []Agent {
	root := ""
	if in.WorkspaceRoot != "" {
		root = canonical(in.WorkspaceRoot)
	}

	var out []Agent
	for dir, views := range live {
		if managedDirs[dir] || settings.PathUnderRoot(root, dir) {
			continue
		}

		newest := views[0]
		for _, sv := range views[1:] {
			if sv.LastActivity.After(newest.LastActivity) {
				newest = sv
			}
		}
		out = append(out, sessionAgent(Agent{ID: dir, Dir: dir, Kind: KindStray}, newest))
	}
	return out
}

// sessionAgent is base carrying sv's own identity and status.
func sessionAgent(base Agent, sv state.SessionView) Agent {
	a := base
	a.Harness = harnessOf(sv)
	a.Provider = string(sv.Provider)
	a.SessionID = sv.SessionID
	a.Title = sv.Title
	a.State = settings.StateRunning
	a.Attention = sv.Attention
	a.LastActivity = sv.LastActivity
	return a
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
	byID     map[string]mux.Pane
	byCogDir map[string][]mux.Pane
	byCwd    map[string][]mux.Pane
	// taggedWindows maps tmux session id to the set of its @cog_dir windows.
	taggedWindows map[string]map[string]bool
}

func indexPanes(panes []mux.Pane) paneIndex {
	ix := paneIndex{
		byID:          map[string]mux.Pane{},
		byCogDir:      map[string][]mux.Pane{},
		byCwd:         map[string][]mux.Pane{},
		taggedWindows: map[string]map[string]bool{},
	}
	for _, p := range panes {
		ix.byID[p.PaneID] = p
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

// sessionAgents returns base joined onto the pane its session's hook
// reported, else the best of candidates; when several live sessions share
// its directory, one agent per session. Each sibling takes its reported pane,
// else the best candidate no sibling holds yet, so every pane stays reachable
// even before a hook lands. The reported pane wins because a pane's cwd is its
// shell's, which need not be where the agent runs.
func (ix paneIndex) sessionAgents(base Agent, views []state.SessionView, candidates []mux.Pane) []Agent {
	if len(views) < 2 {
		if len(views) == 0 || !ix.joinReported(&base, views[0].PaneID) {
			ix.attach(&base, candidates)
		}
		return []Agent{base}
	}

	out := make([]Agent, len(views))
	taken := map[string]bool{}
	for i, sv := range views {
		out[i] = sessionAgent(base, sv)
		out[i].ID = base.Dir + "#" + sv.SessionID
		if ix.joinReported(&out[i], sv.PaneID) {
			taken[sv.PaneID] = true
		}
	}

	for i := range out {
		if out[i].PaneID != "" {
			continue
		}
		free := untaken(candidates, taken)
		if len(free) == 0 {
			free = candidates
		}
		if ix.attach(&out[i], free) {
			taken[out[i].PaneID] = true
		}
	}

	for i := range out {
		out[i].Tab = cmp.Or(out[i].Tab, out[i].Title, ix.tabNumber(out[i].PaneID))
	}
	return out
}

// joinReported joins a onto the pane a hook reported, if it still exists.
func (ix paneIndex) joinReported(a *Agent, paneID string) bool {
	p, ok := ix.byID[paneID]
	if !ok || paneID == "" {
		return false
	}
	ix.join(a, p)
	return true
}

// tabNumber names the tab holding paneID the way the multiplexer's tab bar
// numbers it, or "" when the agent has no pane.
func (ix paneIndex) tabNumber(paneID string) string {
	p, ok := ix.byID[paneID]
	if !ok {
		return ""
	}
	return "tab " + strconv.Itoa(p.TabNumber)
}

func untaken(candidates []mux.Pane, taken map[string]bool) []mux.Pane {
	var free []mux.Pane
	for _, p := range candidates {
		if !taken[p.PaneID] {
			free = append(free, p)
		}
	}
	return free
}

// attach joins the best of candidates onto a and reports whether one existed.
func (ix paneIndex) attach(a *Agent, candidates []mux.Pane) bool {
	p, ok := pickPane(candidates, a.Harness)
	if !ok {
		return false
	}
	ix.join(a, p)
	return true
}

func (ix paneIndex) join(a *Agent, p mux.Pane) {
	a.WindowID = p.WindowID
	a.PaneID = p.PaneID
	a.MuxSessionID = p.SessionID
	a.SessionOwned = ix.ownsSession(p)
	if !launchedTabName(*a, p.TabName) {
		a.Tab = p.TabName
	}
}

// launchedTabName reports whether name is the one cogitator gives a tab it
// opens for a, which the row already shows as repo and branch, workspace and
// session, or directory.
func launchedTabName(a Agent, name string) bool {
	switch name {
	case filepath.Base(a.Dir), filepath.Base(a.Repo) + "/" + a.Branch, a.Workspace + "/" + a.SessionName:
		return true
	}
	return false
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
	names := harnessNames(harnessName)

	var best mux.Pane
	found := false
	for _, p := range candidates {
		if !found || betterPane(p, best, names) {
			best, found = p, true
		}
	}
	return best, found
}

func betterPane(p, best mux.Pane, names []string) bool {
	pRuns, bestRuns := runsHarness(p, names), runsHarness(best, names)
	if pRuns != bestRuns {
		return pRuns
	}
	return paneNumber(p.PaneID) < paneNumber(best.PaneID)
}

func runsHarness(p mux.Pane, names []string) bool {
	command := strings.ToLower(p.Command)
	for _, name := range names {
		if strings.Contains(command, name) {
			return true
		}
	}
	return false
}

// harnessNames are the lowercased kind and launch binary of harnessName: a
// claude-code pane runs "claude".
func harnessNames(harnessName string) []string {
	if harnessName == "" {
		return nil
	}

	names := []string{strings.ToLower(harnessName)}
	h, err := harness.DefaultRegistry.Get(harness.Kind(harnessName))
	if err != nil {
		return names
	}
	if argv := h.LaunchArgv("", ""); len(argv) > 0 {
		names = append(names, strings.ToLower(filepath.Base(argv[0])))
	}
	return names
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
