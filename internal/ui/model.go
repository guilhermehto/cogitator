package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/taskwarrior"
)

type snapshotMsg state.Snapshot

// focusArea tracks which pane currently holds keyboard focus.
// Iota order is load-bearing: zero value maps to focusSessions, keeping
// existing model{} literals in tests valid without explicit initialisation.
type focusArea int

const (
	focusSessions focusArea = iota
	focusTasks
)

// promptMode tracks whether the task input bar is active and in which mode.
// Iota order is load-bearing: zero value maps to promptIdle, keeping
// existing model{} literals in tests valid without explicit initialisation.
type promptMode int

const (
	promptIdle promptMode = iota
	promptAdd
	promptEdit
	promptConfirmDelete
)

type model struct {
	snap            state.Snapshot
	width           int
	height          int
	snaps           <-chan state.Snapshot
	recentCollapsed bool
	bellEnabled     bool
	bellSent        map[rowKey]state.Attention
	cfg             *config.Config

	// Taskwarrior fields
	tw               ClientAPI
	twAvail          bool
	tasks            []taskwarrior.TaskView
	tasksLoaded      bool
	taskCursor       int
	focus            focusArea
	prompt           promptMode
	input            textinput.Model
	lastMutationErr  error
	lastMutationOp   string
	mutationInFlight bool
}

func (m model) Init() tea.Cmd {
	return waitSnapshot(m.snaps)
}

func waitSnapshot(ch <-chan state.Snapshot) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-ch
		if !ok {
			return nil
		}
		return snapshotMsg(s)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "a":
			m.recentCollapsed = !m.recentCollapsed
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapshotMsg:
		m.snap = state.Snapshot(msg)
		next := waitSnapshot(m.snaps)
		if !m.bellEnabled {
			return m, next
		}
		fired := processBellTransitions(m.snap.Sessions, m.bellSent)
		return m, tea.Batch(next, bellCmd(len(fired)))
	}
	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "loading..."
	}

	rows, recentByInstance := visibleSessions(m.snap.Sessions, m.recentCollapsed)
	paneW := m.width - 2
	if paneW < 30 {
		paneW = 30
	}
	body := paneStyle.Width(paneW).Render(m.renderAllSessions(paneW, rows, recentByInstance))

	live, recent := 0, 0
	for _, sv := range rows {
		if sv.Source == state.SourceRecent {
			recent++
		} else {
			live++
		}
	}

	cfg := m.cfg
	if cfg == nil {
		cfg = config.Default()
	}
	recentMins := int(cfg.RecentWindow.Minutes())
	header := titleStyle.Render("cogitator") + dimStyle.Render(
		fmt.Sprintf("  %d live · %d recent (≤%dm)  ·  updated %s  ·  a to %s recent  ·  q to quit",
			live, recent, recentMins, m.snap.UpdatedAt.Format("15:04:05"), toggleVerb(m.recentCollapsed)),
	)

	legend := legendLine()
	footer := unreachableFooter(m.snap.UnreachableInstances)
	if footer == "" {
		return header + "\n" + body + "\n" + legend
	}
	return header + "\n" + body + "\n" + legend + "\n" + footer
}

func newModel(snaps <-chan state.Snapshot, cfg *config.Config, bellEnabled bool) model {
	if cfg == nil {
		cfg = config.Default()
	}

	tw := taskwarrior.NewClient()
	twAvail := tw.Available()

	ti := textinput.New()
	ti.Placeholder = "description project:foo +tag priority:H due:tomorrow"
	// Override AcceptSuggestion so Tab is never consumed by the suggestion
	// mechanism. Tab is routed by the Update loop to switch focus between
	// panes; disabling the binding here prevents the textinput from
	// intercepting it when the input bar is active.
	ti.KeyMap.AcceptSuggestion = key.NewBinding(key.WithDisabled())
	// Width is intentionally left at zero here; it is recomputed in Update
	// on the first tea.WindowSizeMsg so it tracks the actual terminal width.

	return model{
		snaps:           snaps,
		recentCollapsed: true,
		bellEnabled:     bellEnabled,
		bellSent:        map[rowKey]state.Attention{},
		cfg:             cfg,

		tw:               tw,
		twAvail:          twAvail,
		tasks:            nil,
		tasksLoaded:      false,
		taskCursor:       0,
		focus:            focusSessions,
		prompt:           promptIdle,
		input:            ti,
		lastMutationErr:  nil,
		lastMutationOp:   "",
		mutationInFlight: false,
	}
}
