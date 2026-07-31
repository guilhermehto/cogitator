package ui

// workspace_delete.go — deletion half of the Workspaces view: 'D' on a
// session row opens a two-step confirm that tears down one session (every
// member worktree/branch, the session directory, and its tmux target); 'D'
// on a workspace's header or empty-sessions hint row opens the same two-step
// confirm for every session in the workspace, removing the workspace itself
// only once all of them succeeded. Kept separate from workspace_view.go
// (pure navigation/rendering) and workspace_cmd.go (creation), per the phase
// convention that every workspace mode routes its key handling through its
// own method rather than adding arms to Update.
//
// The existing single-worktree delete (promptConfirmDeleteWorktree[2],
// model.go) keeps one branch's merge status in a single string
// (deleteMergeInfo). A session bundle has one member repo per SessionMember,
// so this file's confirm renders one merge-status line per member
// (wsDeleteMembers/wsDeleteMergeInfo) instead, probed concurrently
// (wsMergeStatusCmd, one mergeStatusCmd per member) so a slow repo never
// blocks the confirm from appearing — unfinished rows render "checking…"
// until their probe returns.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// wsDeleteMember is one member repo's row in an active delete confirmation:
// the session it belongs to (rendered only when the whole workspace, and
// therefore possibly several sessions, is the target), the member repo, its
// worktree inside the session directory, and the branch checked out there.
// Captured once when 'D' opens the flow (wsDeleteMembersFor) so the confirm
// dialog and the eventual teardown act on the exact snapshot the user saw,
// regardless of store changes in between.
type wsDeleteMember struct {
	session      string
	repoPath     string
	worktreePath string
	branch       string
}

// wsDeleteMembersFor flattens every member of every session in sessions into
// the delete confirmation's row list, in session-then-member order.
func wsDeleteMembersFor(sessions []workspace.Session) []wsDeleteMember {
	var out []wsDeleteMember
	for _, sess := range sessions {
		for _, mem := range sess.Members {
			out = append(out, wsDeleteMember{
				session:      sess.Name,
				repoPath:     mem.RepoPath,
				worktreePath: mem.WorktreePath,
				branch:       sess.Branch,
			})
		}
	}
	return out
}

// wsDeletePromptActive reports whether p is one of the four workspace/session
// delete confirmation modes, used both to guard the shared mergeStatusMsg
// handler (model.go) and to select the confirm's overlay in View.
func wsDeletePromptActive(p promptMode) bool {
	switch p {
	case promptConfirmDeleteWsSession, promptConfirmDeleteWsSession2,
		promptConfirmDeleteWorkspace, promptConfirmDeleteWorkspace2:
		return true
	default:
		return false
	}
}

// clearWsDeleteTarget resets the prompt to idle and clears every field of the
// in-progress workspace/session delete confirmation. Called on cancel and
// once the delete Cmd has been dispatched, mirroring how deleteTarget/
// deleteMergeInfo are cleared for the single-worktree flow.
func (m *model) clearWsDeleteTarget() {
	m.prompt = promptIdle
	m.wsDeleteWorkspace = ""
	m.wsDeleteSession = ""
	m.wsDeleteMembers = nil
	m.wsDeleteMergeInfo = nil
}

// updateWorkspaceDelete handles 'D' in the Workspaces view: it opens the
// first of two confirmations for whichever the cursor currently targets — a
// single session (wsSessionUnderCursor) or, when the cursor sits on a
// workspace's header or empty-sessions hint line instead, the whole
// workspace (wsUnderCursor) — and kicks off the async per-member
// merge-status probes that annotate it. Defined here rather than in
// workspace_view.go/workspace_cmd.go, both untouched by this feature, per the
// phase convention that every workspace mode routes its key handling through
// a dedicated method. Returns handled=false for any other key, or when there
// is nothing under the cursor to delete (no workspaces at all), so the caller
// falls through to updateWorkspaceLaunch and then updateWorkspaceView.
func (m model) updateWorkspaceDelete(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if msg.String() != "D" {
		return m, nil, false
	}
	if ws, sess, ok := m.wsSessionUnderCursor(); ok {
		return m.openDeleteWsSessionConfirm(ws.Workspace.Name, sess.Session)
	}
	if ws, ok := m.wsUnderCursor(); ok {
		return m.openDeleteWorkspaceConfirm(ws)
	}
	return m, nil, false
}

// openDeleteWsSessionConfirm captures sess's members, opens the first
// confirmation, and dispatches the concurrent per-member merge-status probes.
func (m model) openDeleteWsSessionConfirm(workspaceName string, sess workspace.Session) (model, tea.Cmd, bool) {
	m.wsDeleteWorkspace = workspaceName
	m.wsDeleteSession = sess.Name
	m.wsDeleteMembers = wsDeleteMembersFor([]workspace.Session{sess})
	m.wsDeleteMergeInfo = map[string]string{}
	m.prompt = promptConfirmDeleteWsSession
	return m, wsMergeStatusCmd(m.gitOp, m.wsDeleteMembers), true
}

// openDeleteWorkspaceConfirm captures every member of every session in ws,
// opens the first confirmation, and dispatches the concurrent per-member
// merge-status probes across all of them.
func (m model) openDeleteWorkspaceConfirm(ws workspace.Workspace) (model, tea.Cmd, bool) {
	m.wsDeleteWorkspace = ws.Name
	m.wsDeleteSession = ""
	m.wsDeleteMembers = wsDeleteMembersFor(ws.Sessions)
	m.wsDeleteMergeInfo = map[string]string{}
	m.prompt = promptConfirmDeleteWorkspace
	return m, wsMergeStatusCmd(m.gitOp, m.wsDeleteMembers), true
}

// wsMergeStatusCmd probes every member's branch merge status concurrently —
// one mergeStatusCmd (model.go) per member, batched — so a slow repo never
// blocks the others; the confirm dialog renders "checking…" for whichever
// rows have not returned yet. Each probe is tagged with the member's own
// worktree path, which mergeStatusMsg's handler (model.go) uses to place the
// result in m.wsDeleteMergeInfo. Returns nil for an empty member list (an
// already-empty session or workspace), the correct no-op Cmd.
func wsMergeStatusCmd(gitOp gitOps, members []wsDeleteMember) tea.Cmd {
	if len(members) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, len(members))
	for i, mem := range members {
		cmds[i] = mergeStatusCmd(gitOp, mem.repoPath, mem.branch, mem.worktreePath)
	}
	return tea.Batch(cmds...)
}

// killTmuxTargetForDir best-effort closes whatever tmux window or session is
// attached to dir, honouring mode exactly as launchInner's selectTarget does.
// Shared by deleteWorktreeCmd (model.go, a single worktree) and this file's
// deleteWsSessionCmd/deleteWorkspaceCmd (a whole session directory) so the
// "find the target, kill session or window depending on mode" logic lives in
// one place rather than being duplicated per caller.
func killTmuxTargetForDir(ops tmuxOps, dir string, mode tmuxctl.LaunchMode) {
	if ops == nil || !ops.Available() {
		return
	}
	target, err := ops.FindWindowByDir(dir)
	if err != nil {
		return
	}
	if mode == tmuxctl.ModeSession {
		_ = ops.KillSession(target)
	} else {
		_ = ops.KillWindow(target)
	}
}

// findSessionByName returns the session named name from sessions, and
// whether one was found.
func findSessionByName(sessions []workspace.Session, name string) (workspace.Session, bool) {
	for _, s := range sessions {
		if s.Name == name {
			return s, true
		}
	}
	return workspace.Session{}, false
}

// wsSessionDeletedMsg is returned by deleteWsSessionCmd after a workspace
// session's worktrees/branches/directory have been torn down (best-effort)
// and, only on full success, the session dropped from the store.
// workspaceName/sessionName are stamped on every result (including errors) so
// the handler can phrase its hint.
type wsSessionDeletedMsg struct {
	workspaceName string
	sessionName   string
	err           error
}

// deleteWsSessionCmd loads workspaceName fresh from store, tears down
// sessionName via workspace.TeardownSession (best-effort: collects per-repo
// failures rather than reporting overall success), then closes its tmux
// target exactly as deleteWorktreeCmd does for a single worktree — attempted
// regardless of the teardown outcome, since even a partial teardown leaves
// the session directory holed and an agent still pointed at it is worse than
// a dead pane. The session is dropped from the store via RemoveSession only
// when TeardownSession reported no failures: on a failure, the store keeps
// the session (whatever state its members are actually in) so the error can
// name the repo and the user can retry or investigate.
func deleteWsSessionCmd(store storeOps, ops tmuxOps, workspaceName, sessionName string, mode tmuxctl.LaunchMode) tea.Cmd {
	return func() tea.Msg {
		res := wsSessionDeletedMsg{workspaceName: workspaceName, sessionName: sessionName}
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
		sess, ok := findSessionByName(ws.Sessions, sessionName)
		if !ok {
			res.err = fmt.Errorf("session %q does not exist in workspace %q", sessionName, workspaceName)
			return res
		}

		teardownErr := workspace.TeardownSession(sess)
		killTmuxTargetForDir(ops, sess.Dir, mode)
		if teardownErr != nil {
			res.err = teardownErr
			return res
		}
		if err := store.RemoveSession(workspaceName, sessionName); err != nil {
			res.err = err
			return res
		}
		return res
	}
}

// wsWorkspaceDeletedMsg is returned by deleteWorkspaceCmd after every session
// in workspaceName has been torn down and, only if every one of them
// succeeded, the workspace itself removed from the store.
type wsWorkspaceDeletedMsg struct {
	workspaceName string
	err           error
}

// deleteWorkspaceCmd loads workspaceName fresh from store and tears down
// every one of its sessions exactly as deleteWsSessionCmd does for one
// session (TeardownSession, then killTmuxTargetForDir) — but the workspace
// itself is removed from the store only when every session's teardown
// succeeded; a single session's failure leaves the whole workspace (and
// every session in it, torn down or not) in the store, and the returned
// error names the failing session and repo so the user can retry.
func deleteWorkspaceCmd(store storeOps, ops tmuxOps, workspaceName string, mode tmuxctl.LaunchMode) tea.Cmd {
	return func() tea.Msg {
		res := wsWorkspaceDeletedMsg{workspaceName: workspaceName}
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

		var errs []error
		for _, sess := range ws.Sessions {
			if err := workspace.TeardownSession(sess); err != nil {
				errs = append(errs, fmt.Errorf("session %q: %w", sess.Name, err))
			}
			killTmuxTargetForDir(ops, sess.Dir, mode)
		}
		if len(errs) > 0 {
			res.err = errors.Join(errs...)
			return res
		}
		if err := store.RemoveWorkspace(workspaceName); err != nil {
			res.err = err
			return res
		}
		return res
	}
}

// wsDeleteConfirmCopy returns the title and confirm/cancel hint for the
// active delete-confirmation prompt, tonally matching worktreeDeletePromptLine
// (render.go): the first gate is a warning; the second spells out that the
// action is permanent and defaults to cancel.
func (m model) wsDeleteConfirmCopy() (title, hint string) {
	switch m.prompt {
	case promptConfirmDeleteWsSession:
		return fmt.Sprintf("delete session %q?", m.wsDeleteSession),
			"press y to continue, any other key cancels"
	case promptConfirmDeleteWsSession2:
		return fmt.Sprintf("PERMANENTLY delete session %q?", m.wsDeleteSession),
			"y to delete · any other key cancels (default: cancel)"
	case promptConfirmDeleteWorkspace:
		return fmt.Sprintf("delete workspace %q and all its sessions?", m.wsDeleteWorkspace),
			"press y to continue, any other key cancels"
	case promptConfirmDeleteWorkspace2:
		return fmt.Sprintf("PERMANENTLY delete workspace %q and all its sessions?", m.wsDeleteWorkspace),
			"y to delete · any other key cancels (default: cancel)"
	default:
		return "", ""
	}
}

// renderWsDeleteConfirm renders the floating box shown while a workspace or
// workspace-session delete confirmation is active: a title naming the
// target, one line per member repo with its branch's merge status
// ("checking…" until the matching probe returns), and the confirm/cancel
// hint for whichever of the two gates is active. Mirrors renderWsNamePrompt's
// floating-box composition (workspace_cmd.go) but for a multi-repo
// confirmation instead of a single text prompt.
func (m model) renderWsDeleteConfirm() string {
	title, hint := m.wsDeleteConfirmCopy()
	hintStyle := wtHintStyle
	if m.prompt == promptConfirmDeleteWsSession2 || m.prompt == promptConfirmDeleteWorkspace2 {
		hintStyle = attnErrStyle
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render(title))
	if len(m.wsDeleteMembers) == 0 {
		b.WriteString("\n  " + dimStyle.Render("(no sessions to remove)"))
	}
	for _, mem := range m.wsDeleteMembers {
		info := m.wsDeleteMergeInfo[mem.worktreePath]
		if info == "" {
			info = "checking merge status…"
		}
		label := filepath.Base(mem.repoPath)
		if m.wsDeleteSession == "" {
			// A whole-workspace delete can span several sessions that share
			// the same member repos on different branches — name the session
			// so same-repo lines are distinguishable.
			label = mem.session + "/" + label
		}
		b.WriteString(fmt.Sprintf("\n  %s [%s]: %s", label, mem.branch, info))
	}
	b.WriteString("\n" + hintStyle.Render(hint))
	return paletteBoxStyle.Render(b.String())
}
