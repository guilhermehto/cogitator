package ui

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/settings"
)

const (
	agentHarnessWidth = 11
	// agentDetailMinWidth is the narrowest row that still fits the harness;
	// narrower rows keep only status, name and age on their first line.
	agentDetailMinWidth = 80
	// agentRowLines is how many lines an agent row takes: the name, then the
	// tab label and session title under it.
	agentRowLines = 2
)

// agentHomeDir is read once so row labels can abbreviate it to "~".
var agentHomeDir, _ = os.UserHomeDir()

var agentRepoMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("103"))

type agentListIntent int

const (
	agentIntentNone agentListIntent = iota
	agentIntentJump
	agentIntentSeen
	agentIntentQuit
	agentIntentKill
)

// agentListResult tells the host what a key press asked for; agentID is set
// for jump, seen and kill.
type agentListResult struct {
	intent  agentListIntent
	agentID string
}

// agentList is the agent list shared by the pick popup and the main TUI. The
// cursor is pinned to an agent ID so re-sorts never move it off the agent the
// user is looking at.
type agentList struct {
	agents    []agents.Agent
	cursorID  string
	filtering bool
	filter    textinput.Model
	gPending  bool
}

func newAgentList() agentList {
	input := textinput.New()
	input.Prompt = "/ "
	input.Placeholder = "filter"

	return agentList{filter: input}
}

type agentSection struct {
	title string
	style lipgloss.Style
}

// agentSections are the agent list's sections in display order, as the
// zellij sidebar shows them (render_list in zellij-plugin/src/render.rs).
var agentSections = []agentSection{
	{"Needs you", attnPermStyle},
	{"Working", attnActiveStyle.Bold(true)},
	{"Done", attnFinishedStyle},
	{"Idle", headerStyle},
}

// agentSectionOf indexes agentSections; stopped agents sit with idle ones.
func agentSectionOf(a agents.Agent) int {
	switch a.Group {
	case agents.GroupNeedsYou:
		return 0
	case agents.GroupWorking:
		return 1
	case agents.GroupDone:
		return 2
	default:
		return 3
	}
}

// SetAgents lists agents the way the zellij sidebar does (set_agents in
// zellij-plugin/src/app.rs): section by section, each oldest OrderedAt first
// so whoever has waited longest sits on top, with agents of unknown order
// closing their section in cogitator's order.
func (l *agentList) SetAgents(list []agents.Agent) {
	l.agents = slices.Clone(list)
	slices.SortStableFunc(l.agents, func(a, b agents.Agent) int {
		if c := cmp.Compare(agentListPart(a), agentListPart(b)); c != 0 {
			return c
		}
		return a.OrderedAt.Compare(b.OrderedAt)
	})
	l.settleCursor()
}

// agentListPart ranks agents by section and, within each section, agents
// with a known OrderedAt before those without.
func agentListPart(a agents.Agent) int {
	part := 2 * agentSectionOf(a)
	if a.OrderedAt.IsZero() {
		part++
	}
	return part
}

// visible returns the agents matching the filter, keeping the list order.
func (l agentList) visible() []agents.Agent {
	query := l.filter.Value()
	if strings.TrimSpace(query) == "" {
		return l.agents
	}

	labels := make([]string, len(l.agents))
	for i, a := range l.agents {
		labels[i] = agentName(a)
	}

	matched := make(map[int]bool)
	for _, idx := range fuzzyMatchIndices(query, labels) {
		matched[idx] = true
	}

	var out []agents.Agent
	for i, a := range l.agents {
		if matched[i] {
			out = append(out, a)
		}
	}

	return out
}

func (l agentList) cursorIndex(list []agents.Agent) int {
	for i, a := range list {
		if a.ID == l.cursorID {
			return i
		}
	}

	return -1
}

// settleCursor keeps the cursor on its agent, falling back to the first row
// when that agent is gone or filtered out.
func (l *agentList) settleCursor() {
	list := l.visible()
	if l.cursorIndex(list) >= 0 {
		return
	}

	l.cursorID = ""
	if len(list) > 0 {
		l.cursorID = list[0].ID
	}
}

func (l *agentList) move(delta int) {
	list := l.visible()
	if len(list) == 0 {
		return
	}

	next := clampIndex(max(l.cursorIndex(list), 0)+delta, len(list))
	l.cursorID = list[next].ID
}

func (l *agentList) moveTo(last bool) {
	list := l.visible()
	if len(list) == 0 {
		return
	}

	if last {
		l.cursorID = list[len(list)-1].ID

		return
	}
	l.cursorID = list[0].ID
}

// Current returns the agent under the cursor.
func (l agentList) Current() (agents.Agent, bool) {
	list := l.visible()
	idx := l.cursorIndex(list)
	if idx < 0 {
		return agents.Agent{}, false
	}

	return list[idx], true
}

func (l agentList) Filtering() bool {
	return l.filtering
}

// HandleKey applies a key press and reports what the host should do.
func (l *agentList) HandleKey(msg tea.KeyMsg) agentListResult {
	if l.filtering {
		return l.handleFilterKey(msg)
	}

	pendingG := l.gPending
	l.gPending = false

	switch msg.String() {
	case "esc", "q", "ctrl+c":
		return agentListResult{intent: agentIntentQuit}
	case "enter":
		return l.selection(agentIntentJump)
	case "x":
		return l.selection(agentIntentSeen)
	case "K":
		return l.selection(agentIntentKill)
	case "j", "down":
		l.move(1)
	case "k", "up":
		l.move(-1)
	case "G":
		l.moveTo(true)
	case "g":
		if pendingG {
			l.moveTo(false)
		} else {
			l.gPending = true
		}
	case "/":
		l.filtering = true
		l.filter.Focus()
	}

	return agentListResult{}
}

func (l *agentList) handleFilterKey(msg tea.KeyMsg) agentListResult {
	switch msg.String() {
	case "ctrl+c":
		return agentListResult{intent: agentIntentQuit}
	case "enter":
		return l.selection(agentIntentJump)
	case "esc":
		l.filtering = false
		l.filter.Blur()
		l.filter.SetValue("")
	case "down", "ctrl+n":
		l.move(1)
	case "up", "ctrl+p":
		l.move(-1)
	default:
		l.filter, _ = l.filter.Update(msg)
	}
	l.settleCursor()

	return agentListResult{}
}

func (l agentList) selection(intent agentListIntent) agentListResult {
	if l.cursorIndex(l.visible()) < 0 {
		return agentListResult{}
	}

	return agentListResult{intent: intent, agentID: l.cursorID}
}

// View renders at most height lines, scrolled so the cursor row stays visible.
// Each non-empty section of agentSections gets a titled block; SetAgents keeps
// the list in section order, so the cursor moves through them top to bottom.
func (l agentList) View(now time.Time, width, height int) string {
	list := l.visible()
	rowsBySection := make([][]agents.Agent, len(agentSections))
	for _, a := range list {
		section := agentSectionOf(a)
		rowsBySection[section] = append(rowsBySection[section], a)
	}

	var lines []string
	cursorLine := 0
	for i, s := range agentSections {
		rows := rowsBySection[i]
		if len(rows) == 0 {
			continue
		}

		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, s.style.Render(s.title)+dimStyle.Render(fmt.Sprintf(" · %d", len(rows))))
		for _, a := range rows {
			if a.ID == l.cursorID {
				cursorLine = len(lines)
			}
			lines = append(lines, renderAgentRow(now, a, a.ID == l.cursorID, width)...)
		}
	}

	if len(list) == 0 {
		lines = append(lines, dimStyle.Render("no agents in flight"))
	}

	if l.filtering {
		lines = append(lines, l.filter.View())
	}

	return strings.Join(windowLines(lines, cursorLine, height), "\n")
}

// windowLines keeps the cursor row, which starts at cursorLine, whole and
// near the middle of the height lines it returns.
func windowLines(lines []string, cursorLine, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}

	cursorEnd := cursorLine + agentRowLines - 1
	start := min(max(cursorEnd-height/2, 0), len(lines)-height, cursorLine)

	return lines[start : start+height]
}

// renderAgentRow lays out an agent on agentRowLines lines: status and name on
// the first, with harness (when wide enough) and age on the right like a Repos
// row, then the tab label and muted session title under the name.
func renderAgentRow(now time.Time, a agents.Agent, selected bool, width int) []string {
	status := agentStatusCell(a)
	left := status + agentStyledTarget(a)
	right := dimStyle.Render(formatRelative(now, a.LastActivity))

	if width >= agentDetailMinWidth {
		right = fitCell(agentColor(a.Harness).Render(a.Harness), agentHarnessWidth) + " " + alignRight(right, 4)
	}

	name := left + "  " + right
	detail := strings.Repeat(" ", ansi.StringWidth(status)) + agentStyledDetail(a)
	if width > 0 {
		gap := width - ansi.StringWidth(right) - 2
		name = fitCell(left, max(gap, 0)) + "  " + right
		detail = fitCell(detail, width)
	}

	lines := []string{name, detail}
	if selected {
		for i := range lines {
			lines[i] = highlightSelectedRow(lines[i])
		}
	}

	return lines
}

// agentStatusCell renders the same status glyph the Repos and Workspaces
// tabs show for the agent's session.
func agentStatusCell(a agents.Agent) string {
	return worktreeStatusCell(settings.Row{State: a.State, Attention: a.Attention})
}

// agentStyledName is agentName styled: agentStyledTarget led by the agent's
// tab label when it has one.
func agentStyledName(a agents.Agent) string {
	target := agentStyledTarget(a)
	if a.Tab == "" {
		return target
	}
	return agentLeadStyle(a).Render(a.Tab) + dimStyle.Render(agentTabSeparator) + target
}

// agentStyledTarget names what the agent works in, with the repo or workspace
// muted so the branch or session reads first.
func agentStyledTarget(a agents.Agent) string {
	lead := agentLeadStyle(a)
	repo := agentRepoStyle(a)

	switch {
	case a.Kind == agents.KindWorkspace:
		return repo.Render(a.Workspace) + dimStyle.Render("/") + lead.Render(a.SessionName)
	case a.Kind == agents.KindStray:
		return lead.Render(abbreviateHome(a.Dir)) + " " + recentStyle.Render("stray")
	case a.Branch == "":
		return lead.Render(abbreviateHome(a.Dir))
	default:
		return repo.Render(filepath.Base(a.Repo)) + dimStyle.Render("·") + lead.Render(a.Branch)
	}
}

// agentStyledDetail is the line under an agent's name: its tab label, then
// its session title muted unless the label already is the title.
func agentStyledDetail(a agents.Agent) string {
	var parts []string
	if a.Tab != "" {
		parts = append(parts, agentLeadStyle(a).Render(a.Tab))
	}
	if a.Title != "" && a.Title != a.Tab {
		parts = append(parts, wtPathStyle.Render(a.Title))
	}
	return strings.Join(parts, "  ")
}

// agentLeadStyle dims a stopped agent's own text, as the Repos tab does.
func agentLeadStyle(a agents.Agent) lipgloss.Style {
	if a.State == settings.StateStopped {
		return wtStoppedStyle
	}
	return lipgloss.NewStyle()
}

// agentRepoStyle is agentLeadStyle for the repo or workspace beside the
// branch or session, which is muted even while the agent runs.
func agentRepoStyle(a agents.Agent) lipgloss.Style {
	if a.State == settings.StateStopped {
		return wtStoppedStyle
	}
	return agentRepoMutedStyle
}

func alignRight(s string, width int) string {
	return strings.Repeat(" ", max(0, width-ansi.StringWidth(s))) + s
}

func fitCell(s string, width int) string {
	s = ansi.Truncate(s, width, "…")

	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

// agentTabSeparator sits between an agent's tab label and its name.
const agentTabSeparator = " · "

// agentName is the one-line plain-text label of an agent: the filter matches
// on it, and prompts and the session palette show it.
func agentName(a agents.Agent) string {
	var name string
	switch {
	case a.Kind == agents.KindWorkspace:
		name = a.Workspace + "/" + a.SessionName
	case a.Kind == agents.KindStray:
		name = abbreviateHome(a.Dir) + " stray"
	case a.Branch == "":
		name = abbreviateHome(a.Dir)
	default:
		name = filepath.Base(a.Repo) + "·" + a.Branch
	}

	if a.Tab == "" {
		return name
	}
	return a.Tab + agentTabSeparator + name
}

func abbreviateHome(dir string) string {
	if agentHomeDir == "" {
		return dir
	}
	if dir == agentHomeDir {
		return "~"
	}
	if rest, ok := strings.CutPrefix(dir, agentHomeDir+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}

	return dir
}
