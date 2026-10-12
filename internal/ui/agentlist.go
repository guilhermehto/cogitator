package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
)

const (
	agentNameWidth    = 34
	agentHarnessWidth = 10
	agentReasonWidth  = 11
)

var agentGroupTitles = map[agents.Group]string{
	agents.GroupNeedsYou: "Needs you",
	agents.GroupDone:     "Done",
	agents.GroupWorking:  "Working",
	agents.GroupIdle:     "Idle",
	agents.GroupStopped:  "Stopped",
}

type agentListIntent int

const (
	agentIntentNone agentListIntent = iota
	agentIntentJump
	agentIntentSeen
	agentIntentQuit
)

// agentListResult tells the host what a key press asked for; agentID is set
// for jump and seen.
type agentListResult struct {
	intent  agentListIntent
	agentID string
}

// agentList is the attention-ordered agent list shared by the pick popup and
// the main TUI. The cursor is pinned to an agent ID so re-sorts never move it
// off the agent the user is looking at.
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

func (l *agentList) SetAgents(list []agents.Agent) {
	l.agents = list
	l.settleCursor()
}

// visible returns the agents matching the filter, keeping the group order.
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
func (l agentList) View(now time.Time, width, height int) string {
	list := l.visible()

	var lines []string
	cursorLine := 0
	counts := agentGroupCounts(list)
	lastGroup := agents.Group(-1)
	for _, a := range list {
		if a.Group != lastGroup {
			lines = append(lines, headerStyle.Render(fmt.Sprintf("%s (%d)", agentGroupTitles[a.Group], counts[a.Group])))
			lastGroup = a.Group
		}
		if a.ID == l.cursorID {
			cursorLine = len(lines)
		}
		lines = append(lines, renderAgentRow(now, a, a.ID == l.cursorID, width))
	}

	if len(list) == 0 {
		lines = append(lines, dimStyle.Render("  no agents"))
	}

	if l.filtering {
		lines = append(lines, l.filter.View())
	}

	return strings.Join(windowLines(lines, cursorLine, height), "\n")
}

func agentGroupCounts(list []agents.Agent) map[agents.Group]int {
	counts := make(map[agents.Group]int)
	for _, a := range list {
		counts[a.Group]++
	}

	return counts
}

func windowLines(lines []string, cursorLine, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}

	start := min(max(cursorLine-height/2, 0), len(lines)-height)

	return lines[start : start+height]
}

func renderAgentRow(now time.Time, a agents.Agent, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = promptMarkerStyle.Render("▌ ")
	}

	status := worktreeStatusCell(settings.Row{State: a.State, Attention: a.Attention})
	name := fitCell(agentName(a), agentNameWidth)
	harness := fitCell(dimStyle.Render(a.Harness), agentHarnessWidth)
	reason := fitCell(agentReason(a), agentReasonWidth)
	age := dimStyle.Render(formatRelative(now, a.LastActivity))

	row := marker + status + name + " " + harness + " " + reason + " " + age

	if width <= 0 {
		return row
	}

	return ansi.Truncate(row, width, "…")
}

func fitCell(s string, width int) string {
	s = ansi.Truncate(s, width, "…")

	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

// agentName is the plain-text label rows display and the filter matches on.
func agentName(a agents.Agent) string {
	switch a.Kind {
	case agents.KindWorkspace:
		return a.Workspace + "/" + a.SessionName
	case agents.KindStray:
		return filepath.Base(a.ID) + " stray"
	}
	if a.Branch == "" {
		return a.Repo
	}

	return a.Repo + "·" + a.Branch
}

func agentReason(a agents.Agent) string {
	switch a.Attention {
	case state.AttnPermissionPending:
		return attnPermStyle.Render("permission")
	case state.AttnQuestionPending:
		return attnQuestionStyle.Render("question")
	case state.AttnErrored:
		return attnErrStyle.Render("error")
	case state.AttnFinished:
		return attnFinishedStyle.Render("finished")
	}

	switch a.Group {
	case agents.GroupWorking:
		return attnActiveStyle.Render("working")
	case agents.GroupStopped:
		return dimStyle.Render("stopped")
	}

	return dimStyle.Render("idle")
}
