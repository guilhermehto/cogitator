package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const (
	previewDebounce     = 150 * time.Millisecond
	previewRefreshEvery = time.Second
	previewCapture      = 60
	// agentSplitMinWidth is the narrowest width that still fits a readable
	// list beside the preview; narrower hosts show the list alone.
	agentSplitMinWidth = 100
)

type (
	previewDebounceMsg struct{ seq int }
	previewRefreshMsg  struct{ seq int }
	previewCaptureMsg  struct {
		agentID string
		text    string
		err     error
	}
)

// agentPreview shows the tail of the cursor agent's tmux pane. Captures are
// debounced while the cursor moves and repeat while it rests, so the preview
// reads live without a capture per keystroke.
type agentPreview struct {
	runner tmuxctl.Runner
	// want is the agent the cursor settled on; "" while hidden. seq
	// invalidates debounce and refresh ticks from earlier positions.
	want     string
	seq      int
	inFlight bool
	agentID  string
	text     string
	err      string
}

// Follow schedules a capture when the cursor settled on another agent.
func (p *agentPreview) Follow(current agents.Agent, ok bool) tea.Cmd {
	if !ok || current.ID == p.want {
		return nil
	}

	p.want = current.ID
	p.seq++
	seq := p.seq

	return tea.Tick(previewDebounce, func(time.Time) tea.Msg { return previewDebounceMsg{seq: seq} })
}

// Hide stops the refresh loop; the next Follow starts it again.
func (p *agentPreview) Hide() {
	if p.want == "" {
		return
	}

	p.want = ""
	p.seq++
}

// Debounced starts the capture for a settled cursor. A capture already in
// flight is left alone; Captured chases the cursor afterwards.
func (p *agentPreview) Debounced(msg previewDebounceMsg, current agents.Agent, ok bool) tea.Cmd {
	if msg.seq != p.seq || p.inFlight || !ok {
		return nil
	}

	return p.start(current)
}

// Refresh re-captures the resting cursor agent, picking up a cursor that a
// list re-sort moved without a key press.
func (p *agentPreview) Refresh(msg previewRefreshMsg, current agents.Agent, ok bool) tea.Cmd {
	if msg.seq != p.seq || p.inFlight || !ok || p.want == "" {
		return nil
	}

	p.want = current.ID

	return p.start(current)
}

func (p *agentPreview) Captured(msg previewCaptureMsg, current agents.Agent, ok bool) tea.Cmd {
	p.inFlight = false
	p.agentID, p.text, p.err = msg.agentID, msg.text, ""
	if msg.err != nil {
		p.text, p.err = "", msg.err.Error()
	}

	if ok && current.ID != msg.agentID {
		return p.start(current)
	}

	return p.scheduleRefresh()
}

func (p *agentPreview) start(current agents.Agent) tea.Cmd {
	target := paneTarget(current)
	if target == "" {
		p.agentID, p.text, p.err = current.ID, "", ""

		return p.scheduleRefresh()
	}

	p.inFlight = true
	runner := p.runner
	id := current.ID

	return func() tea.Msg {
		text, err := tmuxctl.CapturePane(runner, target, previewCapture)

		return previewCaptureMsg{agentID: id, text: text, err: err}
	}
}

func (p *agentPreview) scheduleRefresh() tea.Cmd {
	if p.want == "" {
		return nil
	}

	seq := p.seq

	return tea.Tick(previewRefreshEvery, func(time.Time) tea.Msg { return previewRefreshMsg{seq: seq} })
}

// View renders the agent's identity, status and pane tail in width × height.
func (p agentPreview) View(a agents.Agent, now time.Time, width, height int) string {
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(agentStyledName(a)),
		previewStatusLine(a, now),
	}
	if a.Title != "" {
		lines = append(lines, wtPathStyle.Render(a.Title))
	}
	lines = append(lines, dimStyle.Render(strings.Repeat("─", max(width, 0))))

	switch {
	case paneTarget(a) == "":
		lines = append(lines, dimStyle.Render(noPaneText(a)))
	case p.agentID != a.ID:
		lines = append(lines, dimStyle.Render("loading…"))
	case p.err != "":
		lines = append(lines, dimStyle.Render(p.err))
	default:
		lines = append(lines, tailLines(p.text, max(height-len(lines), 0))...)
	}

	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}

	return strings.Join(lines, "\n")
}

func previewStatusLine(a agents.Agent, now time.Time) string {
	parts := []string{agentStatusCell(a) + previewReason(a)}
	if a.Harness != "" {
		parts = append(parts, agentColor(a.Harness).Render(a.Harness))
	}
	if age := formatRelative(now, a.LastActivity); age != "" {
		parts = append(parts, dimStyle.Render(age))
	}

	return strings.Join(parts, dimStyle.Render(" · "))
}

func noPaneText(a agents.Agent) string {
	if a.Kind == agents.KindStray {
		return "no tmux pane"
	}

	return "no tmux window — enter resumes it"
}

// previewReason says why the agent is where it is. It comes from Attention,
// never from captured pane text.
func previewReason(a agents.Agent) string {
	switch a.Attention {
	case state.AttnPermissionPending:
		return attnPermStyle.Render("needs permission")
	case state.AttnQuestionPending:
		return attnQuestionStyle.Render("needs an answer")
	case state.AttnErrored:
		return attnErrStyle.Render("errored")
	case state.AttnFinished:
		return attnFinishedStyle.Render("finished")
	}

	switch a.Group {
	case agents.GroupWorking:
		return attnActiveStyle.Render("working")
	case agents.GroupStopped:
		return dimStyle.Render("stopped")
	}

	return attnInactiveStyle.Render("idle")
}

// paneTarget addresses a stray by its pane and everything else by its window,
// whose active pane is where the harness runs.
func paneTarget(a agents.Agent) string {
	if a.Kind == agents.KindStray && a.PaneID != "" {
		return a.PaneID
	}
	if a.WindowID != "" {
		return a.WindowID
	}

	return a.PaneID
}

// tailLines returns the last n non-trailing-blank lines of captured text with
// control sequences stripped; the text is display-only.
func tailLines(text string, n int) []string {
	lines := strings.Split(strings.TrimRight(ansi.Strip(text), " \n"), "\n")
	if n <= 0 {
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return lines
}

// renderAgentSplit lays the list beside the cursor agent's preview, or the
// list alone when width cannot fit both.
func renderAgentSplit(list agentList, preview agentPreview, now time.Time, width, height int) string {
	if width < agentSplitMinWidth {
		return list.View(now, width, height)
	}

	listWidth := min(max(width*2/5, 44), 64)
	previewWidth := width - listWidth - 3

	left := lipgloss.NewStyle().Width(listWidth).Height(height).MaxHeight(height).Render(list.View(now, listWidth, height))

	var right string
	if current, ok := list.Current(); ok {
		right = preview.View(current, now, previewWidth, height)
	}

	divider := dimStyle.Render(strings.TrimSuffix(strings.Repeat("│\n", max(height, 1)), "\n"))

	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", divider, " ", right)
}
