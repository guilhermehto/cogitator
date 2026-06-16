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
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/taskwarrior"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

type snapshotMsg state.Snapshot

// workspaceRowsMsg is returned by buildWorkspaceRowsCmd when the background
// workspace-row build completes. It carries the merged row list and the
// resolved tmux launch mode so the Update handler can apply them atomically.
type workspaceRowsMsg struct {
	rows       []workspace.Row
	launchMode tmuxctl.LaunchMode
}

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
	// promptFetchBranch is active while the user types a branch name for 'F'.
	// It mirrors promptNewWorktree but, on enter, the branch is fetched from
	// origin and checked out (git.FetchAndAddWorktree) instead of created fresh.
	// The distinction is carried forward via model.worktreeFromRemote.
	promptFetchBranch
	// promptAddRepo is active while the embedded "add repo" fuzzy finder is
	// open ('A'). cogitator scans $HOME for git repositories; the user filters
	// the discovered list with the shared text input and selects one to add.
	promptAddRepo
	// promptChooseHarness is shown after the user types a branch name for 'n'
	// and presses enter. It presents the registered harness kinds as a list;
	// the user moves the cursor with up/down and confirms with enter. On esc
	// the whole new-worktree flow is cancelled. The default cursor position is
	// the index of wsCfg.DefaultHarness (or opencode when unset).
	promptChooseHarness
	// promptConfirmDeleteWorktree is the FIRST of two confirmations for deleting
	// a worktree ('D'). 'y' advances to promptConfirmDeleteWorktree2; any other
	// key cancels. The merge status of the branch is shown so the user knows
	// whether removing the worktree would leave unmerged commits behind.
	promptConfirmDeleteWorktree
	// promptConfirmDeleteWorktree2 is the SECOND confirmation. Its default is
	// cancel: only an explicit 'y' proceeds with deletion; every other key
	// (including esc/enter) aborts. This double-gate guards a destructive,
	// irreversible action.
	promptConfirmDeleteWorktree2
	// promptConfirmRemoveRepo confirms untracking the repo under the cursor
	// ('R'). 'y' proceeds; any other key (including esc) cancels. A single
	// gate is enough: removal only forgets the repo from cogitator's config —
	// the repo and its worktrees stay on disk and can be re-added with 'A'.
	promptConfirmRemoveRepo
)

// launchResultMsg is returned by launchCmd / resumeCmd after the tmux
// operations complete (or fail). dir is the canonical worktree directory.
// launched reports whether a harness process was actually started or
// relaunched (vs. merely selecting an already-live window).
type launchResultMsg struct {
	dir      string
	launched bool
	err      error
	// provider, instanceID, sessionID identify the session that was selected
	// so the Update handler can mark it viewed (clearing any AttnFinished
	// badge). Empty when the row had no associated session.
	provider   string
	instanceID string
	sessionID  string
}

// worktreeCreatedMsg is returned by newWorktreeCmd after git.AddWorktree
// succeeds and the harness window has been opened. canonDest is the
// post-create canonical path (the overlay key). harnessKind is the harness
// that was launched so the handler can write a create-time roster entry.
type worktreeCreatedMsg struct {
	canonDest   string
	harnessKind string
	err         error
}

// mergeStatusMsg carries the result of an async branch merge-status probe used
// to annotate the worktree-delete confirmation. path is the canonical worktree
// dir the status was computed for, so a stale result for a since-cancelled or
// retargeted prompt can be ignored.
type mergeStatusMsg struct {
	path  string
	state git.MergeState
	base  string
}

// worktreeDeletedMsg is returned by deleteWorktreeCmd after `git worktree
// remove` completes. path is the canonical worktree dir; err is non-nil when
// git refused (e.g. uncommitted changes) so the row is preserved and the error
// surfaced.
type worktreeDeletedMsg struct {
	path string
	err  error
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
	EnsureWindowMode(dir, name string, argv []string, mode tmuxctl.LaunchMode) (tmuxctl.Target, error)
	Select(target tmuxctl.Target) error
	SelectSession(target tmuxctl.Target) error
	KillWindow(target tmuxctl.Target) error
	KillSession(target tmuxctl.Target) error
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
func (realTmuxOps) EnsureWindowMode(dir, name string, argv []string, mode tmuxctl.LaunchMode) (tmuxctl.Target, error) {
	return tmuxctl.EnsureWindowMode(dir, name, argv, mode)
}
func (realTmuxOps) Select(target tmuxctl.Target) error { return tmuxctl.Select(target) }
func (realTmuxOps) SelectSession(target tmuxctl.Target) error {
	return tmuxctl.SelectSession(target)
}
func (realTmuxOps) KillWindow(target tmuxctl.Target) error { return tmuxctl.KillWindow(target) }
func (realTmuxOps) KillSession(target tmuxctl.Target) error {
	return tmuxctl.KillSession(target)
}

// viewMarker is the injectable seam through which the model tells the state
// store a session has been viewed by the user (clearing AttnFinished).
// *state.Store satisfies it. The interface keeps internal/ui from importing a
// concrete store dependency into every test.
type viewMarker interface {
	MarkViewed(providerKind harness.Kind, instanceID, sessionID string)
}

// launchModeFor maps the workspace config's LaunchMode to the tmuxctl mode used
// by the action Cmds. LaunchSession maps to ModeSession; everything else
// (including the empty default) maps to ModeWindow.
func launchModeFor(m workspace.LaunchMode) tmuxctl.LaunchMode {
	if m == workspace.LaunchSession {
		return tmuxctl.ModeSession
	}
	return tmuxctl.ModeWindow
}

// gitOps is the injectable seam for git worktree operations.
type gitOps interface {
	AddWorktree(repoPath, branch, dest string) (string, error)
	FetchAndAddWorktree(repoPath, branch, dest string) (string, error)
	RemoveWorktree(repoPath, worktreePath string) error
	BranchMergeStatus(repoPath, branch string) (git.MergeState, string)
}

// realGitOps delegates to the package-level git functions.
type realGitOps struct{}

func (realGitOps) AddWorktree(repoPath, branch, dest string) (string, error) {
	return git.AddWorktree(repoPath, branch, dest)
}

func (realGitOps) FetchAndAddWorktree(repoPath, branch, dest string) (string, error) {
	return git.FetchAndAddWorktree(repoPath, branch, dest)
}

func (realGitOps) RemoveWorktree(repoPath, worktreePath string) error {
	return git.RemoveWorktree(repoPath, worktreePath)
}

func (realGitOps) BranchMergeStatus(repoPath, branch string) (git.MergeState, string) {
	return git.BranchMergeStatus(repoPath, branch)
}

// harnessOps is the injectable seam for harness registry lookups.
type harnessOps interface {
	Get(kind harness.Kind) (harness.Harness, error)
	// Kinds returns all registered harness kinds. Callers that need a stable
	// order must sort the result.
	Kinds() []harness.Kind
}

// realHarnessOps delegates to the package-level harness registry.
type realHarnessOps struct{}

func (realHarnessOps) Get(kind harness.Kind) (harness.Harness, error) {
	return harness.DefaultRegistry.Get(kind)
}

func (realHarnessOps) Kinds() []harness.Kind {
	return harness.DefaultRegistry.Kinds()
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
	// tmuxHint is a transient one-line message shown when tmux is unavailable
	// or an action cannot be performed. Cleared on the next key press.
	tmuxHint string
	// newWorktreeRepo is the repo path captured when the user presses 'n' so
	// the promptNewWorktree handler knows which repo to create the worktree in.
	newWorktreeRepo string
	// newWorktreeBranch is the branch name typed in promptNewWorktree, carried
	// forward to promptChooseHarness so the chooser can dispatch newWorktreeCmd.
	newWorktreeBranch string
	// worktreeFromRemote records whether the in-progress new-worktree flow
	// should fetch the branch from origin ('F') rather than create a fresh
	// branch off the base ('n'). It is set when the flow begins, read by the
	// harness chooser when it dispatches the create Cmd, and reset when the flow
	// completes or is cancelled.
	worktreeFromRemote bool
	// harnessChooserKinds is the ordered list of harness kinds shown in the
	// promptChooseHarness list. Populated when entering the chooser.
	harnessChooserKinds []harness.Kind
	// harnessChooserCursor is the index into harnessChooserKinds of the
	// currently highlighted choice. Defaults to the index of DefaultHarness
	// (or opencode when unset).
	harnessChooserCursor int
	// rosterUpserts is the channel used to inject create-time roster entries
	// into the recorder without calling workspace.Save directly. Nil when the
	// recorder is not wired (e.g. in tests that don't need roster writes).
	rosterUpserts chan<- workspace.RosterEntry
	// viewMarker reports a session as viewed by the user (jump/resume) so the
	// store can clear its AttnFinished badge. nil in tests that don't exercise
	// the launch path; the handler guards on nil.
	viewMarker viewMarker
	// deleteTarget is the worktree row captured when the user presses 'D' to
	// begin the two-step delete confirmation. Zero value when no delete is in
	// progress. Cleared on cancel and on dispatch of the delete Cmd.
	deleteTarget workspace.Row
	// deleteMergeInfo is the human-readable branch merge status shown in the
	// delete confirmation prompts (e.g. "merged into main"). Empty until the
	// async probe (mergeStatusCmd) returns; rendered as "checking…" meanwhile.
	deleteMergeInfo string
	// removeRepoTarget is the canonical repo path captured when the user
	// presses 'R' to untrack the repo under the cursor. Empty when no removal
	// is in progress; cleared on cancel and on dispatch of removeRepoCmd.
	removeRepoTarget string
	// launchMode is the resolved tmux launch mode (window vs session) read from
	// workspace config. Refreshed on each buildWorkspaceRows so config edits
	// take effect without a restart. Zero value (ModeWindow) is safe.
	launchMode tmuxctl.LaunchMode
	// rowsBuilding is true while a background buildWorkspaceRowsCmd is in
	// flight. Only one build runs at a time; a second snapshotMsg while a
	// build is in flight sets rowsDirty instead of starting a second build.
	rowsBuilding bool
	// rowsDirty is set when a snapshotMsg arrives while rowsBuilding is true.
	// When the in-flight build completes, one follow-up build is dispatched
	// using the latest m.snap at that moment (coalesced, not stale).
	rowsDirty bool
	// pendingDeletes tracks worktrees whose row was optimistically removed
	// from the table the moment deletion was confirmed, keyed by canonical
	// worktree path. The stored Row lets a failed deletion restore the row.
	// While a path is pending, workspaceRowsMsg filters it out so an in-flight
	// snapshot rebuild (the worktree still exists on disk until git finishes)
	// cannot resurrect the row. Entries clear on deletion success or failure.
	pendingDeletes map[string]workspace.Row

	// Repo finder ('A') state, meaningful only while prompt == promptAddRepo.
	// repoFinderScanning is true between opening the finder and the scan result
	// arriving. repoFinderAll is the discovered, not-yet-configured repo set;
	// repoFinderMatches is its current fuzzy-filtered view (what is rendered);
	// repoFinderCursor indexes repoFinderMatches; repoFinderErr holds a scan
	// error to surface in the finder body. Zero values are safe (finder closed).
	repoFinderScanning bool
	repoFinderAll      []string
	repoFinderMatches  []string
	repoFinderCursor   int
	repoFinderErr      string

	// Injectable seams for tmux, git, and harness operations. Nil values are
	// replaced with the real implementations in newModel. Tests inject fakes.
	// Zero-value model{} literals in tests are safe: action Cmds guard on nil
	// and return an error result rather than panicking.
	tmux   tmuxOps
	gitOp  gitOps
	harnOp harnessOps

	// Taskwarrior fields
	tw               ClientAPI
	twAvail          bool
	tasksActive      bool
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
//   - window alive: Select
//   - window dead: RelaunchInWindow → Select
//   - no window: EnsureWindow → Select
//
// The function is a tea.Cmd (runs off the UI goroutine).
func launchCmd(ops tmuxOps, row workspace.Row, harnOp harnessOps, mode tmuxctl.LaunchMode) tea.Cmd {
	inner := launchInner(ops, row, harnOp, mode)
	return func() tea.Msg {
		res := inner()
		// Stamp the session identity so the Update handler can mark it viewed
		// (clearing AttnFinished) when the select succeeds.
		res.provider = row.Harness
		res.sessionID = row.SessionID
		return res
	}
}

func launchInner(ops tmuxOps, row workspace.Row, harnOp harnessOps, mode tmuxctl.LaunchMode) func() launchResultMsg {
	return func() launchResultMsg {
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

		// selectTarget moves the client to target. In session mode it switches
		// to the session and lets tmux restore its last-active window (so you
		// land where you left off, not always the worktree's first window).
		// In window mode it focuses the exact tagged window.
		selectTarget := func(target tmuxctl.Target) error {
			if mode == tmuxctl.ModeSession {
				return ops.SelectSession(target)
			}
			return ops.Select(target)
		}

		// Check tmux directly instead of trusting the row state. A running row can
		// be stale if the opencode process or tmux target died before the next
		// discovery update, so use the same recovery path for all resumable rows.
		target, findErr := ops.FindWindowByDir(dir)
		if findErr == nil {
			// Window exists — check if the process is alive.
			alive, aliveErr := ops.WindowProcessAlive(target)
			if aliveErr != nil {
				// Cannot determine liveness — try to select anyway.
				return launchResultMsg{dir: dir, err: selectTarget(target)}
			}
			if alive {
				// Process is alive — just select.
				return launchResultMsg{dir: dir, err: selectTarget(target)}
			}
			// Process is dead — relaunch then select.
			if err := ops.RelaunchInWindow(target, argv); err != nil {
				return launchResultMsg{dir: dir, err: err}
			}
			return launchResultMsg{dir: dir, launched: true, err: selectTarget(target)}
		}

		// No window exists — create one and select it.
		windowName := filepath.Base(dir)
		if row.Branch != "" {
			windowName = filepath.Base(row.Repo) + "/" + row.Branch
		}
		newTarget, err := ops.EnsureWindowMode(dir, windowName, argv, mode)
		if err != nil {
			return launchResultMsg{dir: dir, err: err}
		}
		return launchResultMsg{dir: dir, launched: true, err: selectTarget(newTarget)}
	}
}

// worktreeAddFn selects the worktree-creation function for newWorktreeCmd: the
// fetch-then-checkout path when fromRemote is true, the create-fresh-branch path
// otherwise. It prefers the injected gitOp seam and falls back to the
// package-level git functions when gitOp is nil (zero-value model in tests).
func worktreeAddFn(gitOp gitOps, fromRemote bool) func(string, string, string) (string, error) {
	switch {
	case gitOp != nil && fromRemote:
		return gitOp.FetchAndAddWorktree
	case gitOp != nil:
		return gitOp.AddWorktree
	case fromRemote:
		return git.FetchAndAddWorktree
	default:
		return git.AddWorktree
	}
}

// newWorktreeCmd creates a git worktree for branch under repoPath, then
// launches the harness in a new tmux window. Returns worktreeCreatedMsg with
// the canonical post-create dest (the overlay key).
//
// When fromRemote is true the branch is fetched from origin and checked out
// (git.FetchAndAddWorktree); otherwise a fresh branch is created off the
// current HEAD (git.AddWorktree). Both paths share the same launch flow.
func newWorktreeCmd(ops tmuxOps, gitOp gitOps, harnOp harnessOps, repoPath, branch, harnessKind string, mode tmuxctl.LaunchMode, fromRemote bool) tea.Cmd {
	return func() tea.Msg {
		if ops == nil || !ops.Available() {
			return worktreeCreatedMsg{err: tmuxctl.ErrNotAvailable}
		}

		// Derive the destination path as a sibling of the repo named after the branch.
		// e.g. /home/user/myrepo → /home/user/myrepo-branch
		dest := filepath.Join(filepath.Dir(repoPath), filepath.Base(repoPath)+"-"+branch)

		addFn := worktreeAddFn(gitOp, fromRemote)

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
		target, err := ops.EnsureWindowMode(canonDest, windowName, argv, mode)
		if err != nil {
			return worktreeCreatedMsg{canonDest: canonDest, harnessKind: string(kind), err: err}
		}
		if err := ops.Select(target); err != nil {
			return worktreeCreatedMsg{canonDest: canonDest, harnessKind: string(kind), err: err}
		}
		return worktreeCreatedMsg{canonDest: canonDest, harnessKind: string(kind)}
	}
}

// mergeStatusCmd probes whether branch has been merged into the repo's default
// branch, off the UI goroutine, and reports the result as a mergeStatusMsg
// tagged with path so the handler can correlate it to the active prompt.
func mergeStatusCmd(gitOp gitOps, repo, branch, path string) tea.Cmd {
	return func() tea.Msg {
		var statusFn func(string, string) (git.MergeState, string)
		if gitOp != nil {
			statusFn = gitOp.BranchMergeStatus
		} else {
			statusFn = git.BranchMergeStatus
		}
		stateVal, base := statusFn(repo, branch)
		return mergeStatusMsg{path: path, state: stateVal, base: base}
	}
}

// deleteWorktreeCmd removes the worktree at path (belonging to repo) via git,
// then best-effort closes its attached tmux window/session so no dead pane is
// left pointing at a missing directory. The git removal is the only step that
// can fail the operation; tmux cleanup is advisory and its error is ignored.
func deleteWorktreeCmd(ops tmuxOps, gitOp gitOps, repo, path string, mode tmuxctl.LaunchMode) tea.Cmd {
	return func() tea.Msg {
		var removeFn func(string, string) error
		if gitOp != nil {
			removeFn = gitOp.RemoveWorktree
		} else {
			removeFn = git.RemoveWorktree
		}
		if err := removeFn(repo, path); err != nil {
			return worktreeDeletedMsg{path: path, err: err}
		}

		// Best-effort cleanup of the worktree's tmux attachment. Failures here
		// do not undo the successful removal — the directory is already gone.
		if ops != nil && ops.Available() {
			if target, err := ops.FindWindowByDir(path); err == nil {
				if mode == tmuxctl.ModeSession {
					_ = ops.KillSession(target)
				} else {
					_ = ops.KillWindow(target)
				}
			}
		}
		return worktreeDeletedMsg{path: path}
	}
}

// removeWorktreeRow optimistically drops target's row from the visible table
// and records it in pendingDeletes so a failed deletion can restore it. The
// session cursor is clamped so it never points past the shortened list.
func (m *model) removeWorktreeRow(target workspace.Row) {
	if m.pendingDeletes == nil {
		m.pendingDeletes = map[string]workspace.Row{}
	}
	m.pendingDeletes[target.Worktree] = target
	remaining := m.workspaceRows[:0:0]
	for _, row := range m.workspaceRows {
		if row.Worktree != target.Worktree {
			remaining = append(remaining, row)
		}
	}
	m.workspaceRows = remaining
	if n := len(m.workspaceRows); n == 0 {
		m.sessionCursor = 0
	} else if m.sessionCursor >= n {
		m.sessionCursor = n - 1
	}
}

// filterPendingDeletes drops rows whose worktree is awaiting deletion. A
// snapshot-driven rebuild can list a worktree that git has not finished
// removing yet; without this filter the row would flash back into the table
// between the confirmation and the deletion completing. Returns rows unchanged
// when nothing is pending.
func filterPendingDeletes(rows []workspace.Row, pending map[string]workspace.Row) []workspace.Row {
	if len(pending) == 0 {
		return rows
	}
	filtered := rows[:0:0]
	for _, row := range rows {
		if _, ok := pending[row.Worktree]; !ok {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// canDeleteWorktree reports whether row may be deleted, returning a user-facing
// reason when it may not. The repository's primary worktree (Worktree == Repo)
// and rows not associated with a configured repo are protected: git refuses the
// former, and the latter has no repo root to run `git worktree remove` from.
func canDeleteWorktree(row workspace.Row) (bool, string) {
	if row.Worktree == "" {
		return false, "no worktree selected"
	}
	if row.Repo == "" {
		return false, "cannot delete: worktree is not part of a configured repo"
	}
	if row.Worktree == row.Repo {
		return false, "cannot delete the repository's main worktree"
	}
	return true, ""
}

// mergeInfoText renders a branch merge state as a short human-readable phrase
// for the delete confirmation prompts.
func mergeInfoText(stateVal git.MergeState, base string) string {
	switch stateVal {
	case git.MergeMerged:
		if base == "" {
			return "merged"
		}
		return "merged into " + base
	case git.MergeNotMerged:
		if base == "" {
			return "NOT merged"
		}
		return "NOT merged into " + base
	default:
		return "merge status unknown"
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

			case promptNewWorktree, promptFetchBranch:
				// Branch-name prompt for 'n' (new worktree) and 'F' (fetch from
				// origin). On enter, advance to the harness chooser; the fetch-vs-
				// create distinction is carried by m.worktreeFromRemote. On esc,
				// cancel.
				switch msg.String() {
				case "enter":
					branch := strings.TrimSpace(m.input.Value())
					repoPath := m.newWorktreeRepo
					_, inputCmd := m.input.Update(msg)
					if branch == "" || repoPath == "" {
						// Nothing to do — cancelled effectively.
						m.prompt = promptIdle
						m.input.Blur()
						m.input.SetValue("")
						m.newWorktreeRepo = ""
						m.worktreeFromRemote = false
						return m, inputCmd
					}
					// Carry the branch forward and open the harness chooser.
					m.newWorktreeBranch = branch
					m.input.Blur()
					m.input.SetValue("")
					m.prompt = promptChooseHarness
					m.harnessChooserKinds = harnessChooserKinds(m.harnOp)
					m.harnessChooserCursor = defaultHarnessIndex(m.harnessChooserKinds)
					// Override default cursor from workspace config when set.
					if wsCfg, err := workspace.LoadConfig(); err == nil && wsCfg.DefaultHarness != "" {
						for i, k := range m.harnessChooserKinds {
							if string(k) == wsCfg.DefaultHarness {
								m.harnessChooserCursor = i
								break
							}
						}
					}
					return m, inputCmd

				case "esc":
					m.prompt = promptIdle
					m.input.Blur()
					m.input.SetValue("")
					m.newWorktreeRepo = ""
					m.newWorktreeBranch = ""
					m.worktreeFromRemote = false
					return m, nil

				default:
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					return m, cmd
				}

			case promptChooseHarness:
				// Harness chooser: up/down moves the cursor; enter confirms and
				// dispatches newWorktreeCmd; esc cancels the whole flow.
				switch msg.String() {
				case "enter":
					branch := m.newWorktreeBranch
					repoPath := m.newWorktreeRepo
					var chosenKind string
					if len(m.harnessChooserKinds) > 0 {
						idx := clampIndex(m.harnessChooserCursor, len(m.harnessChooserKinds))
						chosenKind = string(m.harnessChooserKinds[idx])
					}
					if chosenKind == "" {
						chosenKind = string(harness.KindOpenCode)
					}
					fromRemote := m.worktreeFromRemote
					m.prompt = promptIdle
					m.newWorktreeRepo = ""
					m.newWorktreeBranch = ""
					m.worktreeFromRemote = false
					m.harnessChooserKinds = nil
					m.harnessChooserCursor = 0
					launchMode := m.launchMode
					if wsCfg, err := workspace.LoadConfig(); err == nil {
						launchMode = launchModeFor(wsCfg.LaunchMode)
					}
					actionCmd := newWorktreeCmd(m.tmux, m.gitOp, m.harnOp, repoPath, branch, chosenKind, launchMode, fromRemote)
					return m, actionCmd

				case "esc":
					m.prompt = promptIdle
					m.newWorktreeRepo = ""
					m.newWorktreeBranch = ""
					m.worktreeFromRemote = false
					m.harnessChooserKinds = nil
					m.harnessChooserCursor = 0
					return m, nil

				case "up", "ctrl+p":
					m.harnessChooserCursor = clampIndex(m.harnessChooserCursor-1, len(m.harnessChooserKinds))
					return m, nil

				case "down", "ctrl+n":
					m.harnessChooserCursor = clampIndex(m.harnessChooserCursor+1, len(m.harnessChooserKinds))
					return m, nil
				}
				return m, nil

			case promptAddRepo:
				// Embedded repo finder. Enter adds the highlighted repo; the
				// arrow keys (and ctrl+n/p) move the selection; esc closes;
				// everything else edits the filter query and re-ranks matches.
				switch msg.String() {
				case "esc":
					m.closeRepoFinder()
					return m, nil
				case "enter":
					if len(m.repoFinderMatches) == 0 {
						return m, nil
					}
					sel := m.repoFinderMatches[clampIndex(m.repoFinderCursor, len(m.repoFinderMatches))]
					m.closeRepoFinder()
					return m, addSelectedRepoCmd(sel)
				case "up", "ctrl+p":
					m.repoFinderCursor = clampIndex(m.repoFinderCursor-1, len(m.repoFinderMatches))
					return m, nil
				case "down", "ctrl+n":
					m.repoFinderCursor = clampIndex(m.repoFinderCursor+1, len(m.repoFinderMatches))
					return m, nil
				default:
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					m.repoFinderMatches = fuzzyRank(m.input.Value(), m.repoFinderAll)
					m.repoFinderCursor = clampIndex(m.repoFinderCursor, len(m.repoFinderMatches))
					return m, cmd
				}

			case promptConfirmDeleteWorktree:
				// First confirmation. 'y' advances to the second prompt; any
				// other key (including esc) cancels the deletion.
				if msg.String() == "y" || msg.String() == "Y" {
					m.prompt = promptConfirmDeleteWorktree2
					return m, nil
				}
				m.prompt = promptIdle
				m.deleteTarget = workspace.Row{}
				m.deleteMergeInfo = ""
				return m, nil

			case promptConfirmDeleteWorktree2:
				// Second confirmation. Default is cancel: only an explicit 'y'
				// proceeds; every other key (including esc/enter) aborts. This
				// is the last gate before an irreversible removal.
				if msg.String() == "y" || msg.String() == "Y" {
					target := m.deleteTarget
					m.prompt = promptIdle
					m.deleteTarget = workspace.Row{}
					m.deleteMergeInfo = ""
					// Optimistically drop the row now: git worktree removal can
					// take a few seconds, and leaving the row visible looks like
					// the keypress was ignored. removeWorktreeRow stashes the row
					// in pendingDeletes so a failed deletion can restore it.
					m.removeWorktreeRow(target)
					return m, deleteWorktreeCmd(m.tmux, m.gitOp, target.Repo, target.Worktree, m.launchMode)
				}
				m.prompt = promptIdle
				m.deleteTarget = workspace.Row{}
				m.deleteMergeInfo = ""
				return m, nil

			case promptConfirmRemoveRepo:
				// Single confirmation. 'y' untracks the repo; any other key
				// (including esc) cancels. Non-destructive: the repo stays on
				// disk, so one gate is sufficient.
				if msg.String() == "y" || msg.String() == "Y" {
					target := m.removeRepoTarget
					m.prompt = promptIdle
					m.removeRepoTarget = ""
					return m, removeRepoCmd(target)
				}
				m.prompt = promptIdle
				m.removeRepoTarget = ""
				return m, nil
			}
		}

		// (b) Global quit — only when no prompt is active.
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}

		// (c) Tasks pane activation and focus swap.
		if msg.String() == "T" {
			if m.twAvail {
				m.tasksActive = !m.tasksActive
				if m.tasksActive {
					m.focus = focusTasks
					if !m.tasksLoaded {
						return m, loadTasksCmd(m.tw, m.cfg.TaskwarriorTimeout)
					}
				} else {
					m.focus = focusSessions
				}
			}
			return m, nil
		}

		if msg.String() == "tab" {
			if m.twAvail && m.tasksActive {
				if m.focus == focusSessions {
					m.focus = focusTasks
				} else {
					m.focus = focusSessions
				}
			}
			// No-op when !twAvail or tasks are inactive — focus stays on sessions.
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

				// Missing rows cannot be resumed (directory absent from disk).
				if row.State == workspace.StateMissing {
					m.tmuxHint = "worktree directory is missing — cannot resume"
					return m, nil
				}

				return m, launchCmd(m.tmux, row, m.harnOp, m.launchMode)

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
				m.worktreeFromRemote = false
				m.prompt = promptNewWorktree
				m.input.Placeholder = "branch name"
				m.input.SetValue("")
				focusCmd := m.input.Focus()
				return m, focusCmd

			case "F":
				// Fetch a branch from origin into a new worktree: collect the
				// branch name via prompt, then (after the harness chooser) fetch
				// and check it out. Mirrors 'n' but sets worktreeFromRemote so the
				// chooser dispatches the fetch path.
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
				m.worktreeFromRemote = true
				m.prompt = promptFetchBranch
				m.input.Placeholder = "branch name to fetch from origin"
				m.input.SetValue("")
				focusCmd := m.input.Focus()
				return m, focusCmd

			case "A":
				// Open the embedded repo finder: scan $HOME for git repos in
				// the background, then let the user fuzzy-filter and pick one.
				// Runs entirely inside the TUI (no ExecProcess), so it cannot
				// disturb the host tmux client.
				m.prompt = promptAddRepo
				m.repoFinderScanning = true
				m.repoFinderAll = nil
				m.repoFinderMatches = nil
				m.repoFinderCursor = 0
				m.repoFinderErr = ""
				m.input.Placeholder = "filter repos"
				m.input.SetValue("")
				return m, tea.Batch(m.input.Focus(), scanReposCmd(repoFinderRoot()))

			case "D":
				// Delete worktree: open the first of two confirmations and
				// kick off an async merge-status probe to annotate it. tmux is
				// not required (git removal works without it; window cleanup is
				// best-effort).
				if len(m.workspaceRows) == 0 {
					return m, nil
				}
				row := m.workspaceRows[m.sessionCursor]
				if ok, reason := canDeleteWorktree(row); !ok {
					m.tmuxHint = reason
					return m, nil
				}
				m.deleteTarget = row
				m.deleteMergeInfo = ""
				m.prompt = promptConfirmDeleteWorktree
				return m, mergeStatusCmd(m.gitOp, row.Repo, row.Branch, row.Worktree)

			case "R":
				// Untrack repo: drop the repo under the cursor from cogitator's
				// config. Non-destructive — the repo and its worktrees stay on
				// disk — so a single confirmation gates it.
				if len(m.workspaceRows) == 0 {
					return m, nil
				}
				row := m.workspaceRows[m.sessionCursor]
				if row.Repo == "" {
					m.tmuxHint = "no repo to remove for this row"
					return m, nil
				}
				m.removeRepoTarget = row.Repo
				m.prompt = promptConfirmRemoveRepo
				return m, nil
			}
			return m, nil
		}

		// (e) Tasks-focused keys — only when focused on tasks and no mutation in flight.
		if m.focus == focusTasks && m.tasksActive && !m.mutationInFlight {
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
		if msg.err != nil {
			// Surface the error as a transient hint.
			m.tmuxHint = fmt.Sprintf("launch error: %v", msg.err)
		} else if m.viewMarker != nil && msg.sessionID != "" {
			// The user successfully landed in the session — clear any
			// "work finished" badge regardless of whether they act on it.
			m.viewMarker.MarkViewed(harness.Kind(msg.provider), msg.instanceID, msg.sessionID)
		}
		return m, nil

	case worktreeCreatedMsg:
		// A new-worktree Cmd completed. On success, write a create-time roster
		// entry so the harness kind is persisted before any live-discovery
		// snapshot arrives (Codex sessions are never live-discovered, so without
		// this write the roster would never record the harness kind).
		if msg.err != nil {
			m.tmuxHint = fmt.Sprintf("new worktree error: %v", msg.err)
		} else if msg.canonDest != "" {
			// Write a create-time roster entry via the recorder's Upserts
			// channel so the recorder's in-memory map is updated atomically
			// with the next Save. Non-blocking: if the channel is full the
			// write is skipped (best-effort; the entry will appear on the next
			// live-discovery snapshot for harnesses that support it).
			if m.rosterUpserts != nil {
				kind := msg.harnessKind
				if kind == "" {
					kind = string(harness.KindOpenCode)
				}
				entry := workspace.RosterEntry{
					Dir:          msg.canonDest,
					Harness:      kind,
					LastActivity: time.Now(),
				}
				select {
				case m.rosterUpserts <- entry:
				default:
				}
			}
		}
		return m, nil

	case repoScanMsg:
		// Background repo scan finished. Ignore a stale result if the finder
		// was closed in the meantime.
		if m.prompt != promptAddRepo {
			return m, nil
		}
		m.repoFinderScanning = false
		if msg.err != nil {
			m.repoFinderErr = fmt.Sprintf("scan failed: %v", msg.err)
			m.repoFinderAll = nil
			m.repoFinderMatches = nil
			return m, nil
		}
		m.repoFinderErr = ""
		m.repoFinderAll = msg.repos
		m.repoFinderMatches = fuzzyRank(m.input.Value(), m.repoFinderAll)
		m.repoFinderCursor = clampIndex(m.repoFinderCursor, len(m.repoFinderMatches))
		return m, nil

	case repoAddMsg:
		// Outcome of registering a repo selected in the finder.
		switch {
		case msg.addErr != nil:
			m.tmuxHint = fmt.Sprintf("add repo failed: %v", msg.addErr)
			return m, nil
		case msg.added:
			m.tmuxHint = "added repo: " + filepath.Base(msg.repoPath)
			// Rebuild rows so the new repo appears immediately rather than
			// waiting for the next snapshot.
			m.workspaceRows, m.launchMode = buildWorkspaceRows(m.snap, m.cfg)
			if n := len(m.workspaceRows); n == 0 {
				m.sessionCursor = 0
			} else if m.sessionCursor >= n {
				m.sessionCursor = n - 1
			}
			return m, nil
		default:
			// Validation passed but the repo was already configured.
			m.tmuxHint = "repo already configured: " + filepath.Base(msg.repoPath)
			return m, nil
		}

	case repoRemoveMsg:
		// Outcome of untracking a repo via 'R'.
		switch {
		case msg.removeErr != nil:
			m.tmuxHint = fmt.Sprintf("remove repo failed: %v", msg.removeErr)
			return m, nil
		case msg.removed:
			m.tmuxHint = "removed repo: " + filepath.Base(msg.repoPath)
			// Rebuild rows so the repo disappears immediately rather than
			// waiting for the next snapshot.
			m.workspaceRows, m.launchMode = buildWorkspaceRows(m.snap, m.cfg)
			if n := len(m.workspaceRows); n == 0 {
				m.sessionCursor = 0
			} else if m.sessionCursor >= n {
				m.sessionCursor = n - 1
			}
			return m, nil
		default:
			// Path was not configured (e.g. a stale row).
			m.tmuxHint = "repo not tracked: " + filepath.Base(msg.repoPath)
			return m, nil
		}

	case mergeStatusMsg:
		// Annotate the active delete confirmation, but only if it still targets
		// the same worktree the probe was launched for (guards against a stale
		// result arriving after cancel or retarget).
		if (m.prompt == promptConfirmDeleteWorktree || m.prompt == promptConfirmDeleteWorktree2) &&
			msg.path == m.deleteTarget.Worktree {
			m.deleteMergeInfo = mergeInfoText(msg.state, msg.base)
		}
		return m, nil

	case worktreeDeletedMsg:
		if msg.err != nil {
			m.tmuxHint = fmt.Sprintf("delete failed: %v", msg.err)
			// The row was optimistically removed at confirm time; restore it so
			// a failed deletion does not silently drop the worktree from view.
			// The next snapshot rebuild reconciles ordering.
			if saved, ok := m.pendingDeletes[msg.path]; ok {
				m.workspaceRows = append(m.workspaceRows, saved)
				delete(m.pendingDeletes, msg.path)
			}
			return m, nil
		}
		// Success: clear the pending entry. The row was already dropped when the
		// deletion was confirmed; remove it again defensively (idempotent) in
		// case it was never optimistically removed (e.g. a direct dispatch).
		delete(m.pendingDeletes, msg.path)
		var remaining []workspace.Row
		for _, row := range m.workspaceRows {
			if row.Worktree != msg.path {
				remaining = append(remaining, row)
			}
		}
		m.workspaceRows = remaining
		if n := len(m.workspaceRows); n == 0 {
			m.sessionCursor = 0
		} else if m.sessionCursor >= n {
			m.sessionCursor = n - 1
		}
		return m, nil

	case snapshotMsg:
		m.snap = state.Snapshot(msg)
		// Re-arm the snapshot listener and handle bell transitions immediately;
		// the workspace-row build is offloaded to a background Cmd so git/tmux
		// shell-outs never block Update.
		next := waitSnapshot(m.snaps)
		var bellC tea.Cmd
		if m.bellEnabled {
			fired := processBellTransitions(m.snap.Sessions, m.bellSent)
			bellC = bellCmd(len(fired))
		}
		var buildC tea.Cmd
		if m.rowsBuilding {
			// A build is already in flight; mark dirty so the completion
			// handler dispatches one follow-up build with the latest snap.
			m.rowsDirty = true
		} else {
			m.rowsBuilding = true
			buildC = buildWorkspaceRowsCmd(m.snap, m.cfg)
		}
		return m, tea.Batch(next, bellC, buildC)

	case workspaceRowsMsg:
		m.workspaceRows = filterPendingDeletes(msg.rows, m.pendingDeletes)
		m.launchMode = msg.launchMode
		// Clamp cursor so it never points past the end of the new row list.
		if n := len(m.workspaceRows); n == 0 {
			m.sessionCursor = 0
		} else if m.sessionCursor >= n {
			m.sessionCursor = n - 1
		}
		m.rowsBuilding = false
		if m.rowsDirty {
			m.rowsDirty = false
			m.rowsBuilding = true
			return m, buildWorkspaceRowsCmd(m.snap, m.cfg)
		}
		return m, nil

	case tickMsg:
		// Re-arm the ticker and record the current time so View() can render
		// fresh relative timestamps without calling time.Now() on every frame.
		m.tickNow = time.Time(msg)
		return m, tickCmd()
	}
	return m, nil
}

// closeRepoFinder dismisses the embedded repo finder and resets its state,
// returning the shared text input to idle. It persists nothing; callers that
// selected a repo dispatch addSelectedRepoCmd before closing.
// harnessChooserKinds returns the sorted list of harness kinds to show in the
// chooser. It falls back to [KindOpenCode] when harnOp is nil or returns no
// kinds, so the chooser always has at least one option.
func harnessChooserKinds(harnOp harnessOps) []harness.Kind {
	if harnOp == nil {
		return []harness.Kind{harness.KindOpenCode}
	}
	kinds := harnOp.Kinds()
	if len(kinds) == 0 {
		return []harness.Kind{harness.KindOpenCode}
	}
	// Sort for a stable, predictable order in the UI.
	sorted := make([]harness.Kind, len(kinds))
	copy(sorted, kinds)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted
}

// defaultHarnessIndex returns the index of KindOpenCode in kinds, or 0 when
// not found. Used to pre-position the chooser cursor on the most common choice.
func defaultHarnessIndex(kinds []harness.Kind) int {
	for i, k := range kinds {
		if k == harness.KindOpenCode {
			return i
		}
	}
	return 0
}

func (m *model) closeRepoFinder() {
	m.prompt = promptIdle
	m.input.Blur()
	m.input.SetValue("")
	m.repoFinderScanning = false
	m.repoFinderAll = nil
	m.repoFinderMatches = nil
	m.repoFinderCursor = 0
	m.repoFinderErr = ""
}

// renderHarnessChooser renders the harness-selection list shown in the sessions
// pane while prompt == promptChooseHarness. The user moves the cursor with
// up/down and confirms with enter; esc cancels the whole new-worktree flow.
func (m model) renderHarnessChooser(width, height int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Choose harness") + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("new worktree: %s / %s", filepath.Base(m.newWorktreeRepo), m.newWorktreeBranch)) + "\n")

	if len(m.harnessChooserKinds) == 0 {
		b.WriteString(dimStyle.Render("(no harnesses registered)"))
		return b.String()
	}

	cursor := clampIndex(m.harnessChooserCursor, len(m.harnessChooserKinds))
	for i, k := range m.harnessChooserKinds {
		line := ansi.Truncate("  "+string(k), width-2, "…")
		if i == cursor {
			line = wtCursorStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(dimStyle.Render("↑↓ move · enter select · esc cancel"))
	return b.String()
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
	tasksActive := m.twAvail && m.tasksActive
	viewFocus := m.focus
	if !tasksActive {
		viewFocus = focusSessions
	}

	if viewFocus == focusSessions {
		tasksHint := ""
		switch {
		case tasksActive:
			tasksHint = "  ·  tab→tasks · T hide tasks"
		case m.twAvail:
			tasksHint = "  ·  T show tasks"
		}
		headerHint = fmt.Sprintf("  %d live · %d recent (≤%dm)  ·  updated %s  ·  a to %s recent · A add repo · R rm repo · D del wt · F fetch branch%s  ·  q quit",
			live, recent, recentMins, m.snap.UpdatedAt.Format("15:04:05"), toggleVerb(m.recentCollapsed), tasksHint)
	} else {
		headerHint = fmt.Sprintf("  %d pending  ·  a add · e edit · s start/stop · d done · D del · U undo · j/k move  ·  tab→sessions · T hide tasks  ·  q quit",
			len(m.tasks))
	}
	header := titleStyle.Render("cogitator") + dimStyle.Render(headerHint)

	legend := legendLine(m.width, tasksActive)
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

	tasksOuterH := 0
	if tasksActive {
		tasksOuterH = max(8, m.height/3)
	}
	sessionsOuterH := max(6, m.height-tasksOuterH-reserved)

	// Choose border style based on which pane is focused.
	sessionsStyle := paneStyle
	tasksStyle := paneStyle
	if viewFocus == focusSessions {
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
	tasksInnerH := 0
	if tasksActive {
		tasksInnerH = max(1, tasksOuterH-2)
	}

	// When repos are configured, render the merged worktree view. Otherwise
	// fall back to the live-only path so --status/--demo and unconfigured
	// installs render exactly as before.
	var sessionContent string
	switch {
	case m.prompt == promptAddRepo:
		sessionContent = m.renderRepoFinder(paneW, sessionsInnerH)
	case m.prompt == promptChooseHarness:
		sessionContent = m.renderHarnessChooser(paneW, sessionsInnerH)
	case len(m.workspaceRows) > 0:
		now := m.tickNow
		if now.IsZero() {
			now = time.Now()
		}
		sessionContent = m.renderWorkspaceRows(paneW, m.workspaceRows, m.sessionCursor, now)
	default:
		sessionContent = m.renderAllSessions(paneW, rows, recentByInstance)
	}
	sessionsPane := sessionsStyle.Width(paneW).Height(sessionsInnerH).Render(sessionContent)

	parts := []string{header, sessionsPane}
	if tasksActive {
		tasksContent := m.renderTasksPane(tasksOuterH, paneW)
		tasksPane := tasksStyle.Width(paneW).Height(tasksInnerH).Render(tasksContent)
		parts = append(parts, tasksPane)
	}
	parts = append(parts, legend)
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
		pendingDeletes:  map[string]workspace.Row{},
		cfg:             cfg,

		// Inject real implementations for tmux, git, and harness operations.
		// Tests can override these fields with fakes after construction.
		tmux:   realTmuxOps{},
		gitOp:  realGitOps{},
		harnOp: realHarnessOps{},

		tw:               tw,
		twAvail:          twAvail,
		tasksActive:      twAvail,
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

// buildWorkspaceRowsCmd returns a tea.Cmd that runs buildWorkspaceRows in the
// background and delivers the result as a workspaceRowsMsg. snap and cfg are
// captured by value at dispatch time so the closure is not affected by later
// mutations to the model.
func buildWorkspaceRowsCmd(snap state.Snapshot, cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		rows, mode := buildWorkspaceRows(snap, cfg)
		return workspaceRowsMsg{rows: rows, launchMode: mode}
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
// It also returns the resolved tmux launch mode from workspace config so the
// caller can keep its launch behaviour in sync with config edits.
//
// Returns nil rows when no repos are configured (zero-value safe for callers);
// the launch mode is still resolved (defaulting to ModeWindow).
func buildWorkspaceRows(snap state.Snapshot, cfg *config.Config) ([]workspace.Row, tmuxctl.LaunchMode) {
	wsCfg, err := workspace.LoadConfig()
	if err != nil {
		return nil, tmuxctl.ModeWindow
	}
	mode := launchModeFor(wsCfg.LaunchMode)
	if len(wsCfg.Repos) == 0 {
		// No repos configured — live-only path.
		return nil, mode
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

	return workspace.Merge(wsCfg.Repos, worktreesByRepo, roster, liveTopLevel, tmuxDirs), mode
}
