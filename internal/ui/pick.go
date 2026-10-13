package ui

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const (
	pickRefreshEvery = time.Second
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

	preview agentPreview
	// previewToggled records an explicit `p` press, which overrides the
	// width-based default.
	previewToggled bool
	showPreview    bool
}

func newPickModel(feed agentFeed, client string, now func() time.Time) pickModel {
	return pickModel{
		feed: feed, client: client, list: newAgentList(), now: now,
		preview: agentPreview{runner: tmuxctl.DefaultRunner},
	}
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
			m.showPreview = m.width >= agentSplitMinWidth
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
		current, ok := m.list.Current()
		cmd := m.preview.Debounced(msg, current, ok)

		return m, cmd
	case previewRefreshMsg:
		current, ok := m.list.Current()
		cmd := m.preview.Refresh(msg, current, ok)

		return m, cmd
	case previewCaptureMsg:
		current, ok := m.list.Current()
		cmd := m.preview.Captured(msg, current, ok)

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

var previewKey = key.NewBinding(key.WithKeys("p"))

// syncPreview follows the cursor while the preview is shown. It only mutates
// the preview's pointer-free counters, so value receivers stay safe.
func (m *pickModel) syncPreview() tea.Cmd {
	if !m.showPreview {
		m.preview.Hide()

		return nil
	}

	current, ok := m.list.Current()

	return m.preview.Follow(current, ok)
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
		body = renderAgentSplit(m.list, m.preview, m.now(), m.width, listHeight)
	}

	return headerStyle.Render("Agents") + "\n" + body + "\n\n" + footer
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
