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
	glyphWtStopped = "\U000f0766" // 󰝦 same as glyphInactive — agent stopped
	glyphWtMissing = "\U000f0e7a" // 󰹺 directory absent from disk
	glyphWtUnknown = "?"          // harness has no LiveStatus, tmux window present
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

	// paletteMatchStyle highlights the query characters matched within a
	// session-switcher row, so the user can see why a row matched (typing "cm"
	// lights the 'c' of the repo and the 'm' of the branch). Bright amber + bold
	// to stand out over both the normal and the selected-row background.
	paletteMatchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)
	// paletteBoxStyle frames the floating ctrl+P switcher: a rounded accent
	// border drawn around content the caller has already padded to a fixed
	// width, so the box reads as a modal floating over the session list.
	paletteBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63"))
)

// spinnerFrames are the braille glyphs cycled (one per spinnerTickMsg) on a
// pending-create row to signal an in-flight worktree create/fetch.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

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

// workspaceDisplayLine is one scrollable line in the grouped sessions list.
// Header lines have rowIndex == -1; worktree lines point back into
// model.workspaceRows so keyboard selection and action routing remain based on
// the flat worktree slice.
type workspaceDisplayLine struct {
	repo     string
	rowIndex int
}

// workspaceDisplayLines expands the flat worktree slice into the visual order
// used by the sessions pane, inserting one header before each repo group.
func workspaceDisplayLines(rows []workspace.Row) []workspaceDisplayLine {
	type repoGroup struct {
		repo string
		rows []int
	}
	var groups []repoGroup
	repoIndex := map[string]int{}
	for i, row := range rows {
		gi, ok := repoIndex[row.Repo]
		if !ok {
			gi = len(groups)
			repoIndex[row.Repo] = gi
			groups = append(groups, repoGroup{repo: row.Repo})
		}
		groups[gi].rows = append(groups[gi].rows, i)
	}

	lines := make([]workspaceDisplayLine, 0, len(rows)+len(groups))
	for _, group := range groups {
		lines = append(lines, workspaceDisplayLine{repo: group.repo, rowIndex: -1})
		for _, rowIndex := range group.rows {
			lines = append(lines, workspaceDisplayLine{repo: group.repo, rowIndex: rowIndex})
		}
	}
	return lines
}

// workspaceWindow returns the visible half-open range in lines. scroll is
// preserved while the cursor remains inside the viewport and adjusted only
// when selection crosses an edge, giving j/k navigation conventional scrolling
// behaviour. A negative height means unbounded rendering (used by focused
// render-unit tests and callers that do not need a viewport); zero renders no
// list lines because the pane is fully consumed by pinned content.
func workspaceWindow(lines []workspaceDisplayLine, cursor, scroll, height int) (start, end int) {
	if height < 0 || len(lines) <= height {
		return 0, len(lines)
	}
	if height == 0 {
		return 0, 0
	}

	cursorLine := 0
	for i, line := range lines {
		if line.rowIndex == cursor {
			cursorLine = i
			break
		}
	}
	// When the selected row is the first worktree in a repo, keep its header
	// attached while scrolling upward so the row never loses its repo context.
	cursorTop := cursorLine
	if height >= 2 && cursorLine > 0 && lines[cursorLine-1].rowIndex < 0 {
		cursorTop--
	}

	maxStart := len(lines) - height
	start = min(max(scroll, 0), maxStart)
	switch {
	case cursorTop < start:
		start = cursorTop
	case cursorLine >= start+height:
		start = cursorLine - height + 1
	}
	start = min(max(start, 0), maxStart)
	return start, min(start+height, len(lines))
}

// workspaceFooterLineCount reports the number of non-scrolling status/prompt
// lines pinned beneath the sessions list.
func (m model) workspaceFooterLineCount() int {
	count := 0
	if m.tmuxHint != "" {
		count++
	}
	switch m.prompt {
	case promptNewWorktree, promptFetchBranch,
		promptConfirmDeleteWorktree, promptConfirmDeleteWorktree2,
		promptConfirmRemoveRepo:
		count++
	}
	return count
}

// renderWorkspaceRows renders the complete merged worktree list grouped by
// repo. It remains the unbounded helper used by focused rendering tests.
func (m model) renderWorkspaceRows(width int, rows []workspace.Row, cursor int, now time.Time) string {
	return m.renderWorkspaceRowsViewport(width, 0, rows, cursor, now)
}

// renderWorkspaceRowsViewport renders the merged worktree list within height
// rows. The Sessions title and active hint/prompt lines are pinned; only the
// grouped repo/worktree lines scroll.
func (m model) renderWorkspaceRowsViewport(width, height int, rows []workspace.Row, cursor int, now time.Time) string {
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

	lines := workspaceDisplayLines(rows)
	listHeight := -1
	if height > 0 {
		listHeight = max(0, height-1-m.workspaceFooterLineCount())
	}
	start, end := workspaceWindow(lines, cursor, m.sessionScroll, listHeight)

	for _, displayLine := range lines[start:end] {
		if displayLine.rowIndex < 0 {
			header := wtRepoStyle.Render("  "+filepath.Base(displayLine.repo)) +
				"  " + wtPathStyle.Render(shortenDirectory(displayLine.repo))
			b.WriteString(header + "\n")
			continue
		}

		i := displayLine.rowIndex
		row := rows[i]
		var line string
		switch {
		case row.State == workspace.StateCreating:
			line = m.formatCreatingRow(row, width-2)
		case m.pulling[row.Worktree]:
			line = m.formatSpinnerRow(row, width-2, "pulling")
		default:
			line = formatWorktreeRow(now, row, width-2)
		}
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

	// A trailing newline is an additional rendered row to lipgloss. Trim it so
	// the viewport consumes exactly the height budget calculated above.
	return strings.TrimSuffix(b.String(), "\n")
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

// sessionPaletteRowsVisible is the fixed number of result rows the ctrl+P
// switcher shows, clamped down only when the pane is too short. Holding it
// constant keeps the box from shrinking as the filter narrows matches.
const sessionPaletteRowsVisible = 10

// renderSessionPalette builds the floating ctrl+P switcher box. It is rendered
// independently of the session list and then composited (centred) over it by
// the View via overlayBox, so the surrounding sessions stay visible around the
// modal. fieldW/fieldH are the sessions pane's inner dimensions, used to size
// the box and window the result list so the whole modal fits the pane.
//
// Each row shows the worktree's status glyph and its repo/branch label with the
// matched query characters highlighted; the selected row carries the same
// colour-preserving background band the list uses (see highlightSelectedRow),
// instead of the plain reverse-video used by the repo finder.
func (m model) renderSessionPalette(fieldW, fieldH int) string {
	contentW := fieldW - 10
	if contentW > 56 {
		contentW = 56
	}
	if contentW < 16 {
		contentW = max(1, fieldW-4)
	}

	lines := []string{
		padToWidth(" "+headerStyle.Render("Switch session"), contentW),
		padToWidth(ansi.Truncate(" "+dimStyle.Render("> ")+m.input.View(), contentW, ""), contentW),
		"",
	}

	// Fixed-height list: always emit listH rows so the box keeps a constant
	// height while the filter narrows results, instead of shrinking to fit. The
	// box reserves the title, query, and a blank line above and a blank + footer
	// below; the rest is list rows. Target sessionPaletteRowsVisible rows, clamped
	// down only when the pane is too short to hold them.
	maxRows := fieldH - 7
	if maxRows < 1 {
		maxRows = 1
	}
	listH := min(sessionPaletteRowsVisible, maxRows)

	rendered := 0
	if len(m.sessionPaletteMatches) == 0 {
		lines = append(lines, padToWidth(" "+dimStyle.Render("no match"), contentW))
		rendered++
	} else {
		// Window the result list around the cursor.
		cursor := clampIndex(m.sessionPaletteCursor, len(m.sessionPaletteMatches))
		start := 0
		if cursor >= listH {
			start = cursor - listH + 1
		}
		end := min(start+listH, len(m.sessionPaletteMatches))

		query := m.input.Value()
		for i := start; i < end; i++ {
			ci := m.sessionPaletteMatches[i]
			line := padToWidth(m.renderPaletteRow(m.sessionPaletteRows[ci], m.sessionPaletteLabels[ci], query, contentW), contentW)
			if i == cursor {
				line = highlightSelectedRow(line)
			}
			lines = append(lines, line)
			rendered++
		}
	}
	// Pad any remaining rows with blanks so the box height stays constant.
	for ; rendered < listH; rendered++ {
		lines = append(lines, padToWidth("", contentW))
	}

	footer := fmt.Sprintf("%d sessions · ↑↓ move · enter go · esc cancel", len(m.sessionPaletteMatches))
	lines = append(lines, "", padToWidth(" "+dimStyle.Render(footer), contentW))

	return paletteBoxStyle.Render(strings.Join(lines, "\n"))
}

// renderPaletteRow renders one session-switcher result: the worktree status
// glyph followed by the repo and branch, with the matched query characters
// highlighted in place. Colours mirror the session list — accent repo,
// state-coloured branch — so the switcher reads the same as the pane it jumps
// into. width is the row's cell budget; the line is ellipsis-truncated to fit.
// The selection band is applied by the caller.
func (m model) renderPaletteRow(row workspace.Row, label, query string, width int) string {
	positions, _ := fuzzyMatchPositions(query, label)
	matched := make(map[int]bool, len(positions))
	for _, p := range positions {
		matched[p] = true
	}

	// label is "repo branch" (or just "repo"); split on the first space so the
	// repo and branch segments are styled distinctly while the match positions —
	// which index the whole label — still line up via the per-segment offset.
	repoText, branchText := label, ""
	if sp := strings.IndexByte(label, ' '); sp >= 0 {
		repoText, branchText = label[:sp], label[sp+1:]
	}

	line := " " + worktreeStatusCell(row) + highlightSegment(repoText, wtRepoStyle, matched, 0)
	if branchText != "" {
		line += " " + highlightSegment(branchText, paletteBranchStyle(row.State), matched, len([]rune(repoText))+1)
	}
	return ansi.Truncate(line, width, "…")
}

// paletteBranchStyle returns the colour for a switcher row's branch text,
// mirroring how the session list styles a branch by run-state: running branches
// read in the default foreground, stopped are dimmed, missing/unknown carry
// their warning colours.
func paletteBranchStyle(s workspace.RowState) lipgloss.Style {
	switch s {
	case workspace.StateRunning:
		return lipgloss.NewStyle()
	case workspace.StateMissing:
		return wtMissingStyle
	case workspace.StateUnknown:
		return wtUnknownStyle
	default:
		return wtStoppedStyle
	}
}

// highlightSegment renders text in base, except runes whose index within the
// full label is in matched, which render in paletteMatchStyle. offset is the
// index of text's first rune within the label, so the caller's match positions
// (which index the whole label) align with this segment. Consecutive runes of
// the same kind are coalesced into one styled span to limit escape churn.
func highlightSegment(text string, base lipgloss.Style, matched map[int]bool, offset int) string {
	if text == "" {
		return ""
	}
	runes := []rune(text)
	var b strings.Builder
	for i := 0; i < len(runes); {
		hit := matched[offset+i]
		j := i + 1
		for j < len(runes) && matched[offset+j] == hit {
			j++
		}
		seg := string(runes[i:j])
		if hit {
			b.WriteString(paletteMatchStyle.Render(seg))
		} else {
			b.WriteString(base.Render(seg))
		}
		i = j
	}
	return b.String()
}

// padToWidth right-pads s with spaces to exactly w visible cells (ANSI-aware).
// Strings already at or beyond w are returned unchanged. Used to square off
// palette lines so the selection band and box border align regardless of the
// styling embedded in the line.
func padToWidth(s string, w int) string {
	cur := ansi.StringWidth(s)
	if cur >= w {
		return s
	}
	return s + strings.Repeat(" ", w-cur)
}

// overlayBox composites fg centred over a fieldW×fieldH backdrop drawn from bg.
// bg is the session-list content; it is padded to fieldH lines so the box
// centres within the full pane even when few sessions exist, and trimmed if it
// is taller. Each line the box covers is rebuilt as
// [backdrop-left | box | backdrop-right] using ANSI-aware slicing, so the
// surrounding sessions keep their colours around the modal; lines the box does
// not cover pass through unchanged. ansi.TruncateLeft replays the SGR codes
// preceding the cut, so the right backdrop segment keeps its styling.
func overlayBox(bg string, fieldW, fieldH int, fg string) string {
	bgLines := strings.Split(bg, "\n")
	for len(bgLines) < fieldH {
		bgLines = append(bgLines, "")
	}
	bgLines = bgLines[:fieldH]

	fgLines := strings.Split(fg, "\n")
	fgW := 0
	for _, l := range fgLines {
		if w := ansi.StringWidth(l); w > fgW {
			fgW = w
		}
	}
	top := max(0, (fieldH-len(fgLines))/2)
	left := max(0, (fieldW-fgW)/2)

	const reset = "\x1b[0m"
	out := make([]string, len(bgLines))
	for i, bgLine := range bgLines {
		if i < top || i >= top+len(fgLines) {
			out[i] = bgLine
			continue
		}
		fgLine := fgLines[i-top]
		leftPart := ansi.Truncate(bgLine, left, "")
		leftPad := max(0, left-ansi.StringWidth(leftPart))
		rightPart := ansi.TruncateLeft(bgLine, left+ansi.StringWidth(fgLine), "")
		out[i] = leftPart + reset + strings.Repeat(" ", leftPad) + fgLine + reset + rightPart
	}
	return strings.Join(out, "\n")
}

// helpSection is a titled group of keybindings rendered in the help overlay.
type helpSection struct {
	title    string
	bindings [][2]string // {keys, description}
}

// helpSections is the full keybinding reference shown by the '?' overlay,
// grouped so related actions read together. Kept in one place so the overlay
// stays in sync with the Update key handlers.
var helpSections = []helpSection{
	{"Sessions", [][2]string{
		{"j / k · ↑ / ↓", "move cursor"},
		{"gg / < · G / >", "jump to top / bottom"},
		{"ctrl+u / ctrl+d", "prev / next repo"},
		{"enter", "jump to / resume session"},
		{"ctrl+P", "switch session (fuzzy find)"},
		{"a", "show / hide recent sessions"},
	}},
	{"Worktrees", [][2]string{
		{"n", "new worktree"},
		{"F", "fetch branch from origin"},
		{"P", "pull (fast-forward)"},
		{"D", "delete worktree"},
		{"A", "add repo"},
		{"R", "remove (untrack) repo"},
	}},
	{"Tasks", [][2]string{
		{"T", "show / hide tasks pane"},
		{"tab", "switch focus (sessions / tasks)"},
		{"a", "add task"},
		{"e", "edit task"},
		{"s", "start / stop task"},
		{"d", "mark done"},
		{"D", "delete task"},
		{"U", "undo"},
	}},
	{"General", [][2]string{
		{"?", "toggle this help"},
		{"S", "settings"},
		{"q / ctrl+C", "quit"},
	}},
}

// helpKeyStyle / helpSectionStyle colour the two columns of the help overlay:
// the key column in the accent colour, section titles bold.
var (
	helpKeyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	helpSectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
)

// renderHelp builds the floating keybinding-reference box drawn over the
// sessions pane while prompt == promptHelp. It is composited (centred) by the
// View via overlayBox, mirroring the ctrl+P switcher. The sections are laid out
// in two columns so the box stays short enough to fit a typical pane. fieldW is
// the sessions pane's inner width, used to size the box so it fits.
func renderHelp(fieldW int) string {
	// Widest key cell across all bindings, so descriptions align in a column.
	keyW := 0
	for _, sec := range helpSections {
		for _, b := range sec.bindings {
			if w := lipgloss.Width(b[0]); w > keyW {
				keyW = w
			}
		}
	}

	contentW := fieldW - 10
	if contentW > 76 {
		contentW = 76
	}
	if contentW < 16 {
		contentW = max(1, fieldW-4)
	}

	// Split the sections across two columns. Sessions+Worktrees lead the left
	// column; Tasks+General the right. The columns are rendered independently
	// then zipped row-for-row so the box reads top-to-bottom in two streams.
	const gap = 3
	colW := max(1, (contentW-gap)/2)
	left := helpColumn(helpSections[:2], keyW, colW)
	right := helpColumn(helpSections[2:], keyW, colW)

	var lines []string
	lines = append(lines, padToWidth(" "+headerStyle.Render("Keybindings"), contentW))
	lines = append(lines, padToWidth("", contentW))
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		} else {
			l = padToWidth("", colW)
		}
		if i < len(right) {
			r = right[i]
		}
		lines = append(lines, padToWidth(" "+l+strings.Repeat(" ", gap)+r, contentW))
	}
	lines = append(lines, padToWidth("", contentW))
	lines = append(lines, padToWidth(" "+dimStyle.Render("any key to close"), contentW))

	return paletteBoxStyle.Render(strings.Join(lines, "\n"))
}

// helpColumn renders sections into a slice of fixed-width (colW) lines: a bold
// section title, one line per binding (key column padded to keyW so the
// descriptions align), and a blank line between sections.
func helpColumn(sections []helpSection, keyW, colW int) []string {
	var lines []string
	for i, sec := range sections {
		if i > 0 {
			lines = append(lines, padToWidth("", colW))
		}
		lines = append(lines, padToWidth(helpSectionStyle.Render(sec.title), colW))
		for _, b := range sec.bindings {
			key := padCell(helpKeyStyle.Render(b[0]), keyW, lipgloss.Left)
			row := key + "  " + dimStyle.Render(b[1])
			lines = append(lines, padToWidth(ansi.Truncate(row, colW, "…"), colW))
		}
	}
	return lines
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
	forceNote := ""
	if m.deleteForce {
		forceNote = " — discards uncommitted changes"
	}
	switch m.prompt {
	case promptConfirmDeleteWorktree:
		return wtHintStyle.Render(fmt.Sprintf(
			"delete worktree [%s] — %s? press y to continue, any other key cancels", branch, info))
	case promptConfirmDeleteWorktree2:
		return attnErrStyle.Render(fmt.Sprintf(
			"PERMANENTLY delete worktree [%s] (%s)%s? y to delete · any other key cancels (default: cancel)", branch, info, forceNote))
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

// worktreeStatusCell renders the left-most status column for a worktree row:
// the live attention badge for a running session, or the run-state glyph
// (stopped / missing / unknown) otherwise. Shared by the session list and the
// ctrl+P switcher so both speak the same status vocabulary. The returned cell
// is glyph + trailing space (two cells wide for single-width glyphs).
func worktreeStatusCell(row workspace.Row) string {
	if row.State == workspace.StateRunning {
		return attnLabel(row.Attention, state.SourceLive)
	}
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
	return glyphStyle.Render(glyph) + " "
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

	statusCell := worktreeStatusCell(row)

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

// formatCreatingRow renders an in-flight worktree create/fetch as a muted row
// with an animated spinner in the status column. The verb reflects the flow
// ("fetching…" for 'F', "creating…" for 'n'), looked up from pendingCreates so
// the synthetic Row needs no extra field.
func (m model) formatCreatingRow(row workspace.Row, width int) string {
	verb := "creating"
	if pc, ok := m.pendingCreates[createKey(row.Repo, row.Branch)]; ok && pc.fromRemote {
		verb = "fetching"
	}
	return m.formatSpinnerRow(row, width, verb)
}

// formatSpinnerRow renders row as a muted line with an animated spinner in the
// status column and a "(<verb>…)" suffix, signalling an in-flight git operation
// (creating/fetching a worktree, or pulling its branch). The frame index comes
// from the model so successive spinnerTickMsgs animate it.
func (m model) formatSpinnerRow(row workspace.Row, width int, verb string) string {
	sessionW := worktreeSessionWidth(width)

	glyph := "⠋"
	if len(spinnerFrames) > 0 {
		glyph = spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	}
	statusCell := wtStoppedStyle.Render(glyph) + " "

	titleStr := wtStoppedStyle.Render(branchLabel(row)) + "  " + wtPathStyle.Render("("+verb+"…)")

	cells := []string{
		padCell(statusCell, colStateW, lipgloss.Left),
		padCell(titleStr, sessionW, lipgloss.Left),
		padCell("", colActivityW, lipgloss.Right),
	}
	return strings.Join(cells, strings.Repeat(" ", colGap))
}

// renderSettings builds the floating settings modal drawn over the sessions
// pane while prompt == promptSettings. It mirrors renderHelp's box styling and
// shows the persistent default harness and launch mode, with the highlighted
// row reverse-video. fieldW is the sessions pane inner width.
func (m model) renderSettings(fieldW int) string {
	contentW := fieldW - 10
	if contentW > 60 {
		contentW = 60
	}
	if contentW < 16 {
		contentW = max(1, fieldW-4)
	}

	harnessVal := m.settingsDefaultHarness
	if harnessVal == "" {
		harnessVal = "always ask"
	}
	rows := [][2]string{
		{"default harness", harnessVal},
		{"launch mode", string(normalizeSettingsLaunchMode(m.settingsLaunchMode))},
	}
	labelW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r[0]); w > labelW {
			labelW = w
		}
	}

	var lines []string
	lines = append(lines, padToWidth(" "+headerStyle.Render("Settings"), contentW))
	lines = append(lines, padToWidth("", contentW))
	for i, r := range rows {
		label := padCell(r[0], labelW, lipgloss.Left)
		line := ansi.Truncate(" "+label+"   < "+r[1]+" >", contentW, "…")
		if i == m.settingsCursor {
			line = wtCursorStyle.Render(padToWidth(line, contentW))
		} else {
			line = padToWidth(line, contentW)
		}
		lines = append(lines, line)
	}
	lines = append(lines, padToWidth("", contentW))
	if m.settingsErr != "" {
		lines = append(lines, padToWidth(" "+attnErrStyle.Render(ansi.Truncate(m.settingsErr, contentW-2, "…")), contentW))
	}
	lines = append(lines, padToWidth(" "+dimStyle.Render("↑↓ field · ←→ change · esc close"), contentW))

	return paletteBoxStyle.Render(strings.Join(lines, "\n"))
}
