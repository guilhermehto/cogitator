package ui

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const (
	pickRefreshEvery = time.Second
	previewDebounce  = 150 * time.Millisecond
	previewCapture   = 60
	previewMinWidth  = 100
	notRunningText   = "cogitator isn't running — start it in a tmux window"
)

// agentFeed is the slice of agentfeed.Client the picker uses.
type agentFeed interface {
	List(ctx context.Context) ([]agents.Agent, error)
	Focus(ctx context.Context, id, client string) error
	Seen(ctx context.Context, id string) error
}

type (
	pickListMsg struct {
		agents []agents.Agent
		err    error
	}
	pickTickMsg  struct{}
	pickFocusMsg struct{ err error }
	pickSeenMsg  struct{ err error }

	previewDebounceMsg struct{ seq int }
	previewCaptureMsg  struct {
		agentID string
		text    string
		err     error
	}
)

type pickModel struct {
	feed       agentFeed
	client     string
	list       agentList
	now        func() time.Time
	width      int
	height     int
	notRunning bool
	errText    string

	runner tmuxctl.Runner
	// previewToggled records an explicit `p` press, which overrides the
	// width-based default.
	previewToggled bool
	showPreview    bool
	// previewWant is the agent the cursor settled on; previewSeq invalidates
	// debounce ticks from earlier cursor positions.
	previewWant     string
	previewSeq      int
	previewInFlight bool
	previewAgentID  string
	previewText     string
	previewErr      string
}

func newPickModel(feed agentFeed, client string, now func() time.Time) pickModel {
	return pickModel{feed: feed, client: client, list: newAgentList(), now: now, runner: tmuxctl.DefaultRunner}
}

func (m pickModel) Init() tea.Cmd {
	return tea.Batch(m.fetch(), pickTick())
}

func pickTick() tea.Cmd {
	return tea.Tick(pickRefreshEvery, func(time.Time) tea.Msg { return pickTickMsg{} })
}

func (m pickModel) fetch() tea.Cmd {
	return func() tea.Msg {
		list, err := m.feed.List(context.Background())

		return pickListMsg{agents: list, err: err}
	}
}

func (m pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.previewToggled {
			m.showPreview = m.width >= previewMinWidth
		}

		cmd := m.syncPreview()

		return m, cmd
	case pickTickMsg:
		if m.notRunning {
			return m, nil
		}

		return m, tea.Batch(m.fetch(), pickTick())
	case pickListMsg:
		m.applyList(msg)

		cmd := m.syncPreview()

		return m, cmd
	case previewDebounceMsg:
		cmd := m.capturePreview(msg)

		return m, cmd
	case previewCaptureMsg:
		cmd := m.applyCapture(msg)

		return m, cmd
	case pickFocusMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()

			return m, nil
		}

		return m, tea.Quit
	case pickSeenMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()
		}

		return m, m.fetch()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m *pickModel) applyList(msg pickListMsg) {
	if errors.Is(msg.err, agentfeed.ErrNotRunning) {
		m.notRunning = true

		return
	}

	if msg.err != nil {
		m.errText = msg.err.Error()

		return
	}

	m.errText = ""
	m.list.SetAgents(msg.agents)
}

func (m pickModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.notRunning {
		return m, tea.Quit
	}

	if !m.list.Filtering() && key.Matches(msg, previewKey) {
		m.previewToggled = true
		m.showPreview = !m.showPreview

		cmd := m.syncPreview()

		return m, cmd
	}

	result := m.list.HandleKey(msg)
	switch result.intent {
	case agentIntentQuit:
		return m, tea.Quit
	case agentIntentJump:
		return m, m.focus(result.agentID)
	case agentIntentSeen:
		return m, m.markSeen(result.agentID)
	}

	cmd := m.syncPreview()

	return m, cmd
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

var previewKey = key.NewBinding(key.WithKeys("p"))

func (m pickModel) cursorAgent() (agents.Agent, bool) {
	list := m.list.visible()
	idx := m.list.cursorIndex(list)
	if idx < 0 {
		return agents.Agent{}, false
	}

	return list[idx], true
}

// syncPreview schedules a debounced capture when the cursor moved to another
// agent. It only mutates the pointer-free counters, so value receivers stay safe.
func (m *pickModel) syncPreview() tea.Cmd {
	if !m.showPreview {
		return nil
	}

	current, ok := m.cursorAgent()
	if !ok || current.ID == m.previewWant {
		return nil
	}

	m.previewWant = current.ID
	m.previewSeq++
	seq := m.previewSeq

	return tea.Tick(previewDebounce, func(time.Time) tea.Msg { return previewDebounceMsg{seq: seq} })
}

// capturePreview starts a capture for the settled cursor agent. A capture
// already in flight is left alone; applyCapture chases the cursor afterwards.
func (m *pickModel) capturePreview(msg previewDebounceMsg) tea.Cmd {
	if msg.seq != m.previewSeq || m.previewInFlight {
		return nil
	}

	return m.startCapture()
}

func (m *pickModel) startCapture() tea.Cmd {
	current, ok := m.cursorAgent()
	if !ok {
		return nil
	}

	target := paneTarget(current)
	if target == "" {
		m.previewAgentID, m.previewText, m.previewErr = current.ID, "", "no tmux pane"

		return nil
	}

	m.previewInFlight = true
	runner := m.runner
	id := current.ID

	return func() tea.Msg {
		text, err := tmuxctl.CapturePane(runner, target, previewCapture)

		return previewCaptureMsg{agentID: id, text: text, err: err}
	}
}

func (m *pickModel) applyCapture(msg previewCaptureMsg) tea.Cmd {
	m.previewInFlight = false
	m.previewAgentID, m.previewText, m.previewErr = msg.agentID, msg.text, ""
	if msg.err != nil {
		m.previewText, m.previewErr = "", msg.err.Error()
	}

	if current, ok := m.cursorAgent(); ok && current.ID != msg.agentID {
		return m.startCapture()
	}

	return nil
}

func (m pickModel) focus(id string) tea.Cmd {
	return func() tea.Msg {
		return pickFocusMsg{err: m.feed.Focus(context.Background(), id, m.client)}
	}
}

func (m pickModel) markSeen(id string) tea.Cmd {
	return func() tea.Msg {
		return pickSeenMsg{err: m.feed.Seen(context.Background(), id)}
	}
}

func (m pickModel) View() string {
	if m.notRunning {
		return "\n  " + wtHintStyle.Render(notRunningText) + "\n\n  " + dimStyle.Render("press any key") + "\n"
	}

	footer := dimStyle.Render("enter jump · x seen · / filter · p preview · esc close")
	if m.errText != "" {
		footer = attnErrStyle.Render(m.errText)
	}

	listHeight := max(m.height-3, 0)

	body := m.list.View(m.now(), m.width, listHeight)
	if m.showPreview {
		body = m.splitView(listHeight)
	}

	return headerStyle.Render("Agents") + "\n" + body + "\n\n" + footer
}

func (m pickModel) splitView(height int) string {
	listWidth := m.width / 2
	previewWidth := m.width - listWidth - 1

	left := lipgloss.NewStyle().Width(listWidth).MaxWidth(listWidth).Render(m.list.View(m.now(), listWidth, height))
	right := lipgloss.NewStyle().Width(previewWidth).MaxWidth(previewWidth).Render(m.previewView(previewWidth, height))

	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (m pickModel) previewView(width, height int) string {
	current, ok := m.cursorAgent()
	if !ok {
		return ""
	}

	lines := []string{previewReason(current), ""}
	switch {
	case m.previewAgentID != current.ID:
		lines = append(lines, dimStyle.Render("loading…"))
	case m.previewErr != "":
		lines = append(lines, dimStyle.Render(m.previewErr))
	default:
		lines = append(lines, tailLines(m.previewText, max(height-len(lines), 0))...)
	}

	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}

	return strings.Join(lines, "\n")
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

	return agentReason(a)
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

// RunPick shows the agent picker for the tmux client named client. It never
// takes the single-instance lock, so it cannot evict the running TUI. It
// uses the alt screen so the popup restores cleanly when it closes.
func RunPick(cfg *config.Config, logger *slog.Logger, client string) error {
	if logger == nil {
		logger = slog.Default()
	}

	m := newPickModel(agentfeed.NewClient(), client, time.Now)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		logger.Error("pick failed", "err", err)

		return err
	}

	return nil
}
