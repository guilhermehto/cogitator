package ui

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Padding(0, 1)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	agentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Italic(true)
	recentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	paneStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	paneFocusedStyle = paneStyle.BorderForeground(lipgloss.Color("63"))

	attnPermStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	attnQuestionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
	attnErrStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	attnInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	attnActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))

	statusBusyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))

	// taskActiveStyle highlights a running Taskwarrior task. Bold + green so
	// the running row is distinguishable from the cursor (reverse-video) and
	// from the priority glyph palette.
	taskActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true)
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
	glyphActive     = "\U000f09de" // 󰧞
	glyphInactive   = "\U000f0764" // 󰝤
	glyphRecent     = "\U000f02da" // 󰋚
	glyphQuestion   = "\U000f0625" // 󰘥
	glyphPermission = "\U000f033e" // 󰌾
	glyphError      = "\U000f0026" // 󰀦
	// glyphTaskActive marks a running Taskwarrior task in the ST column.
	// When active, it replaces the priority glyph for that row.
	glyphTaskActive = "\U000f040a" // 󰐊 play

	// Workspace / worktree row glyphs.
	glyphWtRunning   = "\U000f09de" // 󰧞 same as glyphActive — live session present
	glyphWtStopped   = "\U000f0764" // 󰝤 same as glyphInactive — agent stopped
	glyphWtEmpty     = "○"          // no session ever launched here
	glyphWtMissing   = "\U000f0e7a" // 󰹺 directory absent from disk
	glyphWtUnknown   = "?"          // harness has no LiveStatus, tmux window present
	glyphWtLaunching = "⟳"          // optimistic launching overlay — harness starting
)

var (
	// wtRunningStyle highlights a running worktree row (same palette as active sessions).
	wtRunningStyle = attnActiveStyle
	// wtStoppedStyle dims a stopped worktree row.
	wtStoppedStyle = dimStyle
	// wtEmptyStyle renders an empty worktree row (no session ever launched).
	wtEmptyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	// wtMissingStyle renders a missing worktree row (directory absent).
	wtMissingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Italic(true)
	// wtUnknownStyle renders an unknown worktree row.
	wtUnknownStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	// wtLaunchingStyle renders a row in the optimistic launching overlay.
	wtLaunchingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	// wtCursorStyle highlights the row under the session cursor.
	wtCursorStyle = lipgloss.NewStyle().Reverse(true)
	// wtRepoStyle renders the repo group header.
	wtRepoStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	// wtHintStyle renders the transient tmux hint line.
	wtHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Italic(true)
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
func legendLine(width int) string {
	sessionParts := []string{
		dimStyle.Render("legend:"),
		attnActiveStyle.Render(glyphActive) + " " + dimStyle.Render("active"),
		attnInactiveStyle.Render(glyphInactive) + " " + dimStyle.Render("inactive"),
		recentStyle.Render(glyphRecent) + " " + dimStyle.Render("recent"),
		attnQuestionStyle.Render(glyphQuestion) + " " + dimStyle.Render("question"),
		attnPermStyle.Render(glyphPermission) + " " + dimStyle.Render("permission"),
		attnErrStyle.Render(glyphError) + " " + dimStyle.Render("error"),
	}
	sessionLegend := strings.Join(sessionParts, "  ")

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

	titleRender := title
	if sv.Source == state.SourceRecent {
		titleRender = dimStyle.Render(title)
	}
	sessionContent := prefix + titleRender
	if agentTag != "" {
		sessionContent = prefix + agentTag + " " + titleRender
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
// launching is the optimistic overlay map (canonical dir → deadline); rows
// whose dir is in this map render as "launching" regardless of their State.
// hint is a transient one-line message shown below the rows (e.g. tmux hint).
func (m model) renderWorkspaceRows(width int, rows []workspace.Row, cursor int, now time.Time) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Sessions") + "\n")
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("(no worktrees configured)"))
		if m.tmuxHint != "" {
			b.WriteString("\n" + wtHintStyle.Render(m.tmuxHint))
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
		if repo == "" {
			repo = "(unconfigured)"
		}
		gi, ok := repoIndex[repo]
		if !ok {
			gi = len(groups)
			repoIndex[repo] = gi
			groups = append(groups, repoGroup{repo: repo})
		}
		groups[gi].rows = append(groups[gi].rows, i)
	}

	for _, g := range groups {
		// Repo header: show the base name of the repo path.
		repoLabel := filepath.Base(g.repo)
		if g.repo == "(unconfigured)" {
			repoLabel = g.repo
		}
		b.WriteString(wtRepoStyle.Render("  "+repoLabel) + "\n")

		for _, i := range g.rows {
			row := rows[i]
			// Check if this row is in the launching overlay.
			isLaunching := m.launching != nil && m.launching[row.Worktree] != (time.Time{})
			line := formatWorktreeRow(now, row, width-2, isLaunching)
			if i == cursor {
				// Highlight the cursor row with reverse video. We apply the
				// style to the full rendered line so the highlight spans the
				// entire row width.
				line = wtCursorStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}

	// Render transient hint (e.g. tmux unavailable, launch error).
	if m.tmuxHint != "" {
		b.WriteString(wtHintStyle.Render(m.tmuxHint) + "\n")
	}

	return b.String()
}

// formatWorktreeRow renders a single workspace.Row as a fixed-width line.
// The layout reuses the same column widths as the live-session rows so the
// two views feel consistent.
//
// isLaunching overrides the row's State to show the optimistic "launching"
// indicator when the harness has been started but not yet confirmed running.
func formatWorktreeRow(now time.Time, row workspace.Row, width int, isLaunching bool) string {
	// When the launching overlay is active, render a distinct launching state
	// regardless of the underlying row state.
	if isLaunching {
		stateCell := wtLaunchingStyle.Render(glyphWtLaunching) + " "
		titleStr := wtLaunchingStyle.Render("launching…")
		if row.Branch != "" {
			titleStr += "  " + dimStyle.Render("["+row.Branch+"]")
		}
		sessionW := width - colStateW - colStatusW - colActivityW - 3*colGap
		if sessionW < 1 {
			sessionW = 1
		}
		cells := []string{
			padCell(stateCell, colStateW, lipgloss.Left),
			padCell(titleStr, sessionW, lipgloss.Left),
			padCell("", colStatusW, lipgloss.Right),
			padCell("", colActivityW, lipgloss.Right),
		}
		return strings.Join(cells, strings.Repeat(" ", colGap))
	}

	// State glyph and style.
	var glyph string
	var glyphStyle lipgloss.Style
	switch row.State {
	case workspace.StateRunning:
		glyph = glyphWtRunning
		glyphStyle = wtRunningStyle
	case workspace.StateStopped:
		glyph = glyphWtStopped
		glyphStyle = wtStoppedStyle
	case workspace.StateEmpty:
		glyph = glyphWtEmpty
		glyphStyle = wtEmptyStyle
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
	stateCell := glyphStyle.Render(glyph) + " "

	// Title / description column.
	var titleStr string
	switch row.State {
	case workspace.StateRunning:
		// Running: show title (or branch/worktree dir as fallback).
		titleStr = row.Title
		if titleStr == "" {
			titleStr = row.Branch
		}
		if titleStr == "" {
			titleStr = filepath.Base(row.Worktree)
		}
	case workspace.StateStopped:
		// Stopped: show title dimmed.
		titleStr = row.Title
		if titleStr == "" {
			titleStr = filepath.Base(row.Worktree)
		}
		titleStr = wtStoppedStyle.Render(titleStr)
	case workspace.StateEmpty:
		titleStr = wtEmptyStyle.Render(filepath.Base(row.Worktree))
	case workspace.StateMissing:
		titleStr = wtMissingStyle.Render(filepath.Base(row.Worktree) + " (missing)")
	case workspace.StateUnknown:
		titleStr = wtUnknownStyle.Render("status unknown")
	default:
		titleStr = dimStyle.Render(filepath.Base(row.Worktree))
	}

	// Branch annotation (shown for non-running rows where branch is known).
	if row.Branch != "" && row.State != workspace.StateRunning {
		titleStr += "  " + dimStyle.Render("["+row.Branch+"]")
	}

	// Status column: attention badges for running rows; empty otherwise.
	var statusStr string
	if row.State == workspace.StateRunning {
		statusStr = attnLabel(row.Attention, state.SourceLive)
	}

	// Activity column: relative last-activity for stopped/unknown rows.
	var activityStr string
	if !row.LastActivity.IsZero() && (row.State == workspace.StateStopped || row.State == workspace.StateUnknown) {
		activityStr = dimStyle.Render(formatRelative(now, row.LastActivity))
	}

	sessionW := width - colStateW - colStatusW - colActivityW - 3*colGap
	if sessionW < 1 {
		sessionW = 1
	}

	cells := []string{
		padCell(stateCell, colStateW, lipgloss.Left),
		padCell(titleStr, sessionW, lipgloss.Left),
		padCell(statusStr, colStatusW, lipgloss.Right),
		padCell(activityStr, colActivityW, lipgloss.Right),
	}
	return strings.Join(cells, strings.Repeat(" ", colGap))
}
