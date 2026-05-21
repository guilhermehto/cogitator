package ui

import (
	"context"
	"fmt"
	"strings"

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
	tasksErr         error // last error from Export; nil on success
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
	if m.twAvail {
		return tea.Batch(waitSnapshot(m.snaps), loadTasksCmd(m.tw, m.cfg.TaskwarriorTimeout))
	}
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

// paneInnerWidth returns the usable inner width of the bordered tasks pane
// given the total terminal width. It subtracts 2 for the border and 2 for
// the horizontal padding, clamping to zero so callers never see a negative.
func paneInnerWidth(w int) int {
	inner := w - 2 - 2 // border (1 each side) + padding (1 each side)
	if inner < 0 {
		return 0
	}
	return inner
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// (a) Prompt mode pre-empt — evaluated before any global or pane key.
		// This ensures Esc inside a prompt clears the prompt rather than quitting.
		if m.prompt != promptIdle {
			switch m.prompt {
			case promptConfirmDelete:
				if msg.String() == "y" || msg.String() == "Y" {
					tasks := m.tasks
					cursor := m.taskCursor
					m.mutationInFlight = true
					m.prompt = promptIdle
					return m, mutateCmd(m.tw, m.cfg.TaskwarriorTimeout, "delete", func(c ClientAPI, ctx context.Context) error {
						return c.Delete(ctx, tasks[cursor].ID)
					})
				}
				// Any other key (including esc) cancels the confirm prompt.
				m.prompt = promptIdle
				return m, nil

			case promptAdd, promptEdit:
				switch msg.String() {
				case "enter":
					value := m.input.Value()
					isEdit := m.prompt == promptEdit
					tasks := m.tasks
					cursor := m.taskCursor
					m.prompt = promptIdle
					m.input.Blur()
					m.input.SetValue("")
					m.mutationInFlight = true
					// Batch the input update cmd (cursor blink teardown) with the mutation.
					_, inputCmd := m.input.Update(msg)
					var mutCmd tea.Cmd
					if isEdit {
						mutCmd = mutateCmd(m.tw, m.cfg.TaskwarriorTimeout, "modify", func(c ClientAPI, ctx context.Context) error {
							return c.Modify(ctx, tasks[cursor].ID, value)
						})
					} else {
						mutCmd = mutateCmd(m.tw, m.cfg.TaskwarriorTimeout, "add", func(c ClientAPI, ctx context.Context) error {
							return c.Add(ctx, value)
						})
					}
					return m, tea.Batch(inputCmd, mutCmd)

				case "esc":
					// Cancel prompt without quitting — must short-circuit before global quit.
					m.prompt = promptIdle
					m.input.Blur()
					m.input.SetValue("")
					return m, nil

				default:
					// Forward all other keys to the textinput so typing, backspace,
					// cursor movement, and the blink Cmd all work correctly.
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					return m, cmd
				}
			}
		}

		// (b) Global quit — only when no prompt is active.
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}

		// (c) Focus swap via Tab.
		if msg.String() == "tab" {
			if m.twAvail {
				if m.focus == focusSessions {
					m.focus = focusTasks
				} else {
					m.focus = focusSessions
				}
			}
			// No-op when !twAvail — focus stays on sessions.
			return m, nil
		}

		// (d) Sessions-focused keys.
		if m.focus == focusSessions {
			if msg.String() == "a" {
				m.recentCollapsed = !m.recentCollapsed
			}
			return m, nil
		}

		// (e) Tasks-focused keys — only when focused on tasks and no mutation in flight.
		if m.focus == focusTasks && !m.mutationInFlight {
			tasks := m.tasks
			cursor := m.taskCursor
			switch msg.String() {
			case "j", "down":
				if len(tasks) > 0 {
					m.taskCursor = min(cursor+1, len(tasks)-1)
				}
			case "k", "up":
				if len(tasks) > 0 {
					m.taskCursor = max(cursor-1, 0)
				}
			case "a":
				m.prompt = promptAdd
				m.input.SetValue("")
				focusCmd := m.input.Focus()
				return m, focusCmd
			case "e":
				if len(tasks) > 0 && cursor >= 0 {
					m.prompt = promptEdit
					m.input.SetValue(flattenTaskDSL(tasks[cursor]))
					focusCmd := m.input.Focus()
					return m, focusCmd
				}
			case "d":
				if len(tasks) > 0 && cursor >= 0 {
					m.mutationInFlight = true
					return m, mutateCmd(m.tw, m.cfg.TaskwarriorTimeout, "done", func(c ClientAPI, ctx context.Context) error {
						return c.Done(ctx, tasks[cursor].ID)
					})
				}
			case "D":
				if len(tasks) > 0 && cursor >= 0 {
					m.prompt = promptConfirmDelete
				}
			case "U":
				m.mutationInFlight = true
				return m, mutateCmd(m.tw, m.cfg.TaskwarriorTimeout, "undo", func(c ClientAPI, ctx context.Context) error {
					return c.Undo(ctx)
				})
			}
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Recompute the input width so the prompt fits inside the bordered
		// tasks pane. The prefix "edit #999: " is 11 chars; we reserve that
		// space so the cursor never overflows the pane boundary.
		const editPromptLen = len("edit #999: ")
		m.input.Width = max(0, paneInnerWidth(m.width)-editPromptLen)
	case tasksLoadedMsg:
		m.tasks = msg.tasks
		m.tasksErr = msg.err
		m.tasksLoaded = true
		// Clamp cursor into [0, len-1]. Allow -1 when the list is empty so
		// downstream key handlers can no-op cleanly without a bounds check.
		switch {
		case len(m.tasks) == 0:
			m.taskCursor = -1
		case m.taskCursor >= len(m.tasks):
			m.taskCursor = len(m.tasks) - 1
		case m.taskCursor < 0:
			m.taskCursor = 0
		}
	case taskMutationOkMsg:
		m.mutationInFlight = false
		m.lastMutationErr = nil
		m.lastMutationOp = msg.op
		// Re-fetch the task list so the pane reflects the mutation result.
		return m, loadTasksCmd(m.tw, m.cfg.TaskwarriorTimeout)
	case taskMutationFailedMsg:
		m.mutationInFlight = false
		m.lastMutationErr = msg.err
		m.lastMutationOp = msg.op
		// Do not refresh — leave the existing list intact and surface the error.
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

	var headerHint string
	if m.focus == focusSessions {
		headerHint = fmt.Sprintf("  %d live · %d recent (≤%dm)  ·  updated %s  ·  a to %s recent  ·  tab→tasks  ·  q quit",
			live, recent, recentMins, m.snap.UpdatedAt.Format("15:04:05"), toggleVerb(m.recentCollapsed))
	} else {
		headerHint = fmt.Sprintf("  %d pending  ·  a add · e edit · d done · D del · U undo · j/k move  ·  tab→sessions  ·  q quit",
			len(m.tasks))
	}
	header := titleStyle.Render("cogitator") + dimStyle.Render(headerHint)

	legend := legendLine()
	footer := unreachableFooter(m.snap.UnreachableInstances)

	// Compute reserved rows for height splitting.
	// Each section separator newline is accounted for in the join below.
	headerRows := 1
	legendRows := 1
	unreachableRows := 0
	if footer != "" {
		unreachableRows = 1
	}
	// mutationFooterRows reserved for step 9; currently 0.
	reserved := headerRows + legendRows + unreachableRows

	tasksOuterH := max(8, m.height/3)
	sessionsOuterH := max(6, m.height-tasksOuterH-reserved)

	// Choose border style based on which pane is focused.
	sessionsStyle := paneStyle
	tasksStyle := paneStyle
	if m.focus == focusSessions {
		sessionsStyle = paneFocusedStyle
	} else {
		tasksStyle = paneFocusedStyle
	}

	sessionContent := m.renderAllSessions(paneW, rows, recentByInstance)
	sessionsPane := sessionsStyle.Width(paneW).Height(sessionsOuterH).Render(sessionContent)

	tasksContent := m.renderTasksPane(tasksOuterH, paneW)
	tasksPane := tasksStyle.Width(paneW).Height(tasksOuterH).Render(tasksContent)

	parts := []string{header, sessionsPane, tasksPane, legend}
	if footer != "" {
		parts = append(parts, footer)
	}
	return strings.Join(parts, "\n")
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
