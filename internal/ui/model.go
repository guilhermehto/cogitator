package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

type snapshotMsg state.Snapshot

// workspaceRowsMsg is returned by buildWorkspaceRowsCmd when the background
// workspace-row build completes. It carries the merged row list, the
// resolved tmux launch mode, and the resolved workspace root so the Update
// handler can apply them atomically.
type workspaceRowsMsg struct {
	rows       []settings.Row
	launchMode tmuxctl.LaunchMode
	// root is the resolved workspace root (settings.ResolveWorkspaceRoot),
	// carried alongside rows/launchMode because buildWorkspaceRows resolves it
	// even on its zero-repos early return — the exact case where View's
	// fallback branch needs it to exclude workspace-owned sessions.
	root string
}

// viewMode selects which top-level view occupies the full pane: Sessions
// (worktrees merged across configured repos) or Workspaces (multi-repo
// bundles and their sessions). Iota order is load-bearing: the zero value
// maps to viewSessions, keeping existing model{} literals in tests valid
// without explicit initialisation — the same convention promptMode documents
// below.
type viewMode int

const (
	viewSessions viewMode = iota
	viewWorkspaces
)

// wsStatusMsg carries the result of loadWorkspaceStatusCmd: the workspace set
// merged with live/roster status, ready for the Workspaces view to render.
type wsStatusMsg struct {
	statuses []workspace.WorkspaceStatus
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

// promptMode tracks whether the sessions-pane input bar is active and in
// which mode. Iota order is load-bearing: zero value maps to promptIdle,
// keeping existing model{} literals in tests valid without explicit
// initialisation.
type promptMode int

const (
	promptIdle promptMode = iota
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
	// promptSwitchSession is active while the ctrl+P session switcher is open.
	// It presents the worktree rows as a fuzzy-filtered list (matched on repo
	// name + branch); enter jumps/attaches to the selection exactly like the
	// sessions-pane Enter handler, esc cancels. cmd+P is intentionally not bound
	// because macOS terminals do not forward it to TUI apps.
	promptSwitchSession
	// promptHelp is active while the floating help overlay ('?') is open. It
	// is a passive modal: it lists every keybinding and is dismissed by any
	// key. No input is collected. Placed last so existing model{} literals in
	// tests keep their iota values.
	promptHelp
	// promptSettings is active while the floating settings modal ('S') is
	// open. It lets the user choose a persistent default harness (or "always
	// ask") and the launch mode; changes are written to config.json
	// immediately. Placed last so existing model{} literals keep their iota
	// values.
	promptSettings
	// promptNewWorkspace is active while the user types a name for 'N' (new,
	// empty workspace) from the Workspaces view. On enter, the name is
	// dispatched to createWorkspaceCmd; on esc, cancelled without creating
	// anything. Placed last so existing model{} literals keep their iota
	// values.
	promptNewWorkspace
	// promptNewWorkspaceSession is active while the user types a session name
	// for 'n' (new session) from the Workspaces view, for the workspace under
	// the cursor (captured in wsCreateTarget). On enter, a valid name advances
	// to promptChooseHarness (shared with the Sessions pane's 'n'/'F' flow;
	// wsCreateTarget non-empty is what tells that shared handler to dispatch
	// assembleWorkspaceSessionCmd instead of newWorktreeCmd); an invalid or
	// duplicate name is refused with wsHint and the prompt stays open. Placed
	// last so existing model{} literals keep their iota values.
	promptNewWorkspaceSession
	// promptConfirmDeleteWsSession is the FIRST of two confirmations for
	// deleting a single workspace session ('D' with the cursor on a session
	// row in the Workspaces view). Mirrors promptConfirmDeleteWorktree's
	// y/any-key gate, but the bundle being deleted has one member repo per
	// SessionMember rather than one branch, so the confirm renders one
	// merge-status line per member (wsDeleteMembers) instead of a single
	// branch/merge-status pair. Placed last so existing model{} literals keep
	// their iota values.
	promptConfirmDeleteWsSession
	// promptConfirmDeleteWsSession2 is the SECOND confirmation for a single
	// workspace session. Default is cancel: only an explicit 'y' proceeds;
	// every other key (including esc/enter) aborts, mirroring
	// promptConfirmDeleteWorktree2's last-gate contract.
	promptConfirmDeleteWsSession2
	// promptConfirmDeleteWorkspace is the FIRST of two confirmations for
	// deleting an entire workspace ('D' with the cursor on a workspace's
	// header or empty-sessions hint row). Every session in the workspace is
	// listed (via wsDeleteMembers, spanning all of them) and torn down the
	// same way a single session is; the workspace itself is removed from the
	// store only once every session has succeeded.
	promptConfirmDeleteWorkspace
	// promptConfirmDeleteWorkspace2 is the SECOND confirmation for a whole
	// workspace. Default is cancel; only 'y' proceeds.
	promptConfirmDeleteWorkspace2
	// promptWorkspaceModal is active while the repo-membership modal ('e' in
	// the Workspaces view) is open. It scans $HOME for git repositories, like
	// promptAddRepo, but combines the discovered non-member candidates with
	// the workspace's current members into one fuzzy-filterable list: enter
	// on a candidate attaches it, enter on a member detaches it; esc cancels
	// with no change. Placed last so existing model{} literals keep their
	// iota values.
	promptWorkspaceModal
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
	// harnessKind is the effective harness actually (re)launched when a
	// configured default overrode the row's recorded harness; empty otherwise.
	// The launch handler upserts the roster to match only when it is set.
	harnessKind string
}

// worktreeCreatedMsg is returned by newWorktreeCmd after git.AddWorktree
// succeeds and the harness window has been opened. canonDest is the
// post-create canonical path (the overlay key). harnessKind is the harness
// that was launched so the handler can write a create-time roster entry.
// repo and branch identify the pending-create placeholder row (keyed by
// repo+branch) so the handler can clear the optimistic spinner row regardless
// of how dest canonicalises.
type worktreeCreatedMsg struct {
	canonDest   string
	harnessKind string
	repo        string
	branch      string
	err         error
}

// spinnerTickMsg drives the animated spinner shown on pending-create rows. It
// fires on spinnerInterval while a worktree creation is in flight and stops
// re-arming once no creates remain (so it costs nothing when idle).
type spinnerTickMsg time.Time

// spinnerInterval is the spinner animation cadence. Fast enough to read as
// motion, slow enough to be cheap.
const spinnerInterval = 120 * time.Millisecond

// spinnerTickCmd returns a Cmd that fires a spinnerTickMsg after spinnerInterval.
// It is re-armed in Update only while pending creates remain.
func spinnerTickCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
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
// git refused (e.g. a locked worktree, or a dirty worktree when force-delete is
// disabled in config) so the row is preserved and the error surfaced.
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
// by the action Cmds. LaunchWindow maps to ModeWindow; everything else
// (including the empty default) maps to ModeSession.
func launchModeFor(m settings.LaunchMode) tmuxctl.LaunchMode {
	if m == settings.LaunchWindow {
		return tmuxctl.ModeWindow
	}
	return tmuxctl.ModeSession
}

// gitOps is the injectable seam for git worktree operations.
type gitOps interface {
	AddWorktree(repoPath, branch, dest string) (string, error)
	FetchAndAddWorktree(repoPath, branch, dest string) (string, error)
	RemoveWorktree(repoPath, worktreePath, branch string, force bool) error
	BranchMergeStatus(repoPath, branch string) (git.MergeState, string)
	Pull(worktreePath, branch string) (string, error)
}

// realGitOps delegates to the package-level git functions.
type realGitOps struct{}

func (realGitOps) AddWorktree(repoPath, branch, dest string) (string, error) {
	return git.AddWorktree(repoPath, branch, dest)
}

func (realGitOps) FetchAndAddWorktree(repoPath, branch, dest string) (string, error) {
	return git.FetchAndAddWorktree(repoPath, branch, dest)
}

func (realGitOps) RemoveWorktree(repoPath, worktreePath, branch string, force bool) error {
	return git.RemoveWorktree(repoPath, worktreePath, branch, force)
}

func (realGitOps) BranchMergeStatus(repoPath, branch string) (git.MergeState, string) {
	return git.BranchMergeStatus(repoPath, branch)
}

func (realGitOps) Pull(worktreePath, branch string) (string, error) {
	return git.Pull(worktreePath, branch)
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

// storeOps is the injectable seam for workspace persistence, mirroring
// tmuxOps/gitOps/harnessOps over their real implementations. *workspace.Store
// satisfies it via realStoreOps. Kept narrow (mirrors the Store's own public
// surface) so tests can inject a fake instead of driving real XDG paths in a
// temp directory.
type storeOps interface {
	LoadWorkspaces() ([]workspace.Workspace, error)
	SaveWorkspaces(workspaces []workspace.Workspace) error
	AddWorkspace(name string) (workspace.Workspace, error)
	RemoveWorkspace(name string) error
	AddSession(workspaceName string, session workspace.Session) error
	RemoveSession(workspaceName, sessionName string) error
	AttachRepo(workspaceName, repoPath string) error
	DetachRepo(workspaceName, repoPath string) error
}

// realStoreOps delegates to a concrete *workspace.Store.
type realStoreOps struct{ store *workspace.Store }

func (r realStoreOps) LoadWorkspaces() ([]workspace.Workspace, error) {
	return r.store.LoadWorkspaces()
}

func (r realStoreOps) SaveWorkspaces(workspaces []workspace.Workspace) error {
	return r.store.SaveWorkspaces(workspaces)
}

func (r realStoreOps) AddWorkspace(name string) (workspace.Workspace, error) {
	return r.store.AddWorkspace(name)
}

func (r realStoreOps) RemoveWorkspace(name string) error {
	return r.store.RemoveWorkspace(name)
}

func (r realStoreOps) AddSession(workspaceName string, session workspace.Session) error {
	return r.store.AddSession(workspaceName, session)
}

func (r realStoreOps) RemoveSession(workspaceName, sessionName string) error {
	return r.store.RemoveSession(workspaceName, sessionName)
}

func (r realStoreOps) AttachRepo(workspaceName, repoPath string) error {
	return r.store.AttachRepo(workspaceName, repoPath)
}

func (r realStoreOps) DetachRepo(workspaceName, repoPath string) error {
	return r.store.DetachRepo(workspaceName, repoPath)
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
	// workspaceRows is the merged list of worktree rows built by settings.Merge
	// on each snapshot and on each tickMsg. It is nil when no repos are
	// configured (zero value is safe — View() guards on len > 0).
	workspaceRows []settings.Row
	// workspaceRoot is the resolved workspace root (settings.ResolveWorkspaceRoot),
	// refreshed alongside workspaceRows. View uses it to exclude
	// workspace-owned session directories from the live-only fallback path
	// and from the header's live/recent counts, so a workspace session's
	// per-repo checkouts are never double-counted alongside their own
	// workspaceRows entry. Empty disables the exclusion (matches
	// pre-workspace behaviour) until the first successful resolve.
	workspaceRoot string
	// sessionCursor is the index into the visible worktree rows list that
	// currently holds keyboard focus. Zero value (0) is safe.
	sessionCursor int
	// sessionScroll is the first rendered repo-header/worktree line in the
	// sessions viewport. Unlike sessionCursor, it indexes the grouped display
	// lines (which include repo headers). Cursor movement keeps it in sync so
	// long workspace lists scroll while the selected worktree stays visible.
	sessionScroll int
	// pendingG is true after the first `g` of a `gg` (jump-to-top) sequence,
	// awaiting the second `g`. Reset on any other key in the sessions pane.
	pendingG bool
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
	// settingsCursor is the highlighted row in the settings modal
	// (promptSettings): 0 = default harness, 1 = launch mode.
	settingsCursor int
	// settingsDefaultHarness and settingsLaunchMode are the settings modal's
	// working copy of the persisted config, snapshotted on open and written
	// back on each change. An empty settingsDefaultHarness means "always ask".
	settingsDefaultHarness string
	settingsLaunchMode     settings.LaunchMode
	// settingsErr holds the last config-save error shown in the settings modal
	// (empty when the last write succeeded).
	settingsErr string
	// rosterUpserts is the channel used to inject create-time roster entries
	// into the recorder without calling settings.Save directly. Nil when the
	// recorder is not wired (e.g. in tests that don't need roster writes).
	rosterUpserts chan<- settings.RosterEntry
	// viewMarker reports a session as viewed by the user (jump/resume) so the
	// store can clear its AttnFinished badge. nil in tests that don't exercise
	// the launch path; the handler guards on nil.
	viewMarker viewMarker
	// deleteTarget is the worktree row captured when the user presses 'D' to
	// begin the two-step delete confirmation. Zero value when no delete is in
	// progress. Cleared on cancel and on dispatch of the delete Cmd.
	deleteTarget settings.Row
	// deleteMergeInfo is the human-readable branch merge status shown in the
	// delete confirmation prompts (e.g. "merged into main"). Empty until the
	// async probe (mergeStatusCmd) returns; rendered as "checking…" meanwhile.
	deleteMergeInfo string
	// deleteForce records whether the in-progress delete will pass
	// `git worktree remove --force` (resolved from config when 'D' opens the
	// flow). It drives both the confirm prompt's data-loss warning and the
	// eventual removal so the two never disagree.
	deleteForce bool
	// removeRepoTarget is the canonical repo path captured when the user
	// presses 'R' to untrack the repo under the cursor. Empty when no removal
	// is in progress; cleared on cancel and on dispatch of removeRepoCmd.
	removeRepoTarget string
	// launchMode is the resolved tmux launch mode (window vs session) read from
	// workspace config. Refreshed on each buildWorkspaceRows so config edits
	// take effect without a restart. The zero value never drives a real launch —
	// every launch path resolves the mode from config first (default: session).
	launchMode tmuxctl.LaunchMode
	// rowsBuilding is true while a background buildWorkspaceRowsCmd is in
	// flight. Only one build runs at a time; a second snapshotMsg while a
	// build is in flight sets rowsDirty instead of starting a second build.
	rowsBuilding bool
	// rowsDirty is set when a snapshotMsg arrives while rowsBuilding is true.
	// When the in-flight build completes, one follow-up build is dispatched
	// using the latest m.snap at that moment (coalesced, not stale).
	rowsDirty bool
	// demo is true under RunDemo. It curates workspaceRows directly and
	// suppresses the background git/tmux row build so the capture stays
	// deterministic and never shells out.
	demo bool
	// pendingDeletes tracks worktrees whose row was optimistically removed
	// from the table the moment deletion was confirmed, keyed by canonical
	// worktree path. The stored Row lets a failed deletion restore the row.
	// While a path is pending, workspaceRowsMsg filters it out so an in-flight
	// snapshot rebuild (the worktree still exists on disk until git finishes)
	// cannot resurrect the row. Entries clear on deletion success or failure.
	pendingDeletes map[string]settings.Row
	// pendingCreates tracks worktrees being created ('n') or fetched ('F') that
	// do not exist on disk yet, keyed by createKey(repo, branch). Each is shown
	// as an optimistic spinner row injected into the table until newWorktreeCmd
	// reports completion. Injected on every row (re)build so a snapshot rebuild
	// mid-fetch cannot drop the placeholder. Entries clear on success or failure.
	pendingCreates map[string]pendingCreate
	// spinnerFrame indexes spinnerFrames for the pending-create animation. Reset
	// to 0 when the spinner stops so it always restarts from the first frame.
	spinnerFrame int
	// spinnerActive is true while a spinnerTickCmd is in flight, so dispatching a
	// second concurrent create does not start a duplicate ticker.
	spinnerActive bool
	// pulling is the set of canonical worktree paths with an in-flight pull
	// ('P'). Each is rendered with the animated spinner and a "(pulling…)" suffix
	// until pullCmd reports completion. Keyed by worktree path so a repeated 'P'
	// on the same row is ignored and the pullFinishedMsg can clear the matching
	// indicator. Reads on a nil map are safe (closed).
	pulling map[string]bool

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

	// Session switcher (ctrl+P) state, meaningful only while
	// prompt == promptSwitchSession. sessionPaletteRows is a snapshot of the
	// candidate rows (worktree rows and workspace sessions alike, via
	// sessionCandidate) captured when the palette opens, used only for
	// rendering (renderPaletteRow, render.go); sessionPaletteTargets is the
	// parallel launch identity dispatched on enter; sessionPaletteLabels is
	// the parallel match text ("repo branch" for a worktree row,
	// "<workspace>/<session>" for a workspace session); sessionPaletteMatches
	// indexes those slices, holding the current fuzzy-filtered view ordered
	// best-first; sessionPaletteCursor indexes sessionPaletteMatches. Zero
	// values are safe (palette closed).
	sessionPaletteRows    []settings.Row
	sessionPaletteTargets []launchTarget
	sessionPaletteLabels  []string
	sessionPaletteMatches []int
	sessionPaletteCursor  int

	// switchOrder records the monotonically increasing sequence in which
	// targets (worktrees or workspace sessions) were last jumped to or
	// resumed, keyed by the target's launch directory (launchTarget.dir);
	// switchSeq is the next sequence to hand out. It seeds the ctrl+P
	// palette's most-recently-used ordering so pressing ctrl+P then enter
	// returns to the previous session. In-memory only — it resets on restart,
	// falling back to the alphabetical row order.
	switchOrder map[string]int
	switchSeq   int

	// Workspaces view (Tab) state. view selects which top-level view occupies
	// the pane; wsStatuses is the merged workspace/session list built by
	// loadWorkspaceStatusCmd on Init and refreshed on each snapshot.
	// wsCursor/wsScroll are this view's own cursor and scroll position, kept
	// separate from sessionCursor/sessionScroll so Tab never disturbs the
	// other view's position. wsPendingG mirrors pendingG for this view's own
	// `gg` jump-to-top, kept separate so a `g` pressed in one view can never
	// arm the other's.
	view       viewMode
	wsStatuses []workspace.WorkspaceStatus
	wsCursor   int
	wsScroll   int
	wsPendingG bool
	// wsBuilding/wsDirty coalesce loadWorkspaceStatusCmd exactly as
	// rowsBuilding/rowsDirty do for buildWorkspaceRowsCmd: only one load runs
	// at a time, and a burst of snapshots arriving while one is in flight
	// collapses into a single follow-up load using the latest snapshot.
	wsBuilding bool
	wsDirty    bool
	// wsHint is a transient one-line message shown in the Workspaces view
	// (e.g. "add a repo first", a failed create's error), mirroring tmuxHint's
	// role for the Sessions pane. Kept separate from tmuxHint so a message
	// raised in one view can never bleed into the other's rendering after Tab.
	wsHint string
	// wsCreateTarget is the workspace name captured when 'n' opens
	// promptNewWorkspaceSession, carried forward through promptChooseHarness
	// (shared with the Sessions pane's 'n'/'F' flow) so its enter-handler
	// knows to dispatch assembleWorkspaceSessionCmd rather than
	// newWorktreeCmd. Empty whenever no workspace-session create is in
	// progress.
	wsCreateTarget string
	// wsCreateSessionName is the session name typed in promptNewWorkspaceSession,
	// carried forward to promptChooseHarness exactly as newWorktreeBranch carries
	// the Sessions pane's branch name.
	wsCreateSessionName string
	// wsPendingSessions tracks workspace sessions being assembled ('n' in the
	// Workspaces view), keyed by wsSessionKey(workspace, session). Each is
	// rendered as an optimistic, animated placeholder row (injectPendingWsSessions)
	// until assembleWorkspaceSessionCmd reports completion, mirroring
	// pendingCreates for the Sessions pane.
	wsPendingSessions map[string]pendingWsSession

	// Workspace/session delete confirmation ('D' in the Workspaces view)
	// state, meaningful only while prompt is one of the four
	// promptConfirmDelete{WsSession,Workspace}[2] modes. wsDeleteWorkspace is
	// the target workspace's name; wsDeleteSession is the target session's
	// name for a single-session delete, empty when the whole workspace (every
	// session) is the target. wsDeleteMembers is the flat, session-tagged list
	// of member rows the confirm renders and probes — captured once when 'D'
	// opens the flow so the confirm dialog and the eventual teardown act on
	// the same snapshot regardless of store changes in between.
	// wsDeleteMergeInfo holds each member's human-readable merge status,
	// keyed by its worktree path, filled in as the concurrent per-member
	// probes (wsMergeStatusCmd) return; a member with no entry yet renders as
	// "checking…", mirroring deleteMergeInfo's role for the single-worktree
	// flow. Zero values are safe (no delete in progress).
	wsDeleteWorkspace string
	wsDeleteSession   string
	wsDeleteMembers   []wsDeleteMember
	wsDeleteMergeInfo map[string]string

	// Repo-membership modal ('e' in the Workspaces view) state, meaningful
	// only while prompt == promptWorkspaceModal. wsModalWorkspace is the
	// target workspace's name, captured when the modal opens.
	// wsModalScanning is true between opening and the scan result arriving.
	// wsModalEntries is the combined, alphabetically sorted set of the
	// workspace's current members (offered for removal) and freshly
	// discovered non-member candidates (offered for addition) — see
	// wsModalEntry (workspace_modal.go). wsModalMatches is its current
	// fuzzy-filtered view (what is rendered), indices into wsModalEntries;
	// wsModalCursor indexes wsModalMatches. wsModalErr holds a scan error to
	// surface in the modal body (a failed commit is reported via wsHint
	// instead, since the modal has already closed by then). Zero values are
	// safe (modal closed).
	wsModalWorkspace string
	wsModalScanning  bool
	wsModalEntries   []wsModalEntry
	wsModalMatches   []int
	wsModalCursor    int
	wsModalErr       string

	// Injectable seams for tmux, git, harness, and workspace-store operations.
	// Nil values are replaced with the real implementations in newModel (store
	// is wired separately by RunTUI after newModel returns, like viewMarker
	// and rosterUpserts). Tests inject fakes. Zero-value model{} literals in
	// tests are safe: action Cmds guard on nil and return an error result
	// rather than panicking.
	tmux   tmuxOps
	gitOp  gitOps
	harnOp harnessOps
	store  storeOps

	// prompt/input drive the sessions-pane input bar shared by every prompt
	// mode (new worktree, fetch branch, repo finder, session switcher, ...).
	prompt promptMode
	input  textinput.Model
}

// launchTarget is the neutral tmux-launch identity consumed by launchCmd,
// launchArgv, and launchInner. It is the seam that lets both the Sessions
// pane (rowLaunchTarget, from a settings.Row) and the Workspaces view
// (wsSessionLaunchTarget, from a workspace session) share one launch path
// without either function being keyed on settings.Row.
type launchTarget struct {
	// dir is the canonical directory tmux selects/creates a window or session
	// for, and the harness's working directory. For a workspace session this
	// is the session directory, not any single member repo's worktree — the
	// harness launched there can read every member through its subdirectories.
	dir string
	// name is the tmux window/session name used only when EnsureWindowMode
	// must create a brand-new target (no existing window is found for dir).
	name string
	// harness is the recorded harness kind ("" falls back to opencode).
	harness string
	// harnessAuthoritative is true when harness must NOT be overridden by a
	// configured default harness. Set for workspace sessions, whose harness
	// was chosen explicitly at create time and recorded on the Session;
	// false for Sessions-pane rows, which keep the pre-existing
	// default-overrides-row behaviour.
	harnessAuthoritative bool
	// resumeToken is passed to the harness's LaunchArgv to resume a specific
	// session; empty lets the harness resume its own most-recent session for
	// dir (or launch fresh).
	resumeToken string
	// provider and sessionID identify the session that was selected so the
	// Update handler can mark it viewed (clearing any AttnFinished badge).
	// Empty when the target has no associated session.
	provider  string
	sessionID string
}

// fallbackProvider returns provider unless it is empty, in which case it
// falls back to harnessKind — the same "stale roster metadata" tolerance
// settings.Row.Provider documents, reused here so workspace sessions get the
// identical fallback for the live-store attention clear.
func fallbackProvider(provider, harnessKind string) string {
	if provider == "" {
		return harnessKind
	}
	return provider
}

// rowLaunchTarget builds the launchTarget for a Sessions-pane worktree row,
// preserving the window-naming and provider-fallback behaviour launchCmd and
// launchInner had inline before the seam was extracted.
func rowLaunchTarget(row settings.Row) launchTarget {
	name := filepath.Base(row.Worktree)
	if row.Branch != "" {
		name = filepath.Base(row.Repo) + "/" + row.Branch
	}
	return launchTarget{
		dir:         row.Worktree,
		name:        name,
		harness:     row.Harness,
		resumeToken: row.SessionID,
		provider:    fallbackProvider(row.Provider, row.Harness),
		sessionID:   row.SessionID,
	}
}

// wsSessionLaunchTarget builds the launchTarget for a workspace session:
// name identifies both the workspace and the session so the tmux
// window/session title distinguishes it from every other target, and
// harnessAuthoritative is set so the session's own recorded harness is never
// second-guessed by a configured default.
func wsSessionLaunchTarget(workspaceName string, sess workspace.SessionStatus) launchTarget {
	return launchTarget{
		dir:                  sess.Session.Dir,
		name:                 workspaceName + "/" + sess.Session.Name,
		harness:              sess.Session.Harness,
		harnessAuthoritative: true,
		resumeToken:          sess.SessionID,
		provider:             fallbackProvider(sess.Provider, sess.Session.Harness),
		sessionID:            sess.SessionID,
	}
}

// launchCmd performs the jump/resume tmux operations for the given target and
// returns a launchResultMsg. It selects the correct tmux action based on
// window existence and pane liveness:
//
//   - window alive: Select
//   - window dead: RelaunchInWindow → Select
//   - no window: EnsureWindow → Select
//
// The function is a tea.Cmd (runs off the UI goroutine).
func launchCmd(ops tmuxOps, target launchTarget, harnOp harnessOps, mode tmuxctl.LaunchMode, defaultKind string) tea.Cmd {
	inner := launchInner(ops, target, harnOp, mode, defaultKind)
	return func() tea.Msg {
		res := inner()
		// Stamp the session identity so the Update handler can mark it viewed
		// (clearing AttnFinished) when the select succeeds.
		res.provider = target.provider
		res.sessionID = target.sessionID
		return res
	}
}

// launchArgv resolves the harness launch argv for target. A configured,
// already-validated default kind overrides the target's recorded harness
// unless harnessAuthoritative is set; overrideKind is the new kind when an
// override happened and empty otherwise, so callers upsert the roster only on
// a real switch. Falls back to opencode when the harness cannot be resolved.
func launchArgv(target launchTarget, harnOp harnessOps, defaultKind string) (argv []string, overrideKind harness.Kind) {
	kind := harness.Kind(target.harness)
	if kind == "" {
		kind = harness.KindOpenCode
	}
	if !target.harnessAuthoritative && defaultKind != "" && harness.Kind(defaultKind) != kind {
		kind = harness.Kind(defaultKind)
		overrideKind = kind
	}
	if harnOp != nil {
		if h, err := harnOp.Get(kind); err == nil {
			// On an override the target's resumeToken belongs to the previous
			// harness and is invalid for the new one; start a fresh session.
			token := target.resumeToken
			if overrideKind != "" {
				token = ""
			}
			argv = h.LaunchArgv(target.dir, token)
		}
	}
	if len(argv) == 0 {
		argv = []string{"opencode", "--mdns", target.dir}
	}
	return argv, overrideKind
}

func launchInner(ops tmuxOps, target launchTarget, harnOp harnessOps, mode tmuxctl.LaunchMode, defaultKind string) func() launchResultMsg {
	return func() launchResultMsg {
		if ops == nil || !ops.Available() {
			return launchResultMsg{dir: target.dir, err: tmuxctl.ErrNotAvailable}
		}

		dir := target.dir

		// Resolve the harness argv; a configured default overrides the
		// target's recorded harness on a cold (re)launch (overrideKind set
		// only then, and never when harnessAuthoritative is set).
		argv, overrideKind := launchArgv(target, harnOp, defaultKind)

		// selectTarget moves the client to tmuxTarget. In session mode it
		// switches to the session and lets tmux restore its last-active
		// window (so you land where you left off, not always the first
		// window). In window mode it focuses the exact tagged window.
		selectTarget := func(tmuxTarget tmuxctl.Target) error {
			if mode == tmuxctl.ModeSession {
				return ops.SelectSession(tmuxTarget)
			}
			return ops.Select(tmuxTarget)
		}

		// Check tmux directly instead of trusting the caller's cached state.
		// A running target can be stale if its process or tmux window died
		// before the next discovery update, so use the same recovery path for
		// every resumable target.
		tmuxTarget, findErr := ops.FindWindowByDir(dir)
		if findErr == nil {
			// Window exists — check if the process is alive.
			alive, aliveErr := ops.WindowProcessAlive(tmuxTarget)
			if aliveErr != nil {
				// Cannot determine liveness — try to select anyway.
				return launchResultMsg{dir: dir, err: selectTarget(tmuxTarget)}
			}
			if alive {
				// Process is alive — just select.
				return launchResultMsg{dir: dir, err: selectTarget(tmuxTarget)}
			}
			// Process is dead — relaunch then select.
			if err := ops.RelaunchInWindow(tmuxTarget, argv); err != nil {
				return launchResultMsg{dir: dir, err: err}
			}
			return launchResultMsg{dir: dir, launched: true, harnessKind: string(overrideKind), err: selectTarget(tmuxTarget)}
		}

		// No window exists — create one and select it.
		newTarget, err := ops.EnsureWindowMode(dir, target.name, argv, mode)
		if err != nil {
			return launchResultMsg{dir: dir, err: err}
		}
		return launchResultMsg{dir: dir, launched: true, harnessKind: string(overrideKind), err: selectTarget(newTarget)}
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
	inner := newWorktreeInner(ops, gitOp, harnOp, repoPath, branch, harnessKind, mode, fromRemote)
	return func() tea.Msg {
		// Stamp repo+branch on every result (including error paths) so the
		// Update handler can clear the matching pending-create spinner row.
		res := inner()
		res.repo = repoPath
		res.branch = branch
		return res
	}
}

func newWorktreeInner(ops tmuxOps, gitOp gitOps, harnOp harnessOps, repoPath, branch, harnessKind string, mode tmuxctl.LaunchMode, fromRemote bool) func() worktreeCreatedMsg {
	return func() worktreeCreatedMsg {
		if ops == nil || !ops.Available() {
			return worktreeCreatedMsg{err: tmuxctl.ErrNotAvailable}
		}

		dest := worktreeDest(repoPath, branch)

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

// deleteWorktreeCmd removes the worktree at path (belonging to repo) and its
// branch via git, then best-effort closes its attached tmux window/session so
// no dead pane is left pointing at a missing directory. The git removal is the
// only step that can fail the operation; branch deletion (inside RemoveWorktree)
// and tmux cleanup are advisory and their errors are ignored.
func deleteWorktreeCmd(ops tmuxOps, gitOp gitOps, repo, path, branch string, mode tmuxctl.LaunchMode, force bool) tea.Cmd {
	return func() tea.Msg {
		var removeFn func(string, string, string, bool) error
		if gitOp != nil {
			removeFn = gitOp.RemoveWorktree
		} else {
			removeFn = git.RemoveWorktree
		}
		if err := removeFn(repo, path, branch, force); err != nil {
			return worktreeDeletedMsg{path: path, err: err}
		}

		// Best-effort cleanup of the worktree's tmux attachment. Failures here
		// do not undo the successful removal — the directory is already gone.
		killTmuxTargetForDir(ops, path, mode)
		return worktreeDeletedMsg{path: path}
	}
}

// pullFinishedMsg is returned by pullCmd after `git pull --ff-only --no-tags
// --autostash origin <branch>` completes for the worktree at path. branch is the
// branch that was pulled (used to phrase the status hint); summary is git's
// one-line result on success; err is non-nil when the pull failed (e.g. diverged
// history, missing origin, network error). path tags the result so the handler
// can clear the matching in-flight indicator.
type pullFinishedMsg struct {
	path    string
	branch  string
	summary string
	err     error
}

// pullCmd fast-forwards the branch checked out in the worktree at path off the
// UI goroutine, reporting the outcome as a pullFinishedMsg tagged with path. It
// prefers the injected gitOp seam and falls back to the package-level git.Pull
// when gitOp is nil (zero-value model in tests).
func pullCmd(gitOp gitOps, path, branch string) tea.Cmd {
	return func() tea.Msg {
		pullFn := git.Pull
		if gitOp != nil {
			pullFn = gitOp.Pull
		}
		summary, err := pullFn(path, branch)
		return pullFinishedMsg{path: path, branch: branch, summary: summary, err: err}
	}
}

// addPulling records an in-flight pull for the worktree at path so the row
// renders its spinner and a repeated 'P' is ignored while it is running.
func (m *model) addPulling(path string) {
	if m.pulling == nil {
		m.pulling = map[string]bool{}
	}
	m.pulling[path] = true
}

// canPullWorktree reports whether the branch in row's worktree can be pulled,
// returning a user-facing reason when it cannot. A worktree that is not yet on
// disk (creating) or whose directory is missing has nowhere to run git; a
// detached HEAD (empty branch) has no branch name to pull from origin.
func canPullWorktree(row settings.Row) (bool, string) {
	switch {
	case row.Worktree == "":
		return false, "no worktree selected"
	case row.State == settings.StateCreating:
		return false, "worktree is still being created"
	case row.State == settings.StateMissing:
		return false, "worktree directory is missing — cannot pull"
	case row.Branch == "":
		return false, "cannot pull: detached HEAD has no branch"
	}
	return true, ""
}

// removeWorktreeRow optimistically drops target's row from the visible table
// and records it in pendingDeletes so a failed deletion can restore it. The
// session cursor is clamped so it never points past the shortened list.
func (m *model) removeWorktreeRow(target settings.Row) {
	if m.pendingDeletes == nil {
		m.pendingDeletes = map[string]settings.Row{}
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

// restoreWorktreeRow re-inserts a row that was optimistically removed for a
// deletion that then failed. It is placed immediately after its repo's last
// existing row (appended only when the repo has no other rows) so same-repo
// rows stay contiguous in the flat list. This matters because renderWorkspaceRows
// groups rows by repo while the session cursor indexes the flat slice: a row
// appended out of group order renders inside its group but carries an
// out-of-sequence index, making j/k navigation skip over it. The session cursor
// is nudged so it keeps pointing at the same row. The next snapshot rebuild
// reconciles exact intra-repo ordering.
func (m *model) restoreWorktreeRow(saved settings.Row) {
	insertAt := len(m.workspaceRows)
	for i, row := range m.workspaceRows {
		if row.Repo == saved.Repo {
			insertAt = i + 1
		}
	}
	m.workspaceRows = append(m.workspaceRows, settings.Row{})
	copy(m.workspaceRows[insertAt+1:], m.workspaceRows[insertAt:])
	m.workspaceRows[insertAt] = saved
	if insertAt <= m.sessionCursor {
		m.sessionCursor++
	}
}

// filterPendingDeletes drops rows whose worktree is awaiting deletion. A
// snapshot-driven rebuild can list a worktree that git has not finished
// removing yet; without this filter the row would flash back into the table
// between the confirmation and the deletion completing. Returns rows unchanged
// when nothing is pending.
func filterPendingDeletes(rows []settings.Row, pending map[string]settings.Row) []settings.Row {
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

// pendingCreate is an in-flight worktree creation, shown as an optimistic
// spinner row until newWorktreeCmd reports completion. fromRemote distinguishes
// the 'F' fetch flow ("fetching…") from the 'n' create flow ("creating…").
type pendingCreate struct {
	repo       string // canonical repo path (the render group + Row.Repo)
	dest       string // raw destination path, used as the placeholder Row.Worktree
	branch     string
	fromRemote bool
}

// createKey identifies a pending create by repo and branch. dest is derived
// from these, but the post-create canonical dest can differ from the raw dest
// (symlink resolution), so repo+branch is the stable correlation key between
// dispatch and the worktreeCreatedMsg that clears it.
func createKey(repo, branch string) string {
	return repo + "\x00" + branch
}

// worktreeDest derives a new worktree's destination path: a sibling of the repo
// named after the branch (e.g. /home/user/myrepo + "feat" → /home/user/myrepo-feat).
// Shared by newWorktreeCmd and the dispatch site so the placeholder row's path
// matches the path the worktree is actually created at.
func worktreeDest(repoPath, branch string) string {
	return filepath.Join(filepath.Dir(repoPath), filepath.Base(repoPath)+"-"+branch)
}

// addPendingCreate records an in-flight creation so injectPendingCreates can
// render its spinner row.
func (m *model) addPendingCreate(repo, dest, branch string, fromRemote bool) {
	if m.pendingCreates == nil {
		m.pendingCreates = map[string]pendingCreate{}
	}
	m.pendingCreates[createKey(repo, branch)] = pendingCreate{
		repo: repo, dest: dest, branch: branch, fromRemote: fromRemote,
	}
}

// clearPendingCreate removes the in-flight create for repo+branch and drops its
// placeholder spinner row from the visible table (so a completed or failed
// create stops animating immediately, rather than freezing until the next
// snapshot rebuild). The session cursor is clamped so it never dangles.
func (m *model) clearPendingCreate(repo, branch string) {
	delete(m.pendingCreates, createKey(repo, branch))
	if len(m.workspaceRows) == 0 {
		return
	}
	filtered := m.workspaceRows[:0:0]
	for _, row := range m.workspaceRows {
		if row.State == settings.StateCreating && row.Repo == repo && row.Branch == branch {
			continue
		}
		filtered = append(filtered, row)
	}
	m.workspaceRows = filtered
	if n := len(m.workspaceRows); n == 0 {
		m.sessionCursor = 0
	} else if m.sessionCursor >= n {
		m.sessionCursor = n - 1
	}
}

// injectPendingCreates appends a StateCreating placeholder row for every
// in-flight create not already present in rows. It is applied after every row
// (re)build so a snapshot rebuild mid-fetch cannot drop the spinner row. The
// "already present" check matches by repo+branch (any state), so a real row
// that has appeared, or a placeholder already injected, is never duplicated.
// Placeholders are appended in a stable repo+branch order to avoid frame-to-
// frame jitter when several creates run at once.
func injectPendingCreates(rows []settings.Row, pending map[string]pendingCreate) []settings.Row {
	if len(pending) == 0 {
		return rows
	}
	keys := make([]string, 0, len(pending))
	for k := range pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := rows
	for _, k := range keys {
		pc := pending[k]
		present := false
		for _, row := range rows {
			if row.Repo == pc.repo && row.Branch == pc.branch {
				present = true
				break
			}
		}
		if present {
			continue
		}
		out = append(out, settings.Row{
			Repo:     pc.repo,
			Worktree: pc.dest,
			Branch:   pc.branch,
			State:    settings.StateCreating,
		})
	}
	return out
}

// canDeleteWorktree reports whether row may be deleted, returning a user-facing
// reason when it may not. The repository's primary worktree (Worktree == Repo)
// and rows not associated with a configured repo are protected: git refuses the
// former, and the latter has no repo root to run `git worktree remove` from.
func canDeleteWorktree(row settings.Row) (bool, string) {
	if row.Worktree == "" {
		return false, "no worktree selected"
	}
	if row.State == settings.StateCreating {
		return false, "worktree is still being created"
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
	// Kick off the initial Workspaces-view load so it is ready before the
	// user ever presses Tab, rather than waiting for the first snapshot.
	// Gated behind !m.demo (mirroring the snapshotMsg row build below) so
	// --demo never touches the workspaces store and its capture stays
	// deterministic.
	var wsC tea.Cmd
	if !m.demo {
		wsC = loadWorkspaceStatusCmd(m.store, m.snap)
	}
	return tea.Batch(waitSnapshot(m.snaps), tick, wsC)
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

// paneHeights returns the total and inner heights for the sessions pane under
// the model's current terminal and footer state. Keeping this calculation
// shared between Update and View lets cursor movement adjust the sessions
// viewport using exactly the height View will render.
func (m model) paneHeights() (sessionsOuterH, sessionsInnerH int) {
	extraFooterRows := 0
	if m.debug && unreachableFooter(m.snap.UnreachableInstances) != "" {
		extraFooterRows++
	}

	// The application header and legend always reserve one row each.
	sessionsOuterH = max(6, m.height-2-extraFooterRows)
	sessionsInnerH = max(1, sessionsOuterH-2)
	return sessionsOuterH, sessionsInnerH
}

// syncSessionScroll moves the grouped sessions viewport just enough to keep
// the selected worktree visible. The scroll offset indexes display lines, so
// repo headers are naturally accounted for when the cursor crosses a group.
func (m *model) syncSessionScroll() {
	lines := workspaceDisplayLines(m.workspaceRows)
	listHeight := m.sessionsListHeight()
	start, _ := workspaceWindow(lines, m.sessionCursor, m.sessionScroll, listHeight)
	m.sessionScroll = start
}

// sessionsListHeight is the number of grouped repo/worktree lines available
// inside the sessions pane after its title and pinned hint/prompt lines.
func (m model) sessionsListHeight() int {
	_, innerH := m.paneHeights()
	return max(0, innerH-1-m.workspaceFooterLineCount())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// (a) Prompt mode pre-empt — evaluated before any global or pane key.
		// This ensures Esc inside a prompt clears the prompt rather than quitting.
		if m.prompt != promptIdle {
			switch m.prompt {
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
					// Carry the branch forward. When a resolvable default harness
					// is configured, skip the chooser and launch with it directly;
					// otherwise open the chooser for a per-launch choice.
					m.newWorktreeBranch = branch
					m.input.Blur()
					m.input.SetValue("")
					fromRemote := m.worktreeFromRemote
					if dk := resolvedDefaultHarness(m.harnOp); dk != "" {
						m2, cmd := m.startNewWorktree(repoPath, branch, dk, fromRemote)
						return m2, tea.Batch(inputCmd, cmd)
					}
					m.prompt = promptChooseHarness
					m.harnessChooserKinds = harnessChooserKinds(m.harnOp)
					m.harnessChooserCursor = defaultHarnessIndex(m.harnessChooserKinds)
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
				// dispatches newWorktreeCmd — or, when this chooser was opened by
				// the Workspaces view's 'n' (wsCreateTarget set),
				// assembleWorkspaceSessionCmd instead; esc cancels the whole flow.
				switch msg.String() {
				case "enter":
					var chosenKind string
					if len(m.harnessChooserKinds) > 0 {
						idx := clampIndex(m.harnessChooserCursor, len(m.harnessChooserKinds))
						chosenKind = string(m.harnessChooserKinds[idx])
					}
					if chosenKind == "" {
						chosenKind = string(harness.KindOpenCode)
					}
					if m.wsCreateTarget != "" {
						return m.startWorkspaceSessionCreate(m.wsCreateTarget, m.wsCreateSessionName, chosenKind)
					}
					branch := m.newWorktreeBranch
					repoPath := m.newWorktreeRepo
					return m.startNewWorktree(repoPath, branch, chosenKind, m.worktreeFromRemote)

				case "esc":
					m.prompt = promptIdle
					m.newWorktreeRepo = ""
					m.newWorktreeBranch = ""
					m.worktreeFromRemote = false
					m.wsCreateTarget = ""
					m.wsCreateSessionName = ""
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

			case promptNewWorkspace:
				// Name prompt for 'N' (new, empty workspace). On enter, a
				// non-empty name is dispatched to createWorkspaceCmd; esc or an
				// empty name cancels without creating anything.
				switch msg.String() {
				case "enter":
					name := strings.TrimSpace(m.input.Value())
					_, inputCmd := m.input.Update(msg)
					m.prompt = promptIdle
					m.input.Blur()
					m.input.SetValue("")
					if name == "" {
						return m, inputCmd
					}
					return m, tea.Batch(inputCmd, createWorkspaceCmd(m.store, name))

				case "esc":
					m.prompt = promptIdle
					m.input.Blur()
					m.input.SetValue("")
					return m, nil

				default:
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					return m, cmd
				}

			case promptNewWorkspaceSession:
				// Name prompt for 'n' (new session) in the workspace captured in
				// wsCreateTarget. On enter, a valid name advances to the shared
				// promptChooseHarness; an invalid or duplicate name is refused via
				// wsHint and the prompt stays open so the user can retype. esc or
				// an empty name cancels the whole flow.
				switch msg.String() {
				case "enter":
					name := strings.TrimSpace(m.input.Value())
					target := m.wsCreateTarget
					_, inputCmd := m.input.Update(msg)
					if name == "" {
						m.prompt = promptIdle
						m.input.Blur()
						m.input.SetValue("")
						m.wsCreateTarget = ""
						return m, inputCmd
					}
					if err := m.validateNewWsSessionName(target, name); err != nil {
						m.wsHint = err.Error()
						return m, inputCmd
					}
					m.wsCreateSessionName = name
					m.wsHint = ""
					m.input.Blur()
					m.input.SetValue("")
					if dk := resolvedDefaultHarness(m.harnOp); dk != "" {
						return m.startWorkspaceSessionCreate(target, name, dk)
					}
					m.prompt = promptChooseHarness
					m.harnessChooserKinds = harnessChooserKinds(m.harnOp)
					m.harnessChooserCursor = defaultHarnessIndex(m.harnessChooserKinds)
					return m, inputCmd

				case "esc":
					m.prompt = promptIdle
					m.input.Blur()
					m.input.SetValue("")
					m.wsCreateTarget = ""
					m.wsCreateSessionName = ""
					return m, nil

				default:
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					return m, cmd
				}

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
				m.deleteTarget = settings.Row{}
				m.deleteMergeInfo = ""
				return m, nil

			case promptConfirmDeleteWorktree2:
				// Second confirmation. Default is cancel: only an explicit 'y'
				// proceeds; every other key (including esc/enter) aborts. This
				// is the last gate before an irreversible removal.
				if msg.String() == "y" || msg.String() == "Y" {
					target := m.deleteTarget
					m.prompt = promptIdle
					m.deleteTarget = settings.Row{}
					m.deleteMergeInfo = ""
					// Optimistically drop the row now: git worktree removal can
					// take a few seconds, and leaving the row visible looks like
					// the keypress was ignored. removeWorktreeRow stashes the row
					// in pendingDeletes so a failed deletion can restore it.
					m.removeWorktreeRow(target)
					// deleteForce was resolved from config when the flow opened
					// ('D'), so the action matches the warning shown at confirm.
					return m, deleteWorktreeCmd(m.tmux, m.gitOp, target.Repo, target.Worktree, target.Branch, m.launchMode, m.deleteForce)
				}
				m.prompt = promptIdle
				m.deleteTarget = settings.Row{}
				m.deleteMergeInfo = ""
				return m, nil

			case promptConfirmDeleteWsSession:
				// First confirmation for a single workspace session. 'y'
				// advances to the second prompt; any other key (including
				// esc) cancels.
				if msg.String() == "y" || msg.String() == "Y" {
					m.prompt = promptConfirmDeleteWsSession2
					return m, nil
				}
				m.clearWsDeleteTarget()
				return m, nil

			case promptConfirmDeleteWsSession2:
				// Second (last) confirmation. Default is cancel: only an
				// explicit 'y' proceeds; every other key (including
				// esc/enter) aborts.
				if msg.String() == "y" || msg.String() == "Y" {
					wsName, sessName := m.wsDeleteWorkspace, m.wsDeleteSession
					m.clearWsDeleteTarget()
					return m, deleteWsSessionCmd(m.store, m.tmux, wsName, sessName, m.launchMode)
				}
				m.clearWsDeleteTarget()
				return m, nil

			case promptConfirmDeleteWorkspace:
				// First confirmation for an entire workspace (every session).
				// 'y' advances to the second prompt; any other key cancels.
				if msg.String() == "y" || msg.String() == "Y" {
					m.prompt = promptConfirmDeleteWorkspace2
					return m, nil
				}
				m.clearWsDeleteTarget()
				return m, nil

			case promptConfirmDeleteWorkspace2:
				// Second (last) confirmation. Default is cancel: only an
				// explicit 'y' proceeds.
				if msg.String() == "y" || msg.String() == "Y" {
					wsName := m.wsDeleteWorkspace
					m.clearWsDeleteTarget()
					return m, deleteWorkspaceCmd(m.store, m.tmux, wsName, m.launchMode)
				}
				m.clearWsDeleteTarget()
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

			case promptSwitchSession:
				// Session switcher. Enter jumps to the highlighted row; the
				// arrow keys (and ctrl+n/p) move the selection; esc closes;
				// everything else edits the filter query and re-ranks matches.
				switch msg.String() {
				case "esc":
					m.closeSessionPalette()
					return m, nil
				case "enter":
					if len(m.sessionPaletteMatches) == 0 {
						return m, nil
					}
					idx := m.sessionPaletteMatches[clampIndex(m.sessionPaletteCursor, len(m.sessionPaletteMatches))]
					row := m.sessionPaletteRows[idx]
					target := m.sessionPaletteTargets[idx]
					m.closeSessionPalette()
					// Apply the same guards as the sessions-pane Enter handler.
					tmuxAvail := m.tmux != nil && m.tmux.Available()
					if !tmuxAvail {
						m.tmuxHint = "tmux not available — start cogitator inside a tmux session to use jump/resume"
						return m, nil
					}
					if row.State == settings.StateMissing {
						m.tmuxHint = "worktree directory is missing — cannot resume"
						return m, nil
					}
					if row.State == settings.StateCreating {
						m.tmuxHint = "worktree is still being created…"
						return m, nil
					}
					// Sync the sessions-pane cursor to the chosen row so the
					// highlight reflects where the jump landed. A workspace
					// session's target dir has no matching entry in
					// m.workspaceRows, so this is a harmless no-op for those.
					for i, r := range m.workspaceRows {
						if r.Worktree == target.dir {
							m.sessionCursor = i
							break
						}
					}
					m.recordSessionSwitch(target.dir)
					return m, launchCmd(m.tmux, target, m.harnOp, m.launchMode, resolvedDefaultHarness(m.harnOp))
				case "up", "ctrl+p":
					m.sessionPaletteCursor = clampIndex(m.sessionPaletteCursor-1, len(m.sessionPaletteMatches))
					return m, nil
				case "down", "ctrl+n":
					m.sessionPaletteCursor = clampIndex(m.sessionPaletteCursor+1, len(m.sessionPaletteMatches))
					return m, nil
				default:
					prev := m.input.Value()
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					m.sessionPaletteMatches = fuzzyMatchIndices(m.input.Value(), m.sessionPaletteLabels)
					// Searching resets selection to the first match; only clamp
					// when the query is unchanged so the previous-session seed
					// (row 1) survives non-editing keys.
					if m.input.Value() != prev {
						m.sessionPaletteCursor = 0
					} else {
						m.sessionPaletteCursor = clampIndex(m.sessionPaletteCursor, len(m.sessionPaletteMatches))
					}
					return m, cmd
				}

			case promptHelp:
				// Passive help overlay: any key dismisses it.
				m.prompt = promptIdle
				return m, nil

			case promptSettings:
				return m.updateSettings(msg)

			case promptWorkspaceModal:
				return m.updateWorkspaceModalActive(msg)
			}
		}

		// (b) Global quit — only when no prompt is active.
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}

		// (b.1) Session switcher — ctrl+P opens the fuzzy "go to session"
		// palette over every worktree row and workspace session. Global
		// (works regardless of which pane is focused) and only reachable
		// here when no prompt is active, since the prompt pre-empt block
		// above short-circuits first.
		if msg.String() == "ctrl+p" {
			candidates, startOnPrevious := m.orderedSessionCandidates()
			if len(candidates) == 0 {
				m.tmuxHint = "no sessions to switch to"
				return m, nil
			}
			m.sessionPaletteRows = make([]settings.Row, len(candidates))
			m.sessionPaletteTargets = make([]launchTarget, len(candidates))
			m.sessionPaletteLabels = make([]string, len(candidates))
			for i, c := range candidates {
				m.sessionPaletteRows[i] = c.row
				m.sessionPaletteTargets[i] = c.target
				m.sessionPaletteLabels[i] = c.label
			}
			m.sessionPaletteMatches = fuzzyMatchIndices("", m.sessionPaletteLabels)
			// Seed the cursor on the previous session (row 1) so ctrl+P then enter
			// jumps straight back to it; only when a genuine previous exists.
			m.sessionPaletteCursor = 0
			if startOnPrevious {
				m.sessionPaletteCursor = 1
			}
			m.prompt = promptSwitchSession
			m.input.Placeholder = "go to session"
			m.input.SetValue("")
			return m, m.input.Focus()
		}

		// (b.2) Help overlay — '?' opens the floating keybinding reference.
		// Global (works in either pane) and only reachable here when no prompt
		// is active, since the prompt pre-empt block short-circuits first.
		if msg.String() == "?" {
			m.prompt = promptHelp
			return m, nil
		}

		// (b.3) Settings overlay — 'S' opens the persistent-settings modal
		// (default harness, launch mode). Global; only reachable when no prompt
		// is active, since the pre-empt block above short-circuits first.
		if msg.String() == "S" {
			m.openSettings()
			return m, nil
		}

		// (b.4) View swap — Tab toggles the whole content area between the
		// Sessions and Workspaces views. Global; only reachable when no prompt
		// is active, since the pre-empt block above short-circuits first. Each
		// view keeps its own cursor/scroll state, so swapping never disturbs
		// the other view's position.
		if msg.String() == "tab" {
			if m.view == viewWorkspaces {
				m.view = viewSessions
			} else {
				m.view = viewWorkspaces
			}
			return m, nil
		}

		// Workspaces-view keys route through their own methods rather than
		// adding arms here, following updateSettings's precedent — this
		// switch already exceeds the configured gocyclo minimum. Lifecycle
		// keys (create workspace/session) are tried first, since they open a
		// prompt or dispatch a Cmd rather than moving the cursor; delete
		// ('D', opening the merge-status confirm) is tried next; launch
		// ('enter' on a session row) after that; anything else falls through
		// to the pure-navigation handler in workspace_view.go.
		// updateWorkspaceDelete/updateWorkspaceLaunch live here (not
		// workspace_view.go/workspace_cmd.go) because both of those files are
		// untouched by this feature.
		if m.view == viewWorkspaces {
			if next, cmd, handled := m.updateWorkspaceLifecycle(msg); handled {
				return next, cmd
			}
			if next, cmd, handled := m.updateWorkspaceDelete(msg); handled {
				return next, cmd
			}
			if next, cmd, handled := m.updateWorkspaceLaunch(msg); handled {
				return next, cmd
			}
			if next, cmd, handled := m.updateWorkspaceModal(msg); handled {
				return next, cmd
			}
			return m.updateWorkspaceView(msg)
		}

		// Sessions-focused keys. The sessions pane is the only pane, so every
		// key reaches this switch once the global/prompt handlers above have
		// passed on it.
		// Clear any transient tmux hint on any key press.
		m.tmuxHint = ""

		// `gg` jumps to the top: the first `g` arms pendingG, the second
		// fires. Any other key clears it.
		wasPendingG := m.pendingG
		m.pendingG = false

		switch msg.String() {
		case "a":
			m.recentCollapsed = !m.recentCollapsed
		case "j", "down":
			if n := len(m.workspaceRows); n > 0 {
				m.sessionCursor = min(m.sessionCursor+1, n-1)
				m.syncSessionScroll()
			}
		case "k", "up":
			if n := len(m.workspaceRows); n > 0 {
				m.sessionCursor = max(m.sessionCursor-1, 0)
				m.syncSessionScroll()
			}
		case "g":
			if wasPendingG {
				m.sessionCursor = 0
				m.syncSessionScroll()
			} else {
				m.pendingG = true
			}
		case "<":
			m.sessionCursor = 0
			m.syncSessionScroll()
		case "G", ">":
			if n := len(m.workspaceRows); n > 0 {
				m.sessionCursor = n - 1
				m.syncSessionScroll()
			}
		case "ctrl+d":
			m.sessionCursor = m.repoBoundary(1)
			m.syncSessionScroll()
		case "ctrl+u":
			m.sessionCursor = m.repoBoundary(-1)
			m.syncSessionScroll()

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
			if row.State == settings.StateMissing {
				m.tmuxHint = "worktree directory is missing — cannot resume"
				return m, nil
			}

			// Pending-create placeholder rows are not on disk yet.
			if row.State == settings.StateCreating {
				m.tmuxHint = "worktree is still being created…"
				return m, nil
			}

			target := rowLaunchTarget(row)
			m.recordSessionSwitch(target.dir)
			return m, launchCmd(m.tmux, target, m.harnOp, m.launchMode, resolvedDefaultHarness(m.harnOp))

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
			// Resolve force-delete once, here, so the confirm prompt's
			// data-loss warning and the eventual removal agree on the flag.
			m.deleteForce = true
			if wsCfg, err := settings.LoadConfig(); err == nil {
				m.deleteForce = wsCfg.ForceDeleteEnabled()
			}
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

		case "P":
			// Pull latest into the highlighted worktree's branch from origin.
			// Handy for refreshing a base branch before branching a new
			// worktree off it. tmux is not required — this is a pure git
			// operation.
			if len(m.workspaceRows) == 0 {
				return m, nil
			}
			row := m.workspaceRows[m.sessionCursor]
			if ok, reason := canPullWorktree(row); !ok {
				m.tmuxHint = reason
				return m, nil
			}
			if m.pulling[row.Worktree] {
				// A pull for this worktree is already in flight; ignore the
				// repeated keypress rather than dispatching a duplicate.
				return m, nil
			}
			m.addPulling(row.Worktree)
			var spinnerC tea.Cmd
			if !m.spinnerActive {
				m.spinnerActive = true
				spinnerC = spinnerTickCmd()
			}
			return m, tea.Batch(pullCmd(m.gitOp, row.Worktree, row.Branch), spinnerC)
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Recompute the input width so the prompt fits inside the bordered
		// sessions pane. The prefix "fetch branch from origin: " is the
		// longest prompt label; we reserve that much space so the cursor
		// never overflows the pane boundary.
		const promptLabelLen = len("fetch branch from origin: ")
		m.input.Width = max(0, paneInnerWidth(m.width)-promptLabelLen)
		m.syncSessionScroll()
		m.syncWsScroll()

	case launchResultMsg:
		// A launch/resume Cmd completed.
		if msg.err != nil {
			m.tmuxHint = fmt.Sprintf("launch error: %v", msg.err)
			return m, nil
		}
		if m.viewMarker != nil && msg.sessionID != "" {
			// The user landed in the session — clear any "work finished" badge.
			m.viewMarker.MarkViewed(harness.Kind(msg.provider), msg.instanceID, msg.sessionID)
		}
		// When a configured default overrode the row's recorded harness on a
		// cold (re)launch, persist the new harness to the roster so the row and
		// status display reflect what is now running. Mirrors the create-time
		// roster write; Title/SessionID refresh on the next discovery snapshot.
		if msg.launched && msg.harnessKind != "" && msg.dir != "" && m.rosterUpserts != nil {
			entry := settings.RosterEntry{
				Dir:          msg.dir,
				Harness:      msg.harnessKind,
				Provider:     msg.harnessKind,
				LastActivity: time.Now(),
			}
			select {
			case m.rosterUpserts <- entry:
			default:
			}
		}
		return m, nil

	case worktreeCreatedMsg:
		// A new-worktree Cmd completed. Clear its optimistic spinner row first
		// (whether it succeeded or failed): on success the real worktree row
		// arrives via the next snapshot rebuild; on failure nothing was created.
		m.clearPendingCreate(msg.repo, msg.branch)
		// On success, write a create-time roster entry so the harness kind is
		// persisted before any live-discovery snapshot arrives (Codex sessions
		// are never live-discovered, so without this write the roster would never
		// record the harness kind).
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
				entry := settings.RosterEntry{
					Dir:          msg.canonDest,
					Harness:      kind,
					Provider:     kind,
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
			// waiting for the next snapshot. Reapply the create overlay so an
			// in-flight fetch's spinner row is not dropped by the rebuild.
			rows, mode, root := buildWorkspaceRows(m.snap, m.cfg)
			m.workspaceRows = injectPendingCreates(rows, m.pendingCreates)
			m.launchMode = mode
			m.workspaceRoot = root
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
			// waiting for the next snapshot. Reapply the create overlay so an
			// in-flight fetch's spinner row is not dropped by the rebuild.
			rows, mode, root := buildWorkspaceRows(m.snap, m.cfg)
			m.workspaceRows = injectPendingCreates(rows, m.pendingCreates)
			m.launchMode = mode
			m.workspaceRoot = root
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

	case wsModalScanMsg:
		// Background repo-membership scan finished. Ignore a stale result if
		// the modal was closed, or reopened for a different workspace, in
		// the meantime.
		if m.prompt != promptWorkspaceModal || msg.workspace != m.wsModalWorkspace {
			return m, nil
		}
		m.wsModalScanning = false
		if msg.err != nil {
			m.wsModalErr = fmt.Sprintf("scan failed: %v", msg.err)
			m.wsModalEntries = nil
			m.wsModalMatches = nil
			return m, nil
		}
		m.wsModalErr = ""
		m.wsModalEntries = msg.entries
		m.wsModalMatches = fuzzyMatchIndices(m.input.Value(), wsModalEntryPaths(m.wsModalEntries))
		m.wsModalCursor = clampIndex(m.wsModalCursor, len(m.wsModalMatches))
		return m, nil

	case wsModalActionErrMsg:
		// A committed attach/detach failed validation or persistence; report
		// it in wsHint since the modal has already closed by the time this
		// arrives. membershipChangedMsg (the success case) is deliberately
		// left unhandled here — step 15 (workspace_backfill.go) is the first
		// consumer of that message.
		m.wsHint = fmt.Sprintf("membership change failed: %v", msg.err)
		return m, nil

	case mergeStatusMsg:
		// Annotate the active delete confirmation, but only if it still targets
		// the same worktree the probe was launched for (guards against a stale
		// result arriving after cancel or retarget).
		if (m.prompt == promptConfirmDeleteWorktree || m.prompt == promptConfirmDeleteWorktree2) &&
			msg.path == m.deleteTarget.Worktree {
			m.deleteMergeInfo = mergeInfoText(msg.state, msg.base)
		}
		// Same guard for the workspace/session delete confirm, but against the
		// snapshotted wsDeleteMembers list (a bundle probes one path per
		// member, not a single deleteTarget) — a probe whose path is not
		// among the current members is a stale result from a cancelled or
		// since-retargeted confirm and is dropped.
		if wsDeletePromptActive(m.prompt) {
			for _, mem := range m.wsDeleteMembers {
				if mem.worktreePath == msg.path {
					if m.wsDeleteMergeInfo == nil {
						m.wsDeleteMergeInfo = map[string]string{}
					}
					m.wsDeleteMergeInfo[msg.path] = mergeInfoText(msg.state, msg.base)
					break
				}
			}
		}
		return m, nil

	case worktreeDeletedMsg:
		if msg.err != nil {
			m.tmuxHint = fmt.Sprintf("delete failed: %v", msg.err)
			// The row was optimistically removed at confirm time; restore it so
			// a failed deletion does not silently drop the worktree from view.
			if saved, ok := m.pendingDeletes[msg.path]; ok {
				m.restoreWorktreeRow(saved)
				delete(m.pendingDeletes, msg.path)
			}
			return m, nil
		}
		// Success: clear the pending entry. The row was already dropped when the
		// deletion was confirmed; remove it again defensively (idempotent) in
		// case it was never optimistically removed (e.g. a direct dispatch).
		delete(m.pendingDeletes, msg.path)
		var remaining []settings.Row
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

	case pullFinishedMsg:
		// Clear the in-flight indicator and surface the outcome as a transient
		// hint (the next snapshot rebuild reconciles the row's real state).
		delete(m.pulling, msg.path)
		branch := msg.branch
		if branch == "" {
			branch = "branch"
		}
		switch {
		case msg.err != nil:
			m.tmuxHint = fmt.Sprintf("pull %s failed: %v", branch, msg.err)
		case msg.summary != "":
			m.tmuxHint = fmt.Sprintf("pulled %s: %s", branch, msg.summary)
		default:
			m.tmuxHint = "pulled " + branch
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
		// Demo mode curates workspaceRows directly; the git/tmux build must
		// never run (it would shell out and clobber the fixture with nil).
		if !m.demo {
			if m.rowsBuilding {
				// A build is already in flight; mark dirty so the completion
				// handler dispatches one follow-up build with the latest snap.
				m.rowsDirty = true
			} else {
				m.rowsBuilding = true
				buildC = buildWorkspaceRowsCmd(m.snap, m.cfg)
			}
		}
		var wsC tea.Cmd
		// Same coalescing, same demo gate, for the Workspaces view's status
		// load — it also touches the workspaces store and must stay out of
		// the deterministic --demo capture.
		if !m.demo {
			if m.wsBuilding {
				m.wsDirty = true
			} else {
				m.wsBuilding = true
				wsC = loadWorkspaceStatusCmd(m.store, m.snap)
			}
		}
		return m, tea.Batch(next, bellC, buildC, wsC)

	case workspaceRowsMsg:
		// Apply both optimistic overlays: drop rows awaiting deletion, and
		// re-inject placeholder spinner rows for in-flight creates (a freshly
		// built list never contains either, so they must be reapplied here).
		rows := filterPendingDeletes(msg.rows, m.pendingDeletes)
		m.workspaceRows = injectPendingCreates(rows, m.pendingCreates)
		m.launchMode = msg.launchMode
		m.workspaceRoot = msg.root
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

	case wsStatusMsg:
		// msg.statuses is always freshly merged (never carries a placeholder),
		// so re-inject any in-flight session creates before storing it —
		// otherwise a snapshot-driven reload while 'n' is assembling would drop
		// the spinner row until assembleWorkspaceSessionCmd completes.
		m.wsStatuses = injectPendingWsSessions(msg.statuses, m.wsPendingSessions, m.spinnerFrame)
		m.clampWsCursor()
		m.wsBuilding = false
		if m.wsDirty {
			m.wsDirty = false
			m.wsBuilding = true
			return m, loadWorkspaceStatusCmd(m.store, m.snap)
		}
		return m, nil

	case wsWorkspaceCreatedMsg:
		// Outcome of createWorkspaceCmd ('N'). On success, refresh from the
		// store so the new workspace appears without waiting for the next
		// snapshot, mirroring repoAddMsg's immediate rebuild for the Sessions
		// pane; coalesce with an in-flight load exactly as snapshotMsg does.
		if msg.err != nil {
			m.wsHint = fmt.Sprintf("create workspace %q failed: %v", msg.name, msg.err)
			return m, nil
		}
		m.wsHint = ""
		if m.wsBuilding {
			m.wsDirty = true
			return m, nil
		}
		m.wsBuilding = true
		return m, loadWorkspaceStatusCmd(m.store, m.snap)

	case wsSessionAssembledMsg:
		// Outcome of assembleWorkspaceSessionCmd ('n' in the Workspaces view).
		// Clear the optimistic spinner row first regardless of outcome: on
		// success the real session arrives via the reload dispatched below; on
		// failure nothing was persisted (assembleWorkspaceSessionCmd rolls back
		// its own partial work).
		m.clearPendingWsSession(msg.workspaceName, msg.sessionName)
		if msg.err != nil {
			m.wsHint = fmt.Sprintf("create session %q failed: %v", msg.sessionName, msg.err)
			return m, nil
		}
		m.wsHint = ""
		if m.wsBuilding {
			m.wsDirty = true
			return m, nil
		}
		m.wsBuilding = true
		return m, loadWorkspaceStatusCmd(m.store, m.snap)

	case wsSessionDeletedMsg:
		// Outcome of deleteWsSessionCmd ('D', both confirms, on a session
		// row). On success, refresh from the store so the removed session
		// disappears without waiting for the next snapshot; on failure
		// nothing was dropped (deleteWsSessionCmd only calls RemoveSession
		// once TeardownSession reports no per-repo failures), so the row
		// stays exactly as it was and the error is surfaced instead.
		if msg.err != nil {
			m.wsHint = fmt.Sprintf("delete session %q failed: %v", msg.sessionName, msg.err)
			return m, nil
		}
		m.wsHint = ""
		if m.wsBuilding {
			m.wsDirty = true
			return m, nil
		}
		m.wsBuilding = true
		return m, loadWorkspaceStatusCmd(m.store, m.snap)

	case wsWorkspaceDeletedMsg:
		// Outcome of deleteWorkspaceCmd ('D', both confirms, on a workspace
		// header/hint row). Mirrors wsSessionDeletedMsg: refresh on success,
		// surface the error on failure (the workspace and every session —
		// torn down or not — are left in the store when any session's
		// teardown failed).
		if msg.err != nil {
			m.wsHint = fmt.Sprintf("delete workspace %q failed: %v", msg.workspaceName, msg.err)
			return m, nil
		}
		m.wsHint = ""
		if m.wsBuilding {
			m.wsDirty = true
			return m, nil
		}
		m.wsBuilding = true
		return m, loadWorkspaceStatusCmd(m.store, m.snap)

	case tickMsg:
		// Re-arm the ticker and record the current time so View() can render
		// fresh relative timestamps without calling time.Now() on every frame.
		m.tickNow = time.Time(msg)
		return m, tickCmd()

	case spinnerTickMsg:
		// Advance the spinner shared by pending creates, in-flight pulls, and
		// pending workspace-session creates. Stop re-arming once none remain
		// so the ticker costs nothing when idle; reset the frame so the next
		// operation starts from the first glyph.
		if len(m.pendingCreates) == 0 && len(m.pulling) == 0 && len(m.wsPendingSessions) == 0 {
			m.spinnerActive = false
			m.spinnerFrame = 0
			return m, nil
		}
		m.spinnerFrame++
		if len(m.wsPendingSessions) > 0 {
			// Unlike the Sessions pane's formatCreatingRow (a model method
			// that reads m.spinnerFrame at render time), formatWsSessionRow
			// (workspace_view.go) is a pure function of workspace.SessionStatus
			// alone, so the animated glyph must be baked into the placeholder's
			// data on every tick rather than read live at render time.
			m.wsStatuses = injectPendingWsSessions(stripPendingWsSessions(m.wsStatuses), m.wsPendingSessions, m.spinnerFrame)
		}
		return m, spinnerTickCmd()
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

// closeSessionPalette resets the ctrl+P session switcher back to the idle
// state, mirroring closeRepoFinder. Callers that need to jump afterwards read
// the selected row before calling this, since it clears the candidate slices.
func (m *model) closeSessionPalette() {
	m.prompt = promptIdle
	m.input.Blur()
	m.input.SetValue("")
	m.sessionPaletteRows = nil
	m.sessionPaletteTargets = nil
	m.sessionPaletteLabels = nil
	m.sessionPaletteMatches = nil
	m.sessionPaletteCursor = 0
}

// recordSessionSwitch marks dir (a launchTarget.dir — a worktree path or a
// workspace session directory) as the most recently jumped to or resumed, so
// the ctrl+P switcher can order candidates most-recently-used first. An empty
// dir (nothing to key on) is ignored.
func (m *model) recordSessionSwitch(dir string) {
	if dir == "" {
		return
	}
	if m.switchOrder == nil {
		m.switchOrder = make(map[string]int)
	}
	m.switchSeq++
	m.switchOrder[dir] = m.switchSeq
}

// hasSwitchRecord reports whether dir has been jumped to or resumed this run.
func (m model) hasSwitchRecord(dir string) bool {
	_, ok := m.switchOrder[dir]
	return ok
}

// sessionPaletteLabel builds the fuzzy-match text for a worktree row in the
// session switcher: the repo's base name and the branch, space-separated. The
// repo path falls back to the worktree path when Repo is unset, matching the
// fallback used by the 'n'/'F'/'R' handlers.
func sessionPaletteLabel(row settings.Row) string {
	repo := row.Repo
	if repo == "" {
		repo = row.Worktree
	}
	label := filepath.Base(repo)
	if row.Branch != "" {
		label += " " + row.Branch
	}
	return label
}

// sessionCandidate is one entry the ctrl+P switcher can jump to: row is the
// render-only settings.Row shim renderPaletteRow (render.go) needs for its
// status glyph and branch styling; target is the neutral launch identity
// dispatched on enter; label is the fuzzy-match text.
type sessionCandidate struct {
	row    settings.Row
	target launchTarget
	label  string
}

// sessionSwitchCandidates collects every target the ctrl+P switcher offers:
// every Sessions-pane worktree row (m.workspaceRows) plus every assembled
// workspace session across m.wsStatuses. A workspace session still being
// assembled has Session.Dir == "" (the same pending-create discriminator
// stripPendingWsSessions documents, workspace_cmd.go) and is excluded,
// mirroring how a Sessions-pane creating row has no equivalent exclusion
// needed here (it is still keyed on a real, if not-yet-existing, worktree
// path).
func (m model) sessionSwitchCandidates() []sessionCandidate {
	out := make([]sessionCandidate, 0, len(m.workspaceRows))
	for _, row := range m.workspaceRows {
		out = append(out, sessionCandidate{
			row:    row,
			target: rowLaunchTarget(row),
			label:  sessionPaletteLabel(row),
		})
	}
	for _, ws := range m.wsStatuses {
		for _, sess := range ws.Sessions {
			if sess.Session.Dir == "" {
				continue
			}
			out = append(out, sessionCandidate{
				row: settings.Row{
					Worktree:  sess.Session.Dir,
					Branch:    sess.Session.Branch,
					Harness:   sess.Session.Harness,
					Provider:  sess.Provider,
					SessionID: sess.SessionID,
					State:     sess.State,
					Attention: sess.Attention,
				},
				target: wsSessionLaunchTarget(ws.Workspace.Name, sess),
				label:  ws.Workspace.Name + "/" + sess.Session.Name,
			})
		}
	}
	return out
}

// orderedSessionCandidates returns sessionSwitchCandidates ordered
// most-recently switched-to first (per switchOrder, keyed by each
// candidate's target directory), preserving the existing alphabetical order
// for candidates never switched to this run. startOnPrevious is true when
// both of the top two candidates have a recorded switch — i.e. a genuine
// "previous" session exists — so the palette cursor should start on row 1.
func (m model) orderedSessionCandidates() (candidates []sessionCandidate, startOnPrevious bool) {
	candidates = m.sessionSwitchCandidates()
	sort.SliceStable(candidates, func(i, j int) bool {
		si, oi := m.switchOrder[candidates[i].target.dir]
		sj, oj := m.switchOrder[candidates[j].target.dir]
		if oi != oj {
			return oi // switched candidates sort ahead of never-switched ones
		}
		if oi && oj {
			return si > sj // more recently switched first
		}
		return false // both unswitched: stable sort keeps alphabetical order
	})
	startOnPrevious = len(candidates) >= 2 &&
		m.hasSwitchRecord(candidates[0].target.dir) && m.hasSwitchRecord(candidates[1].target.dir)
	return candidates, startOnPrevious
}

// renderHarnessChooser renders the harness-selection list shown in the sessions
// pane while prompt == promptChooseHarness. The user moves the cursor with
// up/down and confirms with enter; esc cancels the whole new-worktree flow.
func (m model) renderHarnessChooser(width, height int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Choose harness") + "\n")
	if m.wsCreateTarget != "" {
		b.WriteString(dimStyle.Render(fmt.Sprintf("new session: %s / %s", m.wsCreateTarget, m.wsCreateSessionName)) + "\n")
	} else {
		b.WriteString(dimStyle.Render(fmt.Sprintf("new worktree: %s / %s", filepath.Base(m.newWorktreeRepo), m.newWorktreeBranch)) + "\n")
	}

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
	// Exclude workspace-owned session directories before visibleSessions runs:
	// they already have their own row in workspaceRows (settings.Merge applies
	// the same exclusion), so the live-only fallback and the header's
	// live/recent counts must agree rather than double-counting them. This is
	// the one call site that filters — visibleSessions itself is untouched.
	sessions := excludeWorkspaceOwnedSessions(m.snap.Sessions, m.workspaceRoot)
	rows, recentByInstance := visibleSessions(sessions, m.recentCollapsed, m.snap.UpdatedAt, cfg.InactiveHideAfter)
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

	headerHint := fmt.Sprintf("  %d live · %d recent (≤%dm)  ·  updated %s  ·  ? help",
		live, recent, recentMins, m.snap.UpdatedAt.Format("15:04:05"))
	header := titleStyle.Render("cogitator") + dimStyle.Render(headerHint)

	legend := legendLine()
	// The unreachable footer is gated behind --debug because transient
	// "instance unreachable" warnings (laptop sleep, network blips,
	// short-lived opencode processes) are noisy during normal operation
	// and don't require user action.
	var footer string
	if m.debug {
		footer = unreachableFooter(m.snap.UnreachableInstances)
	}

	_, sessionsInnerH := m.paneHeights()

	// The sessions pane is the only pane, so it always renders focused.
	sessionsStyle := paneFocusedStyle

	// When repos are configured, render the merged worktree view. Otherwise
	// fall back to the live-only path so --status/--demo and unconfigured
	// installs render exactly as before.
	var sessionContent string
	switch {
	case m.prompt == promptAddRepo:
		sessionContent = m.renderRepoFinder(paneW, sessionsInnerH)
	case m.prompt == promptSwitchSession:
		// Render whichever view (Sessions or Workspaces) is active as the
		// backdrop, then composite the floating switcher box centred over it
		// so the surrounding view stays visible.
		now := m.tickNow
		if now.IsZero() {
			now = time.Now()
		}
		var backdrop string
		if m.view == viewWorkspaces {
			backdrop = m.renderWorkspacesView(paneW, sessionsInnerH)
		} else {
			backdrop = m.renderWorkspaceRowsViewport(paneW, sessionsInnerH, m.workspaceRows, m.sessionCursor, now)
		}
		sessionContent = overlayBox(backdrop, paneW, sessionsInnerH, m.renderSessionPalette(paneW, sessionsInnerH))
	case m.prompt == promptHelp:
		// Render whichever view is active as the backdrop, then composite the
		// floating help box centred over it so the pane stays visible behind.
		now := m.tickNow
		if now.IsZero() {
			now = time.Now()
		}
		var backdrop string
		switch {
		case m.view == viewWorkspaces:
			backdrop = m.renderWorkspacesView(paneW, sessionsInnerH)
		case len(m.workspaceRows) > 0:
			backdrop = m.renderWorkspaceRowsViewport(paneW, sessionsInnerH, m.workspaceRows, m.sessionCursor, now)
		default:
			backdrop = m.renderAllSessions(paneW, rows, recentByInstance)
		}
		sessionContent = overlayBox(backdrop, paneW, sessionsInnerH, renderHelp(paneW))
	case m.prompt == promptSettings:
		// Render whichever view is active as the backdrop, then composite the
		// settings modal centred over it so the pane stays visible behind.
		now := m.tickNow
		if now.IsZero() {
			now = time.Now()
		}
		var backdrop string
		switch {
		case m.view == viewWorkspaces:
			backdrop = m.renderWorkspacesView(paneW, sessionsInnerH)
		case len(m.workspaceRows) > 0:
			backdrop = m.renderWorkspaceRowsViewport(paneW, sessionsInnerH, m.workspaceRows, m.sessionCursor, now)
		default:
			backdrop = m.renderAllSessions(paneW, rows, recentByInstance)
		}
		sessionContent = overlayBox(backdrop, paneW, sessionsInnerH, m.renderSettings(paneW))
	case m.prompt == promptChooseHarness:
		sessionContent = m.renderHarnessChooser(paneW, sessionsInnerH)
	case m.prompt == promptNewWorkspace:
		backdrop := m.renderWorkspacesView(paneW, sessionsInnerH)
		sessionContent = overlayBox(backdrop, paneW, sessionsInnerH, m.renderWsNamePrompt("New workspace", "workspace name: "))
	case m.prompt == promptNewWorkspaceSession:
		backdrop := m.renderWorkspacesView(paneW, sessionsInnerH)
		sessionContent = overlayBox(backdrop, paneW, sessionsInnerH, m.renderWsNamePrompt("New session", "session name: "))
	case wsDeletePromptActive(m.prompt):
		backdrop := m.renderWorkspacesView(paneW, sessionsInnerH)
		sessionContent = overlayBox(backdrop, paneW, sessionsInnerH, m.renderWsDeleteConfirm())
	case m.prompt == promptWorkspaceModal:
		backdrop := m.renderWorkspacesView(paneW, sessionsInnerH)
		sessionContent = overlayBox(backdrop, paneW, sessionsInnerH, m.renderWorkspaceModal(paneW, sessionsInnerH))
	case m.view == viewWorkspaces:
		sessionContent = m.renderWorkspacesView(paneW, sessionsInnerH)
	case len(m.workspaceRows) > 0:
		now := m.tickNow
		if now.IsZero() {
			now = time.Now()
		}
		sessionContent = m.renderWorkspaceRowsViewport(paneW, sessionsInnerH, m.workspaceRows, m.sessionCursor, now)
	default:
		sessionContent = m.renderAllSessions(paneW, rows, recentByInstance)
	}
	sessionsPane := sessionsStyle.Width(paneW).Height(sessionsInnerH).Render(sessionContent)

	parts := []string{header, sessionsPane, legend}
	// The Workspaces view's own renderer (workspace_view.go) has no pinned
	// footer line to grow into (unlike renderWorkspaceRowsViewport's tmuxHint),
	// so wsHint is appended here instead — below the pane, same as the debug
	// footer — whenever the Workspaces view is active and has something to say.
	if m.view == viewWorkspaces && m.wsHint != "" {
		parts = append(parts, wtHintStyle.Render(m.wsHint))
	}
	if footer != "" {
		parts = append(parts, footer)
	}
	return strings.Join(parts, "\n")
}

// newModel constructs the TUI model. debug enables diagnostic UI elements
// such as the unreachable-instance footer.
func newModel(snaps <-chan state.Snapshot, cfg *config.Config, bellEnabled, debug bool) model {
	if cfg == nil {
		cfg = config.Default()
	}

	ti := textinput.New()
	// Override AcceptSuggestion so it is never consumed by the suggestion
	// mechanism, since the sessions pane's various prompts set their own
	// placeholder and don't want it clobbered.
	ti.KeyMap.AcceptSuggestion = key.NewBinding(key.WithDisabled())
	// Width is intentionally left at zero here; it is recomputed in Update
	// on the first tea.WindowSizeMsg so it tracks the actual terminal width.

	return model{
		snaps:             snaps,
		recentCollapsed:   true,
		bellEnabled:       bellEnabled,
		debug:             debug,
		bellSent:          map[rowKey]state.Attention{},
		pendingDeletes:    map[string]settings.Row{},
		pendingCreates:    map[string]pendingCreate{},
		wsPendingSessions: map[string]pendingWsSession{},
		pulling:           map[string]bool{},
		cfg:               cfg,

		// Init always attempts an initial Workspaces-view load (skipped only
		// under --demo). Starting wsBuilding true means a snapshot arriving
		// before that load completes coalesces into wsDirty instead of
		// dispatching a second, concurrent load — see loadWorkspaceStatusCmd.
		wsBuilding: true,

		// Inject real implementations for tmux, git, and harness operations.
		// Tests can override these fields with fakes after construction. store
		// is wired separately by RunTUI (mirrors viewMarker/rosterUpserts).
		tmux:   realTmuxOps{},
		gitOp:  realGitOps{},
		harnOp: realHarnessOps{},

		prompt: promptIdle,
		input:  ti,
	}
}

// repoBoundary returns the session cursor index one repo group away from the
// current position: dir > 0 jumps to the first row of the next repo group,
// dir < 0 to the first row of the previous group (or the current group's first
// row when the cursor sits mid-group, matching vim's section motion). Rows are
// contiguous by Repo (settings.Merge groups them), so a group begins wherever
// Repo differs from the previous row. Returns the cursor unchanged when there
// is no group in that direction.
func (m model) repoBoundary(dir int) int {
	rows := m.workspaceRows
	if len(rows) == 0 {
		return 0
	}
	starts := []int{0}
	for i := 1; i < len(rows); i++ {
		if rows[i].Repo != rows[i-1].Repo {
			starts = append(starts, i)
		}
	}
	if dir > 0 {
		for _, s := range starts {
			if s > m.sessionCursor {
				return s
			}
		}
		return m.sessionCursor
	}
	prev := 0
	for _, s := range starts {
		if s >= m.sessionCursor {
			break
		}
		prev = s
	}
	return prev
}

// buildWorkspaceRowsCmd returns a tea.Cmd that runs buildWorkspaceRows in the
// background and delivers the result as a workspaceRowsMsg. snap and cfg are
// captured by value at dispatch time so the closure is not affected by later
// mutations to the model.
func buildWorkspaceRowsCmd(snap state.Snapshot, cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		rows, mode, root := buildWorkspaceRows(snap, cfg)
		return workspaceRowsMsg{rows: rows, launchMode: mode, root: root}
	}
}

// loadWorkspaceStatusCmd returns a tea.Cmd that loads the workspace set from
// store, joins it to the roster and the live session snapshot via
// workspace.MergeStatus, and delivers the result as a wsStatusMsg. snap is
// captured by value at dispatch time, matching buildWorkspaceRowsCmd. store
// may be nil (no workspace store wired, or --demo/tests) — the load is
// skipped and an empty result is returned rather than dispatching to a nil
// interface.
func loadWorkspaceStatusCmd(store storeOps, snap state.Snapshot) tea.Cmd {
	return func() tea.Msg {
		if store == nil {
			return wsStatusMsg{}
		}
		workspaces, err := store.LoadWorkspaces()
		if err != nil {
			// Non-fatal: render with an empty workspace set rather than
			// failing the whole load.
			workspaces = nil
		}
		roster, err := settings.Load()
		if err != nil {
			roster = map[string]settings.RosterEntry{}
		}
		// Pre-filter to top-level sessions only, matching buildWorkspaceRows'
		// contract for settings.Merge (MergeStatus documents the same
		// requirement).
		var liveTopLevel []state.SessionView
		for _, sv := range snap.Sessions {
			if !shouldHideSubagent(sv) && sv.ParentID == "" {
				liveTopLevel = append(liveTopLevel, sv)
			}
		}
		return wsStatusMsg{statuses: workspace.MergeStatus(workspaces, roster, liveTopLevel)}
	}
}

// buildWorkspaceRows loads workspace config, roster, git worktrees, and tmux
// window dirs, then calls settings.Merge to produce the merged row list. It
// is called on every snapshot update so the list stays in sync with live
// session changes.
//
// tmuxDirs is gathered from tmuxctl.ListCogDirs() when tmux is available.
// When tmux is unavailable or the call fails, an empty map is used (safe
// fallback: unknown rows render as stopped instead of unknown).
//
// It also returns the resolved tmux launch mode from workspace config so the
// caller can keep its launch behaviour in sync with config edits, and the
// resolved workspace root so the caller can exclude workspace-owned session
// directories from the live-only fallback path.
//
// Returns nil rows when no repos are configured (zero-value safe for callers);
// the launch mode and workspace root are still resolved in that case —
// the fallback view (which renders exactly when repos are empty) needs the
// root to exclude workspace-owned sessions from its own listing.
func buildWorkspaceRows(snap state.Snapshot, cfg *config.Config) ([]settings.Row, tmuxctl.LaunchMode, string) {
	wsCfg, err := settings.LoadConfig()
	if err != nil {
		return nil, tmuxctl.ModeWindow, ""
	}
	mode := launchModeFor(wsCfg.LaunchMode)
	root, err := settings.ResolveWorkspaceRoot(wsCfg)
	if err != nil {
		// Unresolvable root (e.g. nested inside a git working tree) — disable
		// the exclusion rather than failing the whole row build.
		root = ""
	}
	if len(wsCfg.Repos) == 0 {
		// No repos configured — live-only path.
		return nil, mode, root
	}

	// Display repos alphabetically by name. Sort the in-memory copy only;
	// config.json keeps its insertion order on disk.
	sort.SliceStable(wsCfg.Repos, func(i, j int) bool {
		return strings.ToLower(filepath.Base(wsCfg.Repos[i].Path)) <
			strings.ToLower(filepath.Base(wsCfg.Repos[j].Path))
	})

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

	roster, err := settings.Load()
	if err != nil {
		// Non-fatal: proceed with an empty roster.
		roster = map[string]settings.RosterEntry{}
	}

	// Pre-filter to top-level sessions only (shouldHideSubagent is private to
	// the ui package; settings.Merge trusts the caller to do this filtering).
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

	return settings.Merge(wsCfg.Repos, worktreesByRepo, roster, liveTopLevel, tmuxDirs, root), mode, root
}

// excludeWorkspaceOwnedSessions drops sessions whose Directory lies at or
// below workspaceRoot from sessions. It is View's counterpart to
// settings.Merge's own exclusion (settings.filterWorkspaceOwnedWorktrees):
// without it, a workspace session's per-repo worktree would render twice —
// once as its workspaceRows entry, once via the live-only fallback path and
// the header's live/recent counts, both of which are built from this slice.
//
// An empty workspaceRoot is a no-op (returns sessions unchanged), matching
// settings.PathUnderRoot's own empty-root behaviour, so --demo/--status and
// any install that has not yet resolved a root render exactly as before this
// exclusion existed. processBellTransitions deliberately runs on the raw,
// unfiltered m.snap.Sessions rather than through this function — a workspace
// session that needs attention should still ring the bell.
func excludeWorkspaceOwnedSessions(sessions []state.SessionView, workspaceRoot string) []state.SessionView {
	if workspaceRoot == "" || len(sessions) == 0 {
		return sessions
	}
	root, err := pathnorm.Canonical(workspaceRoot)
	if err != nil {
		root = workspaceRoot
	}
	out := make([]state.SessionView, 0, len(sessions))
	for _, sv := range sessions {
		if sv.Directory != "" {
			dir, err := pathnorm.Canonical(sv.Directory)
			if err != nil {
				dir = sv.Directory
			}
			if settings.PathUnderRoot(root, dir) {
				continue
			}
		}
		out = append(out, sv)
	}
	return out
}

// wsSessionUnderCursor returns the WorkspaceStatus and SessionStatus the
// Workspaces-view cursor currently targets, and false when the cursor sits on
// a workspace header line, an empty-workspace hint line, or there are no
// workspaces at all. It mirrors wsUnderCursor (workspace_cmd.go) but resolves
// all the way to the session row, which that helper does not need for its own
// (workspace-only) callers.
func (m model) wsSessionUnderCursor() (workspace.WorkspaceStatus, workspace.SessionStatus, bool) {
	for _, dl := range wsDisplayLines(m.wsStatuses) {
		if dl.entry != m.wsCursor {
			continue
		}
		if dl.kind != wsLineSession {
			return workspace.WorkspaceStatus{}, workspace.SessionStatus{}, false
		}
		ws := m.wsStatuses[dl.wsIndex]
		return ws, ws.Sessions[dl.sessIndex], true
	}
	return workspace.WorkspaceStatus{}, workspace.SessionStatus{}, false
}

// updateWorkspaceLaunch handles 'enter' on a workspace session row in the
// Workspaces view: it launches (or jumps/resumes) exactly like the Sessions
// pane's own 'enter' (same tmux guards, same StateMissing/StateCreating
// guards), but keyed on the workspace session's own directory and named
// "<workspace>/<session>" via wsSessionLaunchTarget. Defined here rather than
// in workspace_view.go/workspace_cmd.go, both untouched by this feature, per
// the phase convention that every workspace mode routes its key handling
// through a dedicated method. Returns handled=false for any other key, or
// when the cursor is not on a session row, so the caller falls through to
// updateWorkspaceLifecycle and then updateWorkspaceView.
func (m model) updateWorkspaceLaunch(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if msg.String() != "enter" {
		return m, nil, false
	}
	ws, sess, ok := m.wsSessionUnderCursor()
	if !ok {
		return m, nil, false
	}

	tmuxAvail := m.tmux != nil && m.tmux.Available()
	if !tmuxAvail {
		m.wsHint = "tmux not available — start cogitator inside a tmux session to use jump/resume"
		return m, nil, true
	}
	if sess.State == settings.StateMissing {
		m.wsHint = "session directory is missing — cannot resume"
		return m, nil, true
	}
	if sess.State == settings.StateCreating {
		m.wsHint = "session is still being created…"
		return m, nil, true
	}

	target := wsSessionLaunchTarget(ws.Workspace.Name, sess)
	m.recordSessionSwitch(target.dir)
	return m, launchCmd(m.tmux, target, m.harnOp, m.launchMode, resolvedDefaultHarness(m.harnOp)), true
}

// resolvedDefaultHarness returns the configured default harness kind when one
// is set AND currently registered in harnOp; otherwise "". A configured but
// unregistered default (e.g. removed or renamed) resolves to "", so callers
// fall back to the per-launch "always ask" behaviour.
func resolvedDefaultHarness(harnOp harnessOps) string {
	wsCfg, err := settings.LoadConfig()
	if err != nil || wsCfg.DefaultHarness == "" || harnOp == nil {
		return ""
	}
	if _, err := harnOp.Get(harness.Kind(wsCfg.DefaultHarness)); err != nil {
		return ""
	}
	return wsCfg.DefaultHarness
}

// startNewWorktree resets the new-worktree prompt state, inserts an optimistic
// spinner row, and dispatches newWorktreeCmd for branch under repoPath with the
// given harness. Shared by the chooser-confirm path and the default-harness
// skip-the-chooser path.
func (m model) startNewWorktree(repoPath, branch, harnessKind string, fromRemote bool) (model, tea.Cmd) {
	m.prompt = promptIdle
	m.newWorktreeRepo = ""
	m.newWorktreeBranch = ""
	m.worktreeFromRemote = false
	m.harnessChooserKinds = nil
	m.harnessChooserCursor = 0
	launchMode := m.launchMode
	if wsCfg, err := settings.LoadConfig(); err == nil {
		launchMode = launchModeFor(wsCfg.LaunchMode)
	}
	// Optimistic spinner row for the duration of the create/fetch.
	m.addPendingCreate(repoPath, worktreeDest(repoPath, branch), branch, fromRemote)
	m.workspaceRows = injectPendingCreates(m.workspaceRows, m.pendingCreates)
	var spinnerC tea.Cmd
	if !m.spinnerActive {
		m.spinnerActive = true
		spinnerC = spinnerTickCmd()
	}
	actionCmd := newWorktreeCmd(m.tmux, m.gitOp, m.harnOp, repoPath, branch, harnessKind, launchMode, fromRemote)
	return m, tea.Batch(actionCmd, spinnerC)
}

// settingsRowCount is the number of rows in the settings modal.
const settingsRowCount = 2

// settingsHarnessOptions returns the harness choices for the settings modal: an
// "always ask" sentinel ("") followed by the registered kinds (sorted). The
// sentinel clears the persisted default.
func settingsHarnessOptions(harnOp harnessOps) []string {
	opts := []string{""}
	for _, k := range harnessChooserKinds(harnOp) {
		opts = append(opts, string(k))
	}
	return opts
}

// normalizeSettingsLaunchMode maps the unset launch mode to its effective
// default (session) so the modal always displays a concrete value.
func normalizeSettingsLaunchMode(mode settings.LaunchMode) settings.LaunchMode {
	if mode == settings.LaunchWindow {
		return settings.LaunchWindow
	}
	return settings.LaunchSession
}

// openSettings snapshots the persisted config into the modal's working copy and
// opens the settings overlay.
func (m *model) openSettings() {
	wsCfg, _ := settings.LoadConfig()
	m.settingsDefaultHarness = wsCfg.DefaultHarness
	m.settingsLaunchMode = normalizeSettingsLaunchMode(wsCfg.LaunchMode)
	m.settingsCursor = 0
	m.settingsErr = ""
	m.prompt = promptSettings
}

// updateSettings handles key input while the settings modal is open: up/down
// move between rows, left/right (and enter/space) cycle the highlighted
// setting's value, and esc/q/S close the modal.
func (m model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "S":
		m.prompt = promptIdle
	case "up", "k", "ctrl+p":
		m.settingsCursor = clampIndex(m.settingsCursor-1, settingsRowCount)
	case "down", "j", "ctrl+n":
		m.settingsCursor = clampIndex(m.settingsCursor+1, settingsRowCount)
	case "left", "h":
		m.cycleSetting(-1)
	case "right", "l", "enter", " ":
		m.cycleSetting(1)
	}
	return m, nil
}

// cycleSetting advances the highlighted setting by delta (wrapping) and writes
// the change to config.json immediately, recording any save error for display.
func (m *model) cycleSetting(delta int) {
	switch m.settingsCursor {
	case 0:
		opts := settingsHarnessOptions(m.harnOp)
		i := indexOfString(opts, m.settingsDefaultHarness)
		m.settingsDefaultHarness = opts[wrapIndex(i+delta, len(opts))]
		m.recordSettingsErr(settings.SetDefaultHarness(m.settingsDefaultHarness))
	case 1:
		// Only two launch modes, so ±1 always toggles.
		if m.settingsLaunchMode == settings.LaunchWindow {
			m.settingsLaunchMode = settings.LaunchSession
		} else {
			m.settingsLaunchMode = settings.LaunchWindow
		}
		if err := settings.SetLaunchMode(m.settingsLaunchMode); err != nil {
			m.recordSettingsErr(err)
		} else {
			m.recordSettingsErr(nil)
			m.launchMode = launchModeFor(m.settingsLaunchMode)
		}
	}
}

func (m *model) recordSettingsErr(err error) {
	if err != nil {
		m.settingsErr = fmt.Sprintf("save failed: %v", err)
	} else {
		m.settingsErr = ""
	}
}

// indexOfString returns the index of want in ss, or 0 when absent.
func indexOfString(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return 0
}

// wrapIndex returns i wrapped into [0,n); n<=0 yields 0. Used for cyclable
// settings values.
func wrapIndex(i, n int) int {
	if n <= 0 {
		return 0
	}
	return ((i % n) + n) % n
}
