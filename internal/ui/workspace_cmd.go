package ui

// workspace_cmd.go — creation half of the Workspaces view: 'N' (new, empty
// workspace) and 'n' (new session, checking out every member repo of the
// workspace under the cursor on one new branch). Kept separate from
// workspace_view.go (pure navigation/rendering, untouched by this feature) per
// the phase convention that every workspace mode routes its key handling
// through its own method rather than adding arms to Update.
//
// Naming note: pendingWsSession/injectPendingWsSessions/stripPendingWsSessions
// mirror pendingCreate/injectPendingCreates/filterPendingDeletes (model.go) —
// the same "optimistic placeholder row while a slow Cmd runs" pattern, applied
// to workspace.WorkspaceStatus instead of settings.Row.

import (
	"fmt"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// pendingWsSession is an in-flight workspace-session assembly ('n' in the
// Workspaces view), shown as an optimistic, animated placeholder session row
// until assembleWorkspaceSessionCmd reports completion.
type pendingWsSession struct {
	workspace string
	session   string
}

// wsSessionKey identifies a pending workspace-session create by workspace and
// session name — the stable correlation key between dispatch and the
// wsSessionAssembledMsg that clears it, mirroring createKey for the Sessions
// pane's pending creates.
func wsSessionKey(workspaceName, sessionName string) string {
	return workspaceName + "\x00" + sessionName
}

// stripPendingWsSessions drops every placeholder session from statuses. A
// placeholder is identified by Session.Dir == "": every real, persisted
// session always has a non-empty Dir (set by workspace.AssembleSession), so
// this is an unambiguous discriminator that needs no side-band bookkeeping.
// Callers re-inject the current set of pending sessions afterwards
// (injectPendingWsSessions) rather than leaving statuses stripped.
func stripPendingWsSessions(statuses []workspace.WorkspaceStatus) []workspace.WorkspaceStatus {
	if len(statuses) == 0 {
		return statuses
	}
	out := make([]workspace.WorkspaceStatus, len(statuses))
	for i, ws := range statuses {
		sessions := ws.Sessions[:0:0]
		for _, s := range ws.Sessions {
			if s.Session.Dir == "" {
				continue
			}
			sessions = append(sessions, s)
		}
		out[i] = workspace.WorkspaceStatus{Workspace: ws.Workspace, Sessions: sessions}
	}
	return out
}

// injectPendingWsSessions appends a placeholder session row for every pending
// create not already present (by session name) in its workspace. frame
// selects the current spinnerFrames glyph, which is baked directly into the
// placeholder's Session.Branch text: formatWsSessionRow (workspace_view.go) is
// a pure function of workspace.SessionStatus with no access to model state,
// unlike the Sessions pane's formatCreatingRow, so the animation must live in
// the data rather than be read live at render time. Placeholders are injected
// in a stable, sorted order to avoid frame-to-frame jitter when several
// creates run at once. statuses is never mutated in place.
func injectPendingWsSessions(statuses []workspace.WorkspaceStatus, pending map[string]pendingWsSession, frame int) []workspace.WorkspaceStatus {
	if len(pending) == 0 {
		return statuses
	}
	keys := make([]string, 0, len(pending))
	for k := range pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]workspace.WorkspaceStatus, len(statuses))
	copy(out, statuses)
	glyph := spinnerFrames[frame%len(spinnerFrames)]

	for _, k := range keys {
		p := pending[k]
		for i := range out {
			if out[i].Workspace.Name != p.workspace {
				continue
			}
			present := false
			for _, s := range out[i].Sessions {
				if s.Session.Name == p.session {
					present = true
					break
				}
			}
			if present {
				break
			}
			placeholder := workspace.SessionStatus{
				Session: workspace.Session{
					Name:   p.session,
					Branch: fmt.Sprintf("%s creating %s…", glyph, p.session),
				},
				State: settings.StateCreating,
			}
			sessions := append(out[i].Sessions[:len(out[i].Sessions):len(out[i].Sessions)], placeholder)
			out[i] = workspace.WorkspaceStatus{Workspace: out[i].Workspace, Sessions: sessions}
			break
		}
	}
	return out
}

// addPendingWsSession records an in-flight workspace-session create so
// injectPendingWsSessions can render its placeholder row.
func (m *model) addPendingWsSession(workspaceName, sessionName string) {
	if m.wsPendingSessions == nil {
		m.wsPendingSessions = map[string]pendingWsSession{}
	}
	m.wsPendingSessions[wsSessionKey(workspaceName, sessionName)] = pendingWsSession{
		workspace: workspaceName, session: sessionName,
	}
}

// clearPendingWsSession removes the in-flight create for workspace+session
// and drops its placeholder row from m.wsStatuses immediately (rather than
// leaving it animating until the next reload), mirroring clearPendingCreate
// for the Sessions pane.
func (m *model) clearPendingWsSession(workspaceName, sessionName string) {
	delete(m.wsPendingSessions, wsSessionKey(workspaceName, sessionName))
	m.wsStatuses = injectPendingWsSessions(stripPendingWsSessions(m.wsStatuses), m.wsPendingSessions, m.spinnerFrame)
	m.clampWsCursor()
}

// clampWsCursor keeps wsCursor within [0, wsEntryCount) after m.wsStatuses'
// entry count changes (a reload, or a placeholder row appearing/disappearing).
func (m *model) clampWsCursor() {
	if n := wsEntryCount(m.wsStatuses); n == 0 {
		m.wsCursor = 0
	} else if m.wsCursor >= n {
		m.wsCursor = n - 1
	}
}

// wsUnderCursor returns the Workspace the Workspaces-view cursor currently
// targets — whether it sits on the workspace's header line or one of its
// session rows, per wsDisplayLine's documented contract — and false when
// there are no workspaces at all.
func (m model) wsUnderCursor() (workspace.Workspace, bool) {
	for _, dl := range wsDisplayLines(m.wsStatuses) {
		if dl.entry == m.wsCursor {
			return m.wsStatuses[dl.wsIndex].Workspace, true
		}
	}
	return workspace.Workspace{}, false
}

// validateNewWsSessionName pre-flight-checks a session name typed for
// workspaceName before the (slow) assemble Cmd is even dispatched: it must
// slugify to a legal branch name (workspace.SessionBranch, then
// ValidBranchShape as the pure, no-shell-out defensive check that function's
// own doc comment calls out for exactly this use), and it must not collide
// with a session already in that workspace. Catching both here means an
// illegal or duplicate name is refused at the prompt, not discovered only
// after a session directory and worktrees have already been created and
// rolled back.
func (m model) validateNewWsSessionName(workspaceName, name string) error {
	branch, err := workspace.SessionBranch(name)
	if err != nil {
		return err
	}
	if err := workspace.ValidBranchShape(branch); err != nil {
		return err
	}
	for _, ws := range m.wsStatuses {
		if ws.Workspace.Name != workspaceName {
			continue
		}
		for _, sess := range ws.Workspace.Sessions {
			if sess.Name == name {
				return fmt.Errorf("session %q already exists in workspace %q", name, workspaceName)
			}
		}
	}
	return nil
}

// updateWorkspaceLifecycle handles the Workspaces view's creation keys — 'N'
// (new, empty workspace) and 'n' (new session in the workspace under the
// cursor) — returning handled=false for any other key so the caller falls
// through to updateWorkspaceView's pure navigation (workspace_view.go, left
// untouched by this feature).
func (m model) updateWorkspaceLifecycle(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	switch msg.String() {
	case "N":
		next, cmd := m.openNewWorkspacePrompt()
		return next, cmd, true
	case "n":
		next, cmd := m.openNewWorkspaceSessionPrompt()
		return next, cmd, true
	}
	return m, nil, false
}

// openNewWorkspacePrompt opens the name prompt for 'N'.
func (m model) openNewWorkspacePrompt() (model, tea.Cmd) {
	m.prompt = promptNewWorkspace
	m.wsHint = ""
	m.input.Placeholder = "workspace name"
	m.input.SetValue("")
	return m, m.input.Focus()
}

// openNewWorkspaceSessionPrompt opens the session-name prompt for 'n', for
// the workspace under the cursor. A workspace with no member repos cannot
// host a session, so 'n' reports that instead of opening the prompt; likewise
// when there is no workspace under the cursor at all (an empty list).
func (m model) openNewWorkspaceSessionPrompt() (model, tea.Cmd) {
	ws, ok := m.wsUnderCursor()
	if !ok {
		return m, nil
	}
	if len(ws.Members) == 0 {
		m.wsHint = fmt.Sprintf("workspace %q has no member repos — add one before creating a session", ws.Name)
		return m, nil
	}
	m.wsCreateTarget = ws.Name
	m.wsHint = ""
	m.prompt = promptNewWorkspaceSession
	m.input.Placeholder = "session name"
	m.input.SetValue("")
	return m, m.input.Focus()
}

// startWorkspaceSessionCreate resets the new-session prompt state, inserts an
// optimistic placeholder row, and dispatches assembleWorkspaceSessionCmd for
// sessionName in workspaceName with the given harness. Mirrors
// startNewWorktree's role for the Sessions pane's 'n'/'F' flow.
func (m model) startWorkspaceSessionCreate(workspaceName, sessionName, harnessKind string) (model, tea.Cmd) {
	m.prompt = promptIdle
	m.wsCreateTarget = ""
	m.wsCreateSessionName = ""
	m.harnessChooserKinds = nil
	m.harnessChooserCursor = 0

	m.addPendingWsSession(workspaceName, sessionName)
	m.wsStatuses = injectPendingWsSessions(stripPendingWsSessions(m.wsStatuses), m.wsPendingSessions, m.spinnerFrame)
	m.clampWsCursor()

	var spinnerC tea.Cmd
	if !m.spinnerActive {
		m.spinnerActive = true
		spinnerC = spinnerTickCmd()
	}
	actionCmd := assembleWorkspaceSessionCmd(m.store, workspaceName, sessionName, harnessKind)
	return m, tea.Batch(actionCmd, spinnerC)
}

// renderWsNamePrompt renders the floating name-entry box shown while
// promptNewWorkspace ('N') or promptNewWorkspaceSession ('n') is active. It
// mirrors the Sessions pane's inline branch-name prompt (worktreePromptLine)
// but is composited as a centered overlay (via overlayBox, render.go) rather
// than a pinned footer line, since the Workspaces view's own renderer
// (workspace_view.go) has no footer budget to grow into.
func (m model) renderWsNamePrompt(title, label string) string {
	return paletteBoxStyle.Render(
		headerStyle.Render(title) + "\n" + wtHintStyle.Render(label) + m.input.View(),
	)
}

// wsWorkspaceCreatedMsg is returned by createWorkspaceCmd after 'N' completes
// (or fails). name is the workspace name, stamped on every result (including
// errors) so the handler can phrase its hint.
type wsWorkspaceCreatedMsg struct {
	name string
	err  error
}

// createWorkspaceCmd creates an empty workspace named name via store and
// reports the outcome as a wsWorkspaceCreatedMsg. store may be nil (no
// workspace store wired, e.g. --demo or a zero-value model in tests).
func createWorkspaceCmd(store storeOps, name string) tea.Cmd {
	return func() tea.Msg {
		if store == nil {
			return wsWorkspaceCreatedMsg{name: name, err: fmt.Errorf("workspace store is not available")}
		}
		if _, err := store.AddWorkspace(name); err != nil {
			return wsWorkspaceCreatedMsg{name: name, err: err}
		}
		return wsWorkspaceCreatedMsg{name: name}
	}
}

// wsSessionAssembledMsg is returned by assembleWorkspaceSessionCmd after a
// workspace session's worktrees have been assembled and persisted (or the
// attempt failed). workspaceName/sessionName are stamped on every result
// (including errors) so the handler can clear the matching pending placeholder
// and phrase its hint.
type wsSessionAssembledMsg struct {
	workspaceName string
	sessionName   string
	session       workspace.Session
	err           error
}

// assembleWorkspaceSessionCmd loads workspaceName fresh from store, assembles
// a new session inside it via workspace.AssembleSession — one git worktree
// per member repo, all on one new branch — and on success persists the
// session via store.AddSession. AssembleSession is pre-flight-then-commit and
// rolls back its own partial work on failure, so a failure there alone leaves
// nothing behind; but if AddSession itself then fails (e.g. a same-named
// session was added by another writer in the interim), the just-assembled
// worktrees would otherwise be left on disk with no matching store entry, so
// that case is torn down explicitly (workspace.TeardownSession, best-effort)
// before the error is returned. This is the single tea.Cmd boundary for the
// whole create: no git or store access happens on the UI goroutine.
func assembleWorkspaceSessionCmd(store storeOps, workspaceName, sessionName, harnessKind string) tea.Cmd {
	return func() tea.Msg {
		res := wsSessionAssembledMsg{workspaceName: workspaceName, sessionName: sessionName}
		if store == nil {
			res.err = fmt.Errorf("workspace store is not available")
			return res
		}

		workspaces, err := store.LoadWorkspaces()
		if err != nil {
			res.err = err
			return res
		}
		ws, ok := findWorkspaceByName(workspaces, workspaceName)
		if !ok {
			res.err = fmt.Errorf("workspace %q does not exist", workspaceName)
			return res
		}

		wsCfg, err := settings.LoadConfig()
		if err != nil {
			res.err = err
			return res
		}
		root, err := settings.ResolveWorkspaceRoot(wsCfg)
		if err != nil {
			res.err = err
			return res
		}

		session, err := workspace.AssembleSession(ws, root, sessionName, harnessKind)
		if err != nil {
			res.err = err
			return res
		}
		if err := store.AddSession(workspaceName, session); err != nil {
			_ = workspace.TeardownSession(session)
			res.err = err
			return res
		}
		res.session = session
		return res
	}
}

// findWorkspaceByName returns the workspace named name from workspaces, and
// whether one was found.
func findWorkspaceByName(workspaces []workspace.Workspace, name string) (workspace.Workspace, bool) {
	for _, ws := range workspaces {
		if ws.Name == name {
			return ws, true
		}
	}
	return workspace.Workspace{}, false
}
