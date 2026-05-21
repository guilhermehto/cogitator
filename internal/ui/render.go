package ui

import (
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/guilhermehto/cogitator/internal/state"
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

func legendLine() string {
	parts := []string{
		dimStyle.Render("legend:"),
		attnActiveStyle.Render(glyphActive) + " " + dimStyle.Render("active"),
		attnInactiveStyle.Render(glyphInactive) + " " + dimStyle.Render("inactive"),
		recentStyle.Render(glyphRecent) + " " + dimStyle.Render("recent"),
		attnQuestionStyle.Render(glyphQuestion) + " " + dimStyle.Render("question"),
		attnPermStyle.Render(glyphPermission) + " " + dimStyle.Render("permission"),
		attnErrStyle.Render(glyphError) + " " + dimStyle.Render("error"),
	}
	return strings.Join(parts, "  ")
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
