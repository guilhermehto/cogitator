package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/taskwarrior"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

type snapshotMsg state.Snapshot

// tickMsg is sent by tickCmd on each relative-time refresh interval.
// It carries the current time so View() can compute fresh relative timestamps
// without calling time.Now() directly (easier to test).
type tickMsg time.Time

// tickInterval is how often the sessions pane refreshes relative timestamps
// for stopped worktree rows. One minute is sufficient because formatRelative
// only has minute-level resolution.
const tickInterval = time.Minute

// tickCmd returns a Cmd that fires a tickMsg after tickInterval and re-arms
// itself. The re-arm happens in Update so the ticker is always live while the
// model is running.
func tickCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

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
	// promptNewWorktree is active while the user types a branch name for 'n'.
	// On enter, the branch name is passed to git.AddWorktree + harness launch.
	// On esc, the prompt is cancelled without creating anything.
	promptNewWorktree
)

// launchingTimeout is how long a row stays in the optimistic "launching" state
// before the overlay is cleared and the row re-derives its real state from the
// next merge. This covers the case where the harness exits immediately or mDNS
// never advertises.
const launchingTimeout = 30 * time.Second

// launchResultMsg is returned by launchCmd / resumeCmd after the tmux
// operations complete (or fail). dir is the canonical worktree directory.
// launched reports whether a harness process was actually started or
// relaunched (vs. merely selecting an already-live window). Only a genuine
// launch warrants keeping the optimistic overlay until a session confirms
// running; a pure jump/select has nothing new to wait for.
type launchResultMsg struct {
	dir      string
	launched bool
	err      error
}

// worktreeCreatedMsg is returned by newWorktreeCmd after git.AddWorktree
// succeeds and the harness window has been opened. canonDest is the
// post-create canonical path (the overlay key).
type worktreeCreatedMsg struct {
	canonDest string
	err       error
}

// tmuxOps is the injectable seam for tmux operations used by the action Cmds.
// The zero value is nil; production code uses the real tmuxctl package-level
// functions via defaultTmuxOps. Tests inject a fake.
type tmuxOps interface {
	Available() bool
	FindWindowByDir(dir string) (tmuxctl.Target, error)
	WindowProcessAlive(target tmuxctl.Target) (bool, error)
	RelaunchInWindow(target tmuxctl.Target, argv []string) error
	EnsureWindow(dir, name string, argv []string) (tmuxctl.Target, error)
	Select(target tmuxctl.Target) error
}

// realTmuxOps delegates to the package-level tmuxctl functions.
type realTmuxOps struct{}

func (realTmuxOps) Available() bool { return tmuxctl.Available() }
func (realTmuxOps) FindWindowByDir(dir string) (tmuxctl.Target, error) {
	return tmuxctl.FindWindowByDir(dir)
}
func (realTmuxOps) WindowProcessAlive(target tmuxctl.Target) (bool, error) {
	return tmuxctl.WindowProcessAlive(target)
}
func (realTmuxOps) RelaunchInWindow(target tmuxctl.Target, argv []string) error {
	return tmuxctl.RelaunchInWindow(target, argv)
}
func (realTmuxOps) EnsureWindow(dir, name string, argv []string) (tmuxctl.Target, error) {
	return tmuxctl.EnsureWindow(dir, name, argv)
}
func (realTmuxOps) Select(target tmuxctl.Target) error { return tmuxctl.Select(target) }

// gitOps is the injectable seam for git worktree creation.
type gitOps interface {
	AddWorktree(repoPath, branch, dest string) (string, error)
}

// realGitOps delegates to the package-level git functions.
type realGitOps struct{}

func (realGitOps) AddWorktree(repoPath, branch, dest string) (string, error) {
	return git.AddWorktree(repoPath, branch, dest)
}

// harnessOps is the injectable seam for harness registry lookups.
type harnessOps interface {
	Get(kind harness.Kind) (harness.Harness, error)
}

// realHarnessOps delegates to the package-level harness registry.
type realHarnessOps struct{}

func (realHarnessOps) Get(kind harness.Kind) (harness.Harness, error) {
	return harness.DefaultRegistry.Get(kind)
}

type model struct {
	snap            state.Snapshot
	width           int
	height          int
	snaps           <-chan state.Snapshot
	recentCollapsed bool
	bellEnabled     bool
	debug           bool
	bellSent        map[rowKey]state.Attention
	cfg             *config.Config

	// Workspace / worktree fields.
	// workspaceRows is the merged list of worktree rows built by workspace.Merge
	// on each snapshot and on each tickMsg. It is nil when no repos are
	// configured (zero value is safe — View() guards on len > 0).
	workspaceRows []workspace.Row
	// sessionCursor is the index into the visible worktree rows list that
	// currently holds keyboard focus. Zero value (0) is safe.
	sessionCursor int
	// tickNow is the reference time used by the sessions pane for relative
	// timestamps. Updated on each tickMsg. Zero value causes View() to fall
	// back to time.Now().
	tickNow time.Time
	// launching is the optimistic overlay for rows that have been launched or
	// resumed but not yet confirmed running by the next merge. Keyed by
	// canonical worktree dir; value is the deadline after which the overlay
	// is cleared and the row re-derives its real state. Zero value (nil) is safe.
	launching map[string]time.Time
	// tmuxHint is a transient one-line message shown when tmux is unavailable
	// or an action cannot be performed. Cleared on the next key press.
	tmuxHint string
	// newWorktreeRepo is the repo path captured when the user presses 'n' so
	// the promptNewWorktree handler knows which repo to create the worktree in.
	newWorktreeRepo string

	// Injectable seams for tmux, git, and harness operations. Nil values are
	// replaced with the real implementations in newModel. Tests inject fakes.
	// Zero-value model{} literals in tests are safe: action Cmds guard on nil
	// and return an error result rather than panicking.
	tmux    tmuxOps
	gitOp   gitOps
	harnOp  harnessOps

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

// launchCmd performs the jump/resume tmux operations for the given row and
// returns a launchResultMsg. It selects the correct tmux action based on
// window existence and pane liveness:
//
//   - running row: FindWindowByDir → Select (jump to existing window)
//   - stopped/unknown row, window alive: Select
//   - stopped/unknown row, window dead: RelaunchInWindow → Select
//   - stopped/unknown row, no window: EnsureWindow → Select
//
// The function is a tea.Cmd (runs off the UI goroutine).
func launchCmd(ops tmuxOps, row workspace.Row, harnOp harnessOps) tea.Cmd {
	return func() tea.Msg {
		if ops == nil || !ops.Available() {
			return launchResultMsg{dir: row.Worktree, err: tmuxctl.ErrNotAvailable}
		}

		dir := row.Worktree

		// Resolve the harness argv for resume/launch.
		harnessKind := harness.Kind(row.Harness)
		if harnessKind == "" {
			harnessKind = harness.KindOpenCode
		}
		var argv []string
		if harnOp != nil {
			if h, err := harnOp.Get(harnessKind); err == nil {
				argv = h.LaunchArgv(dir, row.SessionID)
			}
		}
		if len(argv) == 0 {
			// Fallback: use opencode directly.
			argv = []string{"opencode", "--mdns", dir}
		}

		// For running rows, just find and select the window.
		if row.State == workspace.StateRunning {
			target, err := ops.FindWindowByDir(dir)
			if err != nil {
				// Window not found for a running row — best effort: no-op.
				return launchResultMsg{dir: dir, err: err}
			}
			return launchResultMsg{dir: dir, err: ops.Select(target)}
		}

		// For stopped/unknown/empty rows: check if a window already exists.
		target, findErr := ops.FindWindowByDir(dir)
		if findErr == nil {
			// Window exists — check if the process is alive.
			alive, aliveErr := ops.WindowProcessAlive(target)
			if aliveErr != nil {
				// Cannot determine liveness — try to select anyway.
				return launchResultMsg{dir: dir, err: ops.Select(target)}
			}
			if alive {
				// Process is alive — just select.
				return launchResultMsg{dir: dir, err: ops.Select(target)}
			}
			// Process is dead — relaunch then select.
			if err := ops.RelaunchInWindow(target, argv); err != nil {
				return launchResultMsg{dir: dir, err: err}
			}
			return launchResultMsg{dir: dir, launched: true, err: ops.Select(target)}
		}

		// No window exists — create one and select it.
		windowName := filepath.Base(dir)
		if row.Branch != "" {
			windowName = filepath.Base(row.Repo) + "/" + row.Branch
		}
		newTarget, err := ops.EnsureWindow(dir, windowName, argv)
		if err != nil {
			return launchResultMsg{dir: dir, err: err}
		}
		return launchResultMsg{dir: dir, launched: true, err: ops.Select(newTarget)}
	}
}

// newWorktreeCmd creates a git worktree for branch under repoPath, then
// launches the harness in a new tmux window. Returns worktreeCreatedMsg with
// the canonical post-create dest (the overlay key).
func newWorktreeCmd(ops tmuxOps, gitOp gitOps, harnOp harnessOps, repoPath, branch, harnessKind string) tea.Cmd {
	return func() tea.Msg {
		if ops == nil || !ops.Available() {
			return worktreeCreatedMsg{err: tmuxctl.ErrNotAvailable}
		}

		// Derive the destination path as a sibling of the repo named after the branch.
		// e.g. /home/user/myrepo → /home/user/myrepo-branch
		dest := filepath.Join(filepath.Dir(repoPath), filepath.Base(repoPath)+"-"+branch)

		var addFn func(string, string, string) (string, error)
		if gitOp != nil {
			addFn = gitOp.AddWorktree
		} else {
			addFn = git.AddWorktree
		}

		canonDest, err := addFn(repoPath, branch, dest)
		if err != nil {
			return worktreeCreatedMsg{err: err}
		}

		// Resolve harness argv.
		kind := harness.Kind(harnessKind)
		if kind == "" {
			kind = harness.KindOpenCode
		}
		var argv []string
		if harnOp != nil {
			if h, hErr := harnOp.Get(kind); hErr == nil {
				argv = h.LaunchArgv(canonDest, "")
			}
		}
		if len(argv) == 0 {
			argv = []string{"opencode", "--mdns", canonDest}
		}

		windowName := filepath.Base(repoPath) + "/" + branch
		target, err := ops.EnsureWindow(canonDest, windowName, argv)
		if err != nil {
			return worktreeCreatedMsg{canonDest: canonDest, err: err}
		}
		if err := ops.Select(target); err != nil {
			return worktreeCreatedMsg{canonDest: canonDest, err: err}
		}
		return worktreeCreatedMsg{canonDest: canonDest}
	}
}

func (m model) Init() tea.Cmd {
	// tickCmd keeps relative timestamps in the sessions pane fresh. It fires
	// once per minute and re-arms itself in Update. The tick runs regardless
	// of whether repos are configured — it is cheap and avoids a conditional
	// that would complicate Init.
	tick := tickCmd()
	if m.twAvail {
		return tea.Batch(waitSnapshot(m.snaps), loadTasksCmd(m.tw, m.cfg.TaskwarriorTimeout), tick)
	}
	return tea.Batch(waitSnapshot(m.snaps), tick)
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

			case promptNewWorktree:
				// Branch-name prompt for 'n' (new worktree). On enter, create
				// the worktree and launch the harness. On esc, cancel.
				switch msg.String() {
				case "enter":
					branch := strings.TrimSpace(m.input.Value())
					repoPath := m.newWorktreeRepo
					m.prompt = promptIdle
					m.input.Blur()
					m.input.SetValue("")
					m.newWorktreeRepo = ""
					_, inputCmd := m.input.Update(msg)
					if branch == "" || repoPath == "" {
						// Nothing to do — cancelled effectively.
						return m, inputCmd
					}
					// Determine harness kind from workspace config.
					harnessKind := "opencode"
					if wsCfg, err := workspace.LoadConfig(); err == nil && wsCfg.DefaultHarness != "" {
						harnessKind = wsCfg.DefaultHarness
					}
					actionCmd := newWorktreeCmd(m.tmux, m.gitOp, m.harnOp, repoPath, branch, harnessKind)
					return m, tea.Batch(inputCmd, actionCmd)

				case "esc":
					m.prompt = promptIdle
					m.input.Blur()
					m.input.SetValue("")
					m.newWorktreeRepo = ""
					return m, nil

				default:
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
			// Clear any transient tmux hint on any key press.
			m.tmuxHint = ""

			switch msg.String() {
			case "a":
				m.recentCollapsed = !m.recentCollapsed
			case "j", "down":
				if n := len(m.workspaceRows); n > 0 {
					m.sessionCursor = min(m.sessionCursor+1, n-1)
				}
			case "k", "up":
				if n := len(m.workspaceRows); n > 0 {
					m.sessionCursor = max(m.sessionCursor-1, 0)
				}

			case "enter":
				// Jump to a running agent or resume a stopped one.
				// Guard: tmux must be available.
				tmuxAvail := m.tmux != nil && m.tmux.Available()
				if !tmuxAvail {
					m.tmuxHint = "tmux not available — start cogitator inside a tmux session to use jump/resume"
					return m, nil
				}
				if len(m.workspaceRows) == 0 {
					return m, nil
				}
				row := m.workspaceRows[m.sessionCursor]
				dir := row.Worktree

				// If the row is already in the launching overlay, jump/no-op
				// rather than launching again.
				if _, launching := m.launching[dir]; launching {
					// Try to select the window if it exists; otherwise no-op.
					return m, launchCmd(m.tmux, row, m.harnOp)
				}

				// Missing rows cannot be resumed (directory absent from disk).
				if row.State == workspace.StateMissing {
					m.tmuxHint = "worktree directory is missing — cannot resume"
					return m, nil
				}

				// Set optimistic launching overlay.
				deadline := time.Now().Add(launchingTimeout)
				if m.launching == nil {
					m.launching = make(map[string]time.Time)
				}
				m.launching[dir] = deadline

				return m, launchCmd(m.tmux, row, m.harnOp)

			case "n":
				// New worktree: collect a branch name via prompt.
				tmuxAvail := m.tmux != nil && m.tmux.Available()
				if !tmuxAvail {
					m.tmuxHint = "tmux not available — start cogitator inside a tmux session to create worktrees"
					return m, nil
				}
				if len(m.workspaceRows) == 0 {
					return m, nil
				}
				row := m.workspaceRows[m.sessionCursor]
				// Determine the repo path: use row.Repo if set, else row.Worktree.
				repoPath := row.Repo
				if repoPath == "" {
					repoPath = row.Worktree
				}
				if repoPath == "" {
					return m, nil
				}
				m.newWorktreeRepo = repoPath
				m.prompt = promptNewWorktree
				m.input.Placeholder = "branch name"
				m.input.SetValue("")
				focusCmd := m.input.Focus()
				return m, focusCmd
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
			case "s":
				// Toggle: running task → stop, idle task → start.
				// We branch on Start.IsZero() rather than tracking a flag so
				// the action stays consistent with whatever Export last
				// reported, even if the row was mutated out-of-band.
				if len(tasks) > 0 && cursor >= 0 {
					m.mutationInFlight = true
					if tasks[cursor].Start.IsZero() {
						return m, mutateCmd(m.tw, m.cfg.TaskwarriorTimeout, "start", func(c ClientAPI, ctx context.Context) error {
							return c.Start(ctx, tasks[cursor].ID)
						})
					}
					return m, mutateCmd(m.tw, m.cfg.TaskwarriorTimeout, "stop", func(c ClientAPI, ctx context.Context) error {
						return c.Stop(ctx, tasks[cursor].ID)
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
		// Sort once at load time so m.tasks[m.taskCursor] is always the
		// highlighted row. Sorting in the render path instead would
		// desynchronise the cursor index from action dispatch (done, stop,
		// delete, etc. all read m.tasks[m.taskCursor]).
		m.tasks = sortedTasks(msg.tasks)
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

	case launchResultMsg:
		// A launch/resume Cmd completed.
		//   - error: clear the overlay so the row re-derives its real state.
		//   - success + launched: a harness process was (re)started; keep the
		//     overlay until the next merge confirms the row is running.
		//   - success + !launched: we only selected an already-live window
		//     (jump/resume of an inactive session). There is no new agent to
		//     wait for, so clear the overlay immediately — otherwise the row
		//     would sit in "launching…" until the 30s timeout.
		if msg.err != nil || !msg.launched {
			if m.launching != nil {
				delete(m.launching, msg.dir)
			}
		}
		if msg.err != nil {
			// Surface the error as a transient hint.
			m.tmuxHint = fmt.Sprintf("launch error: %v", msg.err)
		}
		return m, nil

	case worktreeCreatedMsg:
		// A new-worktree Cmd completed. On success, set the launching overlay
		// keyed by the post-create canonical dest. On error, clear any overlay
		// and surface the error.
		if msg.err != nil {
			if msg.canonDest != "" && m.launching != nil {
				delete(m.launching, msg.canonDest)
			}
			m.tmuxHint = fmt.Sprintf("new worktree error: %v", msg.err)
		} else if msg.canonDest != "" {
			deadline := time.Now().Add(launchingTimeout)
			if m.launching == nil {
				m.launching = make(map[string]time.Time)
			}
			m.launching[msg.canonDest] = deadline
		}
		return m, nil

	case snapshotMsg:
		m.snap = state.Snapshot(msg)
		// Rebuild workspace rows on every snapshot so running/stopped state
		// stays in sync with live session changes.
		m.workspaceRows = buildWorkspaceRows(m.snap, m.cfg)
		// Clear launching overlay for any dir that is now confirmed running.
		if m.launching != nil {
			for dir := range m.launching {
				for _, row := range m.workspaceRows {
					if row.Worktree == dir && row.State == workspace.StateRunning {
						delete(m.launching, dir)
						break
					}
				}
			}
		}
		// Clamp cursor so it never points past the end of the new row list.
		if n := len(m.workspaceRows); n == 0 {
			m.sessionCursor = 0
		} else if m.sessionCursor >= n {
			m.sessionCursor = n - 1
		}
		next := waitSnapshot(m.snaps)
		if !m.bellEnabled {
			return m, next
		}
		fired := processBellTransitions(m.snap.Sessions, m.bellSent)
		return m, tea.Batch(next, bellCmd(len(fired)))

	case tickMsg:
		// Re-arm the ticker and record the current time so View() can render
		// fresh relative timestamps without calling time.Now() on every frame.
		m.tickNow = time.Time(msg)
		// Expire any launching overlays whose deadline has passed. The row
		// will re-derive its real state from the next merge.
		now := time.Time(msg)
		if m.launching != nil {
			for dir, deadline := range m.launching {
				if now.After(deadline) {
					delete(m.launching, dir)
				}
			}
		}
		return m, tickCmd()
	}
	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "loading..."
	}

	cfg := m.cfg
	if cfg == nil {
		cfg = config.Default()
	}
	rows, recentByInstance := visibleSessions(m.snap.Sessions, m.recentCollapsed, m.snap.UpdatedAt, cfg.InactiveHideAfter)
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

	recentMins := int(cfg.RecentWindow.Minutes())

	var headerHint string
	if m.focus == focusSessions {
		headerHint = fmt.Sprintf("  %d live · %d recent (≤%dm)  ·  updated %s  ·  a to %s recent  ·  tab→tasks  ·  q quit",
			live, recent, recentMins, m.snap.UpdatedAt.Format("15:04:05"), toggleVerb(m.recentCollapsed))
	} else {
		headerHint = fmt.Sprintf("  %d pending  ·  a add · e edit · s start/stop · d done · D del · U undo · j/k move  ·  tab→sessions  ·  q quit",
			len(m.tasks))
	}
	header := titleStyle.Render("cogitator") + dimStyle.Render(headerHint)

	legend := legendLine(m.width)
	// The unreachable footer is gated behind --debug because transient
	// "instance unreachable" warnings (laptop sleep, network blips,
	// short-lived opencode processes) are noisy during normal operation
	// and don't require user action.
	var footer string
	if m.debug {
		footer = unreachableFooter(m.snap.UnreachableInstances)
	}
	mutationFooter := taskwarriorErrorFooter(m.lastMutationOp, m.lastMutationErr)

	// Compute reserved rows for height splitting.
	// Each section separator newline is accounted for in the join below.
	headerRows := 1
	legendRows := 1
	unreachableRows := 0
	if footer != "" {
		unreachableRows = 1
	}
	mutationFooterRows := 0
	if mutationFooter != "" {
		mutationFooterRows = 1
	}
	reserved := headerRows + legendRows + unreachableRows + mutationFooterRows

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

	// lipgloss .Height(h) sets the CONTENT height; the rounded border adds
	// 2 more rows (top + bottom) to the rendered output. Subtract 2 so each
	// pane's total rendered height matches the split reservation. Without
	// this, the View() output is 4 rows taller than the terminal and the
	// alt-screen crops the top (header, Sessions title, column header).
	sessionsInnerH := max(1, sessionsOuterH-2)
	tasksInnerH := max(1, tasksOuterH-2)

	// When repos are configured, render the merged worktree view. Otherwise
	// fall back to the live-only path so --status/--demo and unconfigured
	// installs render exactly as before.
	var sessionContent string
	if len(m.workspaceRows) > 0 {
		now := m.tickNow
		if now.IsZero() {
			now = time.Now()
		}
		sessionContent = m.renderWorkspaceRows(paneW, m.workspaceRows, m.sessionCursor, now)
	} else {
		sessionContent = m.renderAllSessions(paneW, rows, recentByInstance)
	}
	sessionsPane := sessionsStyle.Width(paneW).Height(sessionsInnerH).Render(sessionContent)

	tasksContent := m.renderTasksPane(tasksOuterH, paneW)
	tasksPane := tasksStyle.Width(paneW).Height(tasksInnerH).Render(tasksContent)

	parts := []string{header, sessionsPane, tasksPane, legend}
	if footer != "" {
		parts = append(parts, footer)
	}
	if mutationFooter != "" {
		parts = append(parts, mutationFooter)
	}
	return strings.Join(parts, "\n")
}

// newModel constructs the TUI model. tw is injected so demo / test paths can
// substitute a synthetic ClientAPI without shelling out to the `task` binary;
// production callers pass taskwarrior.NewClient(). If tw is nil, the Tasks
// pane is suppressed (twAvail=false). debug enables diagnostic UI elements
// such as the unreachable-instance footer.
func newModel(snaps <-chan state.Snapshot, cfg *config.Config, bellEnabled, debug bool, tw ClientAPI) model {
	if cfg == nil {
		cfg = config.Default()
	}

	twAvail := tw != nil && tw.Available()

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
		debug:           debug,
		bellSent:        map[rowKey]state.Attention{},
		cfg:             cfg,

		// Inject real implementations for tmux, git, and harness operations.
		// Tests can override these fields with fakes after construction.
		tmux:   realTmuxOps{},
		gitOp:  realGitOps{},
		harnOp: realHarnessOps{},

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

// buildWorkspaceRows loads workspace config, roster, git worktrees, and tmux
// window dirs, then calls workspace.Merge to produce the merged row list. It
// is called on every snapshot update so the list stays in sync with live
// session changes.
//
// tmuxDirs is gathered from tmuxctl.ListCogDirs() when tmux is available.
// When tmux is unavailable or the call fails, an empty map is used (safe
// fallback: unknown rows render as stopped instead of unknown).
//
// Returns nil when no repos are configured (zero-value safe for callers).
func buildWorkspaceRows(snap state.Snapshot, cfg *config.Config) []workspace.Row {
	wsCfg, err := workspace.LoadConfig()
	if err != nil || len(wsCfg.Repos) == 0 {
		// No repos configured or config unreadable — live-only path.
		return nil
	}

	// Build worktrees-by-repo map. Errors from individual repos are non-fatal:
	// a repo that can't be listed (e.g. missing git) yields an empty slice,
	// which Merge renders as a header-only row.
	worktreesByRepo := make(map[string][]git.Worktree, len(wsCfg.Repos))
	for _, repo := range wsCfg.Repos {
		if repo.Missing {
			continue
		}
		wts, err := git.ListWorktrees(repo.Path)
		if err != nil {
			// Non-fatal: render the repo with no worktrees.
			continue
		}
		worktreesByRepo[repo.Path] = wts
	}

	roster, err := workspace.Load()
	if err != nil {
		// Non-fatal: proceed with an empty roster.
		roster = map[string]workspace.RosterEntry{}
	}

	// Pre-filter to top-level sessions only (shouldHideSubagent is private to
	// the ui package; workspace.Merge trusts the caller to do this filtering).
	var liveTopLevel []state.SessionView
	for _, sv := range snap.Sessions {
		if !shouldHideSubagent(sv) && sv.ParentID == "" {
			liveTopLevel = append(liveTopLevel, sv)
		}
	}

	// Gather tmux window dirs so Merge can classify rows as StateUnknown when
	// a tmux window exists for a dir whose harness lacks LiveStatus.
	// Non-fatal: if tmux is unavailable or the call fails, use an empty map.
	var tmuxDirs map[string]bool
	if tmuxctl.Available() {
		if dirs, err := tmuxctl.ListCogDirs(); err == nil {
			tmuxDirs = dirs
		}
	}

	return workspace.Merge(wsCfg.Repos, worktreesByRepo, roster, liveTopLevel, tmuxDirs)
}
