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
	// agentDetailMinWidth is the narrowest row that still fits the session
	// title and harness; narrower rows keep only status, name and age.
	agentDetailMinWidth = 80
)

// agentHomeDir is read once so row labels can abbreviate it to "~".
var agentHomeDir, _ = os.UserHomeDir()

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

// SetAgents lists agents the way the zellij sidebar does (set_agents in
// zellij-plugin/src/app.rs): those that need you first, then the rest, each
// part oldest activity first so whoever has waited longest sits on top, with
// agents of unknown activity closing their part in cogitator's order.
func (l *agentList) SetAgents(list []agents.Agent) {
	l.agents = slices.Clone(list)
	slices.SortStableFunc(l.agents, func(a, b agents.Agent) int {
		if c := cmp.Compare(agentListPart(a), agentListPart(b)); c != 0 {
			return c
		}
		return a.LastActivity.Compare(b.LastActivity)
	})
	l.settleCursor()
}

// agentListPart ranks needs-you agents first and, within each section,
// agents with a known last activity before those without.
func agentListPart(a agents.Agent) int {
	part := 0
	if a.Group != agents.GroupNeedsYou {
		part = 2
	}
	if a.LastActivity.IsZero() {
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
// Like the zellij sidebar, agents that need you get their own section above
// the rest; SetAgents keeps them at the front of the list.
func (l agentList) View(now time.Time, width, height int) string {
	list := l.visible()
	split := 0
	for split < len(list) && list[split].Group == agents.GroupNeedsYou {
		split++
	}
	sections := []struct {
		title string
		style lipgloss.Style
		rows  []agents.Agent
	}{
		{"Needs you", attnPermStyle, list[:split]},
		{"Agents", headerStyle, list[split:]},
	}

	var lines []string
	cursorLine := 0
	for _, s := range sections {
		if len(s.rows) == 0 {
			continue
		}

		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, s.style.Render(s.title)+dimStyle.Render(fmt.Sprintf(" · %d", len(s.rows))))
		for _, a := range s.rows {
			if a.ID == l.cursorID {
				cursorLine = len(lines)
			}
			lines = append(lines, renderAgentRow(now, a, a.ID == l.cursorID, width))
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

func windowLines(lines []string, cursorLine, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}

	start := min(max(cursorLine-height/2, 0), len(lines)-height)

	return lines[start : start+height]
}

// renderAgentRow lays out status, name and (when wide enough) the muted
// session title on the left, harness and age on the right, like a Repos row.
func renderAgentRow(now time.Time, a agents.Agent, selected bool, width int) string {
	left := agentStatusCell(a) + agentStyledName(a)
	right := dimStyle.Render(formatRelative(now, a.LastActivity))

	if width >= agentDetailMinWidth {
		if a.Title != "" && a.Title != a.Tab {
			left += "  " + wtPathStyle.Render(a.Title)
		}
		right = fitCell(agentColor(a.Harness).Render(a.Harness), agentHarnessWidth) + " " + alignRight(right, 4)
	}

	row := left + "  " + right
	if width > 0 {
		gap := width - ansi.StringWidth(right) - 2
		row = fitCell(left, max(gap, 0)) + "  " + right
	}

	if selected {
		return highlightSelectedRow(row)
	}

	return row
}

// agentStatusCell renders the same status glyph the Repos and Workspaces
// tabs show for the agent's session.
func agentStatusCell(a agents.Agent) string {
	return worktreeStatusCell(settings.Row{State: a.State, Attention: a.Attention})
}

// agentStyledName is agentName with the repo or workspace muted so the branch
// or session reads first, led by the agent's tab label when it has one;
// stopped agents are dimmed as in the Repos tab.
func agentStyledName(a agents.Agent) string {
	lead := lipgloss.NewStyle()
	if a.State == settings.StateStopped {
		lead = wtStoppedStyle
	}

	var name string
	switch {
	case a.Kind == agents.KindWorkspace:
		name = wtRepoStyle.UnsetBold().Render(a.Workspace) + dimStyle.Render("/") + lead.Render(a.SessionName)
	case a.Kind == agents.KindStray:
		name = lead.Render(abbreviateHome(a.Dir)) + " " + recentStyle.Render("stray")
	case a.Branch == "":
		name = lead.Render(abbreviateHome(a.Dir))
	default:
		name = wtRepoStyle.UnsetBold().Render(filepath.Base(a.Repo)) + dimStyle.Render("·") + lead.Render(a.Branch)
	}

	if a.Tab == "" {
		return name
	}
	return lead.Render(a.Tab) + dimStyle.Render(agentTabSeparator) + name
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

// agentName is the plain-text label rows display and the filter matches on.
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
