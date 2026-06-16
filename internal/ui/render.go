package ui

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

var (
	titleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Padding(0, 1)
	headerStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	dimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	agentStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Italic(true)
	recentStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	paneStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	paneFocusedStyle = paneStyle.BorderForeground(lipgloss.Color("63"))

	attnPermStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	attnQuestionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
	attnErrStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	attnInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	attnActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	attnFinishedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Bold(true)

	statusBusyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))

	// providerStyle renders the provider badge (e.g. "[opencode]") in a muted
	// colour so it is visible but does not compete with the session title.
	providerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)

	// taskActiveStyle highlights a running Taskwarrior task. Bold + green so
	// the running row is distinguishable from the cursor (reverse-video) and
	// from the priority glyph palette.
	taskActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true)

	cellLineBreakReplacer = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")
)

var agentPalette = []string{
	"33", "39", "45", "51", "75", "81",
	"99", "105", "111", "117", "135", "141",
	"147", "153", "165", "171", "177", "183",
	"203", "207", "213", "219",
}

func agentColor(name string) lipgloss.Style {
	if name == "" {
		return agentStyle
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	idx := h.Sum32() % uint32(len(agentPalette))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(agentPalette[idx])).Italic(true)
}

const (
	glyphActive     = "\U000f0765" // 󰝥 filled circle
	glyphInactive   = "\U000f0766" // 󰝦 hollow circle
	glyphRecent     = "\U000f02da" // 󰋚
	glyphQuestion   = "\U000f0625" // 󰘥
	glyphPermission = "\U000f033e" // 󰌾
	glyphError      = "\U000f0026" // 󰀦
	glyphFinished   = "\U000f012c" // 󰄬 check — agent finished, awaiting your return
	// glyphTaskActive marks a running Taskwarrior task in the ST column.
	// When active, it replaces the priority glyph for that row.
	glyphTaskActive = "\U000f040a" // 󰐊 play

	// Workspace / worktree row glyphs.
	glyphWtStopped   = "\U000f0766" // 󰝦 same as glyphInactive — agent stopped
	glyphWtMissing   = "\U000f0e7a" // 󰹺 directory absent from disk
	glyphWtUnknown   = "?"          // harness has no LiveStatus, tmux window present
)

var (
	// wtStoppedStyle dims a stopped worktree row.
	wtStoppedStyle = dimStyle
	// wtMissingStyle renders a missing worktree row (directory absent).
	wtMissingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Italic(true)
	// wtUnknownStyle renders an unknown worktree row.
	wtUnknownStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	// wtCursorStyle highlights plain-text cursor rows (the repo finder and the
	// harness chooser) with reverse video. Worktree rows use the colour-
	// preserving band in highlightSelectedRow instead, so reverse video is
	// never applied to rows that carry per-cell foreground colours.
	wtCursorStyle = lipgloss.NewStyle().Reverse(true)
	// wtSelectedBg is the background painted across the highlighted (cursor)
	// worktree row. A background — unlike reverse video — leaves each cell's
	// foreground colour intact, so the selected row keeps its status/branch/
	// path styling while still reading as selected. See highlightSelectedRow.
	wtSelectedBg = lipgloss.Color("237")
	// wtRepoStyle renders the repo group header.
	wtRepoStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	// wtPathStyle renders the faded-italic path annotation shown next to a repo
	// header (the repo path) and next to each session row (the worktree/branch).
	wtPathStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
	// wtHintStyle renders the transient tmux hint line.
	wtHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Italic(true)
	// wtBaseStyle marks the repo's root/default worktree — the base off which
	// new worktrees ('n') are created. Reuses the accent colour (63) used by the
	// repo header so the tag reads as the repo's primary checkout.
	wtBaseStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
)

// taskPriorityGlyph maps Taskwarrior priority codes to display glyphs.
// No entry means the cell is left blank (normal priority).
var taskPriorityGlyph = map[string]string{
	"H": "\U000f0071", // 󰁱 high
	"M": "●",          // medium
	"L": "·",          // low
}

const (
	colStateW    = 5
	colStatusW   = 10
	colActivityW = 8
	colGap       = 2

	// maxSessionTitleW caps the muted session-title annotation shown after the
	// branch on a worktree row. The branch leads (it is what you navigate by),
	// so the title is de-emphasised and longer titles are truncated with "…".
	maxSessionTitleW = 48
)

// Task pane column widths. DESC takes the remainder after all fixed columns
// and gaps are subtracted from the available inner width.
const (
	colTaskStateW   = 3
	colTaskIDW      = 5
	colTaskProjectW = 14
	colTaskTagsW    = 16
	colTaskDueW     = 10
)

func attnLabel(a state.Attention, source state.Source) string {
	switch a {
	case state.AttnPermissionPending:
		return attnPermStyle.Render(glyphPermission) + " "
	case state.AttnQuestionPending:
		return attnQuestionStyle.Render(glyphQuestion) + " "
	case state.AttnErrored:
		return attnErrStyle.Render(glyphError) + " "
	case state.AttnFinished:
		return attnFinishedStyle.Render(glyphFinished) + " "
	}
	if source == state.SourceRecent {
		return recentStyle.Render(glyphRecent) + " "
	}
	if a == state.AttnInactive {
		return attnInactiveStyle.Render(glyphInactive) + " "
	}
	return attnActiveStyle.Render(glyphActive) + " "
}

func formatRelative(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}

func styledStatus(s string) string {
	switch s {
	case "busy", "generating":
		return statusBusyStyle.Render(s)
	case "", "idle":
		return ""
	default:
		return dimStyle.Render(s)
	}
}

// legendLine renders the status-icon legend, optionally appending task-priority
// glyphs. If the combined line would exceed width, the task glyphs are omitted
// so the legend never wraps on narrow terminals.
func legendLine(width int, includeTasks bool) string {
	sessionParts := []string{
		dimStyle.Render("legend:"),
		attnActiveStyle.Render(glyphActive) + " " + dimStyle.Render("active"),
		attnFinishedStyle.Render(glyphFinished) + " " + dimStyle.Render("finished"),
		attnInactiveStyle.Render(glyphInactive) + " " + dimStyle.Render("inactive"),
		recentStyle.Render(glyphRecent) + " " + dimStyle.Render("recent"),
		attnQuestionStyle.Render(glyphQuestion) + " " + dimStyle.Render("question"),
		attnPermStyle.Render(glyphPermission) + " " + dimStyle.Render("permission"),
		attnErrStyle.Render(glyphError) + " " + dimStyle.Render("error"),
	}
	sessionLegend := strings.Join(sessionParts, "  ")
	if !includeTasks {
		return sessionLegend
	}

	taskParts := []string{
		taskActiveStyle.Render(glyphTaskActive) + " " + dimStyle.Render("running"),
		taskPriorityGlyph["H"] + " " + dimStyle.Render("high"),
		"● " + dimStyle.Render("medium"),
		"· " + dimStyle.Render("low"),
	}
	taskLegend := strings.Join(taskParts, " · ")

	combined := sessionLegend + "    " + taskLegend
	if width > 0 && lipgloss.Width(combined) > width {
		return sessionLegend
	}
	return combined
}

func toggleVerb(collapsed bool) string {
	if collapsed {
		return "show"
	}
	return "hide"
}

func (m model) renderAllSessions(width int, rows []state.SessionView, recentByInstance map[string]int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Sessions") + "\n")
	if len(rows) == 0 && len(recentByInstance) == 0 {
		b.WriteString(dimStyle.Render("(no live or recent sessions on discovered instances)"))
		return b.String()
	}
	b.WriteString(columnHeader(width-2) + "\n")
	now := m.snap.UpdatedAt
	if now.IsZero() {
		now = time.Now()
	}

	var live, recent []state.SessionView
	for _, sv := range rows {
		if sv.Source == state.SourceRecent {
			recent = append(recent, sv)
		} else {
			live = append(live, sv)
		}
	}

	if len(live) > 0 {
		b.WriteString(renderTree(now, live, width-2, sortLiveRows))
	}

	totalRecent := 0
	for _, n := range recentByInstance {
		totalRecent += n
	}
	if totalRecent > 0 {
		marker := "▸"
		if !m.recentCollapsed {
			marker = "▾"
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf("%s %d recent", marker, totalRecent)) + "\n")
		if !m.recentCollapsed && len(recent) > 0 {
			b.WriteString(renderTree(now, recent, width-2, sortRecentRows))
		}
	}

	return b.String()
}

func padCell(s string, cellWidth int, align lipgloss.Position) string {
	s = singleLineCell(s)
	w := lipgloss.Width(s)
	if w >= cellWidth {
		return s
	}
	pad := strings.Repeat(" ", cellWidth-w)
	if align == lipgloss.Right {
		return pad + s
	}
	return s + pad
}

// singleLineCell folds hard line breaks out of table cells. Session titles come
// from providers and may be multiline; rows must stay one terminal line tall.
func singleLineCell(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return cellLineBreakReplacer.Replace(s)
}

func columnHeader(width int) string {
	sessionW := width - colStateW - colStatusW - colActivityW - 3*colGap
	if sessionW < 1 {
		sessionW = 1
	}
	cells := []string{
		padCell(dimStyle.Render("STATE"), colStateW, lipgloss.Left),
		padCell(dimStyle.Render("SESSION"), sessionW, lipgloss.Left),
		padCell(dimStyle.Render("STATUS"), colStatusW, lipgloss.Right),
		padCell(dimStyle.Render("ACTIVITY"), colActivityW, lipgloss.Right),
	}
	return strings.Join(cells, strings.Repeat(" ", colGap))
}

func renderTree(now time.Time, rows []state.SessionView, width int, sortRows func([]state.SessionView)) string {
	byParent := map[string][]state.SessionView{}
	knownIDs := map[string]bool{}
	for _, r := range rows {
		knownIDs[r.SessionID] = true
	}
	roots := []state.SessionView{}
	for _, r := range rows {
		if r.ParentID != "" && knownIDs[r.ParentID] {
			byParent[r.ParentID] = append(byParent[r.ParentID], r)
		} else {
			roots = append(roots, r)
		}
	}

	sortRows(roots)
	var b strings.Builder
	for _, r := range roots {
		b.WriteString(formatRow(now, r, width, false) + "\n")
		kids := byParent[r.SessionID]
		sortRows(kids)
		for _, c := range kids {
			b.WriteString(formatRow(now, c, width, true) + "\n")
		}
	}
	return b.String()
}

func formatRow(now time.Time, sv state.SessionView, width int, child bool) string {
	title := sv.Title
	if title == "" {
		title = sv.Slug
	}
	if title == "" {
		title = sv.SessionID
	}
	title = trimAgentSuffix(title, sv.Agent)

	prefix := ""
	if child {
		prefix = dimStyle.Render("  ↳ ")
	}

	agentTag := ""
	if sv.Agent != "" {
		agentTag = agentColor(sv.Agent).Render("@" + sv.Agent)
	}

	providerBadge := ""
	if sv.Provider != "" {
		providerBadge = providerStyle.Render("[" + string(sv.Provider) + "]")
	}

	titleRender := title
	if sv.Source == state.SourceRecent {
		titleRender = dimStyle.Render(title)
	}
	sessionContent := prefix + titleRender
	if agentTag != "" {
		sessionContent = prefix + agentTag + " " + titleRender
	}
	if providerBadge != "" {
		sessionContent += " " + providerBadge
	}
	if !child && sv.Directory != "" {
		sessionContent += "  " + dimStyle.Render(shortenDirectory(sv.Directory))
	}

	sessionW := width - colStateW - colStatusW - colActivityW - 3*colGap
	if sessionW < 1 {
		sessionW = 1
	}
	stateCell := attnLabel(sv.Attention, sv.Source)
	if child {
		stateCell = dimStyle.Render("↳ ") + stateCell
	}
	cells := []string{
		padCell(stateCell, colStateW, lipgloss.Left),
		padCell(sessionContent, sessionW, lipgloss.Left),
		padCell(styledStatus(sv.StatusType), colStatusW, lipgloss.Right),
		padCell(dimStyle.Render(formatRelative(now, sv.LastActivity)), colActivityW, lipgloss.Right),
	}
	return strings.Join(cells, strings.Repeat(" ", colGap))
}

func trimAgentSuffix(title, agent string) string {
	if agent == "" {
		return title
	}
	trimmed := strings.TrimRight(title, " \t")
	if !strings.HasSuffix(trimmed, ")") {
		return title
	}
	depth := 0
	openIdx := -1
	for i := len(trimmed) - 1; i >= 0; i-- {
		switch trimmed[i] {
		case ')':
			depth++
		case '(':
			depth--
		}
		if depth == 0 {
			openIdx = i
			break
		}
	}
	if openIdx <= 0 {
		return title
	}
	return strings.TrimRight(trimmed[:openIdx], " \t")
}

var homeDir = func() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}()

func shortenDirectory(path string) string {
	if path == "" {
		return ""
	}
	if homeDir != "" {
		if path == homeDir {
			return "~"
		}
		if strings.HasPrefix(path, homeDir+"/") {
			return "~" + path[len(homeDir):]
		}
	}
	return path
}

// renderWorkspaceRows renders the merged worktree list grouped by repo.
// cursor is the index into rows of the currently selected row.
// now is the reference time for relative timestamps on stopped rows.
// hint is a transient one-line message shown below the rows (e.g. tmux hint).
func (m model) renderWorkspaceRows(width int, rows []workspace.Row, cursor int, now time.Time) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Sessions") + "\n")
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("(no worktrees configured)"))
		if m.tmuxHint != "" {
			b.WriteString("\n" + wtHintStyle.Render(m.tmuxHint))
		}
		if m.prompt == promptNewWorktree || m.prompt == promptFetchBranch {
			b.WriteString("\n" + m.worktreePromptLine())
		}
		if m.prompt == promptConfirmDeleteWorktree || m.prompt == promptConfirmDeleteWorktree2 {
			b.WriteString("\n" + m.worktreeDeletePromptLine())
		}
		if m.prompt == promptConfirmRemoveRepo {
			b.WriteString("\n" + m.repoRemovePromptLine())
		}
		return b.String()
	}

	// Group rows by repo so we can emit a repo header before each group.
	// We preserve the order from rows (Merge already orders by repo then worktree).
	type repoGroup struct {
		repo string
		rows []int // indices into rows
	}
	var groups []repoGroup
	repoIndex := map[string]int{} // repo path → index in groups
	for i, row := range rows {
		repo := row.Repo
		gi, ok := repoIndex[repo]
		if !ok {
			gi = len(groups)
			repoIndex[repo] = gi
			groups = append(groups, repoGroup{repo: repo})
		}
		groups[gi].rows = append(groups[gi].rows, i)
	}

	for _, g := range groups {
		// Repo header: bold base name, with the full (home-shortened) repo
		// path in faded italic next to it.
		header := wtRepoStyle.Render("  "+filepath.Base(g.repo)) +
			"  " + wtPathStyle.Render(shortenDirectory(g.repo))
		b.WriteString(header + "\n")

		for _, i := range g.rows {
			row := rows[i]
			line := formatWorktreeRow(now, row, width-2)
			if i == cursor {
				// Highlight the cursor row with a background band that keeps
				// the row's per-cell foreground colours (see
				// highlightSelectedRow). Reverse video would instead force a
				// strip of all colour first, because lipgloss emits an SGR
				// reset after every coloured cell that also clears the reverse
				// attribute.
				line = highlightSelectedRow(line)
			}
			b.WriteString(line + "\n")
		}
	}

	// Render transient hint (e.g. tmux unavailable, launch error).
	if m.tmuxHint != "" {
		b.WriteString(wtHintStyle.Render(m.tmuxHint) + "\n")
	}

	// Render branch-name prompt when the user pressed 'n' (new worktree) or 'F'
	// (fetch from origin). This must render regardless of m.twAvail — the
	// sessions pane is independent of taskwarrior. Placed after the hint so it is
	// always the last visible line.
	if m.prompt == promptNewWorktree || m.prompt == promptFetchBranch {
		b.WriteString(m.worktreePromptLine() + "\n")
	}

	// Render the worktree delete confirmation (first or second step) as the
	// last visible line so it reads as the active modal.
	if m.prompt == promptConfirmDeleteWorktree || m.prompt == promptConfirmDeleteWorktree2 {
		b.WriteString(m.worktreeDeletePromptLine() + "\n")
	}

	// Render the repo-untrack confirmation as the last visible line so it
	// reads as the active modal.
	if m.prompt == promptConfirmRemoveRepo {
		b.WriteString(m.repoRemovePromptLine() + "\n")
	}

	return b.String()
}

// highlightSelectedRow paints wtSelectedBg across an already-rendered worktree
// row while preserving its foreground colours. lipgloss emits a full SGR reset
// ("\x1b[0m") after every styled cell, and that reset also clears the
// background — so a single outer background wrap would only fill up to the
// first coloured cell. We therefore re-assert the background immediately after
// every interior reset and wrap the whole line, producing a continuous
// selection band that survives the resets. The opener and reset are taken from
// lipgloss so they honour the active colour profile; under a no-colour profile
// lipgloss emits no SGR, open is empty, and the row is returned unchanged
// (matching the old reverse path, which also vanished without colour support).
func highlightSelectedRow(line string) string {
	const marker = "\x00"
	open, reset, ok := strings.Cut(
		lipgloss.NewStyle().Background(wtSelectedBg).Render(marker), marker)
	if !ok || open == "" {
		return line
	}
	if reset != "" {
		line = strings.ReplaceAll(line, reset, reset+open)
	}
	return open + line + reset
}

// repoRemovePromptLine returns the styled confirmation line shown while the
// user is confirming that a repo should be untracked ('R'). It spells out that
// the repo is only forgotten by cogitator — nothing on disk is removed — and
// that any key other than 'y' cancels.
func (m model) repoRemovePromptLine() string {
	name := filepath.Base(m.removeRepoTarget)
	return wtHintStyle.Render(fmt.Sprintf(
		"stop tracking repo [%s]? worktrees stay on disk · y to confirm · any other key cancels", name))
}

// newWorktreeBase returns the branch of the root (main) worktree for the repo
// captured when the user pressed 'n' — i.e. the branch a new worktree will be
// based off. Returns "" when the repo or its root branch is unknown (e.g.
// detached HEAD), in which case the prompt falls back to a generic label.
func (m model) newWorktreeBase() string {
	if m.newWorktreeRepo == "" {
		return ""
	}
	for _, row := range m.workspaceRows {
		if row.IsRoot && row.Repo == m.newWorktreeRepo {
			return row.Branch
		}
	}
	return ""
}

// worktreePromptLine returns the styled prompt line shown in the sessions pane
// while the user is typing a branch name for a new worktree ('n') or a branch to
// fetch from origin ('F'). It is a shared helper so both the empty-rows and
// non-empty-rows paths in renderWorkspaceRows produce the same label.
func (m model) worktreePromptLine() string {
	if m.prompt == promptFetchBranch {
		return wtHintStyle.Render("fetch branch from origin: ") + m.input.View()
	}
	label := "new worktree branch: "
	if base := m.newWorktreeBase(); base != "" {
		label = "new worktree off " + base + ": "
	}
	return wtHintStyle.Render(label) + m.input.View()
}

// renderRepoFinder renders the embedded "add repo" fuzzy finder shown in the
// sessions pane while prompt == promptAddRepo. It draws a query line, the
// fuzzy-matched repository list (cursor row highlighted, windowed to fit the
// pane), and a status/help footer. height is the pane's inner content height,
// used to window the list so a long result set never overflows the pane.
func (m model) renderRepoFinder(width, height int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Add repo") + "\n")
	b.WriteString("add repo > " + m.input.View())

	switch {
	case m.repoFinderErr != "":
		b.WriteString("\n" + wtHintStyle.Render(m.repoFinderErr))
		return b.String()
	case m.repoFinderScanning:
		b.WriteString("\n" + dimStyle.Render("scanning "+shortenDirectory(repoFinderRoot())+" …"))
		return b.String()
	case len(m.repoFinderMatches) == 0:
		if len(m.repoFinderAll) == 0 {
			b.WriteString("\n" + dimStyle.Render("no git repositories found under "+shortenDirectory(repoFinderRoot())))
		} else {
			b.WriteString("\n" + dimStyle.Render("no match"))
		}
		return b.String()
	}

	// Window the match list around the cursor. Reserve three lines for the
	// title, query, and footer so the rendered block fits in height exactly.
	listH := height - 3
	if listH < 1 {
		listH = 1
	}
	cursor := clampIndex(m.repoFinderCursor, len(m.repoFinderMatches))
	start := 0
	if cursor >= listH {
		start = cursor - listH + 1
	}
	end := start + listH
	if end > len(m.repoFinderMatches) {
		end = len(m.repoFinderMatches)
	}

	for i := start; i < end; i++ {
		// Lines are plain text, so the reverse highlight can wrap them
		// directly (no embedded colour resets to strip, unlike worktree rows).
		line := ansi.Truncate("  "+shortenDirectory(m.repoFinderMatches[i]), width-2, "…")
		if i == cursor {
			line = wtCursorStyle.Render(line)
		}
		b.WriteString("\n" + line)
	}

	b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("%d repos · ↑↓ move · enter add · esc cancel", len(m.repoFinderMatches))))
	return b.String()
}

// worktreeDeletePromptLine returns the styled confirmation line shown while a
// worktree deletion is being confirmed. The first confirmation is a warning
// tone; the second is rendered in the error style and spells out that it is
// permanent and defaults to cancel. Both surface the branch and its merge
// status so the user can judge whether removing the worktree loses work.
func (m model) worktreeDeletePromptLine() string {
	branch := m.deleteTarget.Branch
	if branch == "" {
		branch = filepath.Base(m.deleteTarget.Worktree)
	}
	info := m.deleteMergeInfo
	if info == "" {
		info = "checking merge status…"
	}
	switch m.prompt {
	case promptConfirmDeleteWorktree:
		return wtHintStyle.Render(fmt.Sprintf(
			"delete worktree [%s] — %s? press y to continue, any other key cancels", branch, info))
	case promptConfirmDeleteWorktree2:
		return attnErrStyle.Render(fmt.Sprintf(
			"PERMANENTLY delete worktree [%s] (%s)? y to delete · any other key cancels (default: cancel)", branch, info))
	default:
		return ""
	}
}

// worktreeSessionWidth returns the width of the session/title column for a
// worktree row: the inner width minus the left status column, the right
// activity column, and the two gaps between the three columns. Clamped to 1.
func worktreeSessionWidth(width int) int {
	w := width - colStateW - colActivityW - 2*colGap
	if w < 1 {
		w = 1
	}
	return w
}

// branchLabel returns the worktree's branch — the primary, navigable identity
// for a row — falling back to the worktree directory's base name when the
// branch is unknown.
func branchLabel(row workspace.Row) string {
	if row.Branch != "" {
		return row.Branch
	}
	return filepath.Base(row.Worktree)
}

// sessionTitleSuffix renders a session title as muted, truncated trailing text
// to follow the branch on a worktree row. The branch leads, so the title is
// capped to maxSessionTitleW and de-emphasised. Empty titles add nothing.
func sessionTitleSuffix(title string) string {
	title = singleLineCell(title)
	if title == "" {
		return ""
	}
	return "  " + wtPathStyle.Render(ansi.Truncate(title, maxSessionTitleW, "…"))
}

// formatWorktreeRow renders a single workspace.Row as a fixed-width line.
//
// Columns are status | session | activity. The status column is left-most so
// the session's status reads first: running rows show the live attention badge
// (active / permission / question / error), and non-running rows show the
// worktree run-state glyph (stopped / empty / missing / unknown). This keeps
// the status in the same place the cursor highlight starts.
func formatWorktreeRow(now time.Time, row workspace.Row, width int) string {
	sessionW := worktreeSessionWidth(width)

	// Status column (left-most). Running rows carry a live attention label;
	// everything else shows the run-state glyph.
	var statusCell string
	if row.State == workspace.StateRunning {
		statusCell = attnLabel(row.Attention, state.SourceLive)
	} else {
		var glyph string
		var glyphStyle lipgloss.Style
		switch row.State {
		case workspace.StateStopped:
			glyph = glyphWtStopped
			glyphStyle = wtStoppedStyle
		case workspace.StateMissing:
			glyph = glyphWtMissing
			glyphStyle = wtMissingStyle
		case workspace.StateUnknown:
			glyph = glyphWtUnknown
			glyphStyle = wtUnknownStyle
		default:
			glyph = "·"
			glyphStyle = dimStyle
		}
		statusCell = glyphStyle.Render(glyph) + " "
	}

	// Title / description column. The branch leads each row (it is what you
	// navigate by); the session title trails as muted, truncated text.
	var titleStr string
	switch row.State {
	case workspace.StateRunning:
		// Running: branch leads, session title trails muted. When the branch is
		// unknown, fall back to the title (or worktree dir) as the lead.
		if row.Branch != "" {
			titleStr = row.Branch + sessionTitleSuffix(row.Title)
		} else if row.Title != "" {
			titleStr = row.Title
		} else {
			titleStr = filepath.Base(row.Worktree)
		}
	case workspace.StateStopped:
		// Stopped: same layout, dimmed lead.
		if row.Branch != "" {
			titleStr = wtStoppedStyle.Render(row.Branch) + sessionTitleSuffix(row.Title)
		} else {
			lead := row.Title
			if lead == "" {
				lead = filepath.Base(row.Worktree)
			}
			titleStr = wtStoppedStyle.Render(lead)
		}
	case workspace.StateMissing:
		titleStr = wtMissingStyle.Render(branchLabel(row) + " (missing)")
	case workspace.StateUnknown:
		titleStr = wtUnknownStyle.Render(branchLabel(row)) + "  " + wtPathStyle.Render("status unknown")
	default:
		titleStr = dimStyle.Render(branchLabel(row))
	}

	if row.IsRoot {
		titleStr += " " + wtBaseStyle.Render("(base)")
	}

	// Activity column: relative last-activity for stopped/unknown rows.
	var activityStr string
	if !row.LastActivity.IsZero() && (row.State == workspace.StateStopped || row.State == workspace.StateUnknown) {
		activityStr = dimStyle.Render(formatRelative(now, row.LastActivity))
	}

	cells := []string{
		padCell(statusCell, colStateW, lipgloss.Left),
		padCell(titleStr, sessionW, lipgloss.Left),
		padCell(activityStr, colActivityW, lipgloss.Right),
	}
	return strings.Join(cells, strings.Repeat(" ", colGap))
}
