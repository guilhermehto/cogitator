package ui

// workspace_backfill.go — the only consumer of membershipChangedMsg
// (workspace_modal.go): once 'e' commits an attach or detach, this file asks
// which of the target workspace's existing sessions (if any) should receive
// the change, then applies it per session. The membership record itself is
// already persisted by the time membershipChangedMsg arrives — this file
// never touches AttachRepo/DetachRepo again — so nothing here can undo that
// commit; it only decides whether a live session's own worktree bundle
// follows it. Kept separate from workspace_modal.go (membership only) and
// workspace_view.go/workspace_cmd.go/workspace_delete.go per the phase
// convention that every workspace mode routes its key handling through its
// own method rather than adding arms to Update.

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/workspace"
)

// handleMembershipChanged responds to a just-committed attach/detach: when
// the target workspace has no sessions, there is nothing to backfill, so the
// only remaining work is a reload so the change shows up without waiting for
// the next snapshot; when it has one or more sessions, the multi-select
// backfill prompt opens so the user — not this handler — decides which of
// them are touched.
func (m model) handleMembershipChanged(msg membershipChangedMsg) (model, tea.Cmd) {
	sessions := workspaceSessionNames(m.wsStatuses, msg.workspace)
	if len(sessions) == 0 {
		return m.reloadWsStatuses()
	}
	return m.openWorkspaceBackfillPrompt(msg.workspace, msg.repo, msg.attached, sessions)
}

// workspaceSessionNames returns the session names of the workspace named
// name, in order, from statuses — nil when no such workspace is present or
// it has none.
func workspaceSessionNames(statuses []workspace.WorkspaceStatus, name string) []string {
	for _, ws := range statuses {
		if ws.Workspace.Name != name {
			continue
		}
		names := make([]string, len(ws.Workspace.Sessions))
		for i, sess := range ws.Workspace.Sessions {
			names[i] = sess.Name
		}
		return names
	}
	return nil
}

// reloadWsStatuses dispatches a fresh loadWorkspaceStatusCmd, coalescing with
// an in-flight load exactly as the other commit-outcome handlers do
// (wsWorkspaceCreatedMsg, wsSessionAssembledMsg, wsSessionDeletedMsg, ...).
func (m model) reloadWsStatuses() (model, tea.Cmd) {
	if m.wsBuilding {
		m.wsDirty = true
		return m, nil
	}
	m.wsBuilding = true
	return m, loadWorkspaceStatusCmd(m.store, m.snap)
}

// openWorkspaceBackfillPrompt opens the multi-select session picker. Every
// session starts unchecked, so a user who applies without toggling anything
// backfills nothing — the membership change itself already persisted before
// this prompt ever opened.
func (m model) openWorkspaceBackfillPrompt(workspaceName, repo string, attached bool, sessions []string) (model, tea.Cmd) {
	m.wsBackfillWorkspace = workspaceName
	m.wsBackfillRepo = repo
	m.wsBackfillAttached = attached
	m.wsBackfillSessions = sessions
	m.wsBackfillSelected = map[string]bool{}
	m.wsBackfillCursor = 0
	m.prompt = promptWorkspaceBackfill
	return m, nil
}

// closeWorkspaceBackfillPrompt resets the prompt to idle and clears every
// field of the in-progress backfill choice, mirroring clearWsDeleteTarget
// (workspace_delete.go).
func (m *model) closeWorkspaceBackfillPrompt() {
	m.prompt = promptIdle
	m.wsBackfillWorkspace = ""
	m.wsBackfillRepo = ""
	m.wsBackfillAttached = false
	m.wsBackfillSessions = nil
	m.wsBackfillSelected = nil
	m.wsBackfillCursor = 0
}

// updateWorkspaceBackfillActive handles every key while the backfill prompt
// is open: up/down (and ctrl+p/ctrl+n) move the cursor, space toggles the
// highlighted session, enter applies the change to whichever sessions are
// checked (zero is a valid, no-op choice), and esc skips entirely — both
// exits reload the Workspaces view so the already-committed membership
// change shows up without waiting for the next snapshot.
func (m model) updateWorkspaceBackfillActive(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeWorkspaceBackfillPrompt()
		return m.reloadWsStatuses()
	case "enter":
		workspaceName, repo, attached := m.wsBackfillWorkspace, m.wsBackfillRepo, m.wsBackfillAttached
		var chosen []string
		for _, s := range m.wsBackfillSessions {
			if m.wsBackfillSelected[s] {
				chosen = append(chosen, s)
			}
		}
		m.closeWorkspaceBackfillPrompt()
		if len(chosen) == 0 {
			return m.reloadWsStatuses()
		}
		return m, backfillMembershipCmd(m.store, workspaceName, repo, attached, chosen)
	case "up", "ctrl+p":
		m.wsBackfillCursor = clampIndex(m.wsBackfillCursor-1, len(m.wsBackfillSessions))
		return m, nil
	case "down", "ctrl+n":
		m.wsBackfillCursor = clampIndex(m.wsBackfillCursor+1, len(m.wsBackfillSessions))
		return m, nil
	case " ":
		if len(m.wsBackfillSessions) == 0 {
			return m, nil
		}
		if m.wsBackfillSelected == nil {
			m.wsBackfillSelected = map[string]bool{}
		}
		sel := m.wsBackfillSessions[clampIndex(m.wsBackfillCursor, len(m.wsBackfillSessions))]
		m.wsBackfillSelected[sel] = !m.wsBackfillSelected[sel]
		return m, nil
	}
	return m, nil
}

// wsBackfillFailure names one chosen session's backfill failure: the session
// itself, and why — AssembleMember/TeardownMember's own errors already name
// the repo, so the pair together satisfies the requirement that a failure
// "names the session and the repo."
type wsBackfillFailure struct {
	session string
	err     error
}

// wsBackfillAppliedMsg is returned by backfillMembershipCmd once every chosen
// session has been attempted. A session absent from failures either
// succeeded or (for a detach) never had the member in the first place — both
// are the correct outcome, so only actual failures are reported.
type wsBackfillAppliedMsg struct {
	workspaceName string
	repo          string
	attached      bool
	failures      []wsBackfillFailure
}

// backfillMembershipCmd applies workspaceName's already-committed repo
// membership change (attach or detach) to each of sessionNames in turn. Every
// session is processed independently via its own Store.UpdateSessionMembers
// call, so a failure partway through never rolls back a session already
// applied, and a session that fails or was never chosen at all is simply left
// as it was. This is the single tea.Cmd boundary for the whole backfill: no
// git or store access happens on the UI goroutine.
func backfillMembershipCmd(store storeOps, workspaceName, repo string, attached bool, sessionNames []string) tea.Cmd {
	return func() tea.Msg {
		res := wsBackfillAppliedMsg{workspaceName: workspaceName, repo: repo, attached: attached}
		if store == nil {
			for _, sessionName := range sessionNames {
				res.failures = append(res.failures, wsBackfillFailure{
					session: sessionName, err: fmt.Errorf("workspace store is not available"),
				})
			}
			return res
		}
		for _, sessionName := range sessionNames {
			if err := backfillOneSession(store, workspaceName, repo, attached, sessionName); err != nil {
				res.failures = append(res.failures, wsBackfillFailure{session: sessionName, err: err})
			}
		}
		return res
	}
}

// sessionMemberUpdater is the narrow seam backfillOneSession asserts store
// against to reach Store.UpdateSessionMembers without widening storeOps
// (model.go) itself: storeOps is the injectable seam shared by every other
// workspace command in this package, and this mutator exists only for the
// backfill path, so it is declared here instead. realStoreOps and
// *fakeStoreOps (workspace_cmd_test.go) both implement it — see the
// realStoreOps method just below and fakeStoreOps's in
// workspace_backfill_test.go.
type sessionMemberUpdater interface {
	UpdateSessionMembers(workspaceName, sessionName string, mutate func(*workspace.Session) error) error
}

// UpdateSessionMembers delegates to the concrete *workspace.Store, giving
// realStoreOps (model.go) the one extra method the backfill path needs
// without adding it to the shared storeOps interface.
func (r realStoreOps) UpdateSessionMembers(workspaceName, sessionName string, mutate func(*workspace.Session) error) error {
	return r.store.UpdateSessionMembers(workspaceName, sessionName, mutate)
}

// backfillOneSession applies the membership change to exactly one session:
// locate sessionName inside workspaceName and, under Store's own mutex via
// UpdateSessionMembers, assemble (attached) or tear down (!attached) the one
// member via step 8's AssembleMember/TeardownMember — which run their own
// repo-in-this-session pre-flight (branch free, basename collision) since the
// new worktree joins the session's existing branch. The whole
// read-modify-write happens inside that single store call, so two overlapping
// backfills of different sessions can never interleave and silently drop one
// another's update. Every other workspace and session is carried through
// untouched, so a failure here never disturbs them.
func backfillOneSession(store storeOps, workspaceName, repo string, attached bool, sessionName string) error {
	updater, ok := store.(sessionMemberUpdater)
	if !ok {
		return fmt.Errorf("workspace store does not support session member updates")
	}
	return updater.UpdateSessionMembers(workspaceName, sessionName, func(session *workspace.Session) error {
		if attached {
			member, err := workspace.AssembleMember(*session, repo)
			if err != nil {
				return err
			}
			session.Members = append(append([]workspace.SessionMember{}, session.Members...), member)
			return nil
		}

		mIdx := -1
		for i, mem := range session.Members {
			if mem.RepoPath == repo {
				mIdx = i
				break
			}
		}
		if mIdx < 0 {
			// Session never had this member — e.g. it was itself skipped by
			// an earlier backfill. The desired outcome (no worktree for repo
			// in this session) already holds, so this is success, not error.
			return nil
		}
		member := session.Members[mIdx]
		if err := workspace.TeardownMember(*session, member); err != nil {
			return err
		}
		session.Members = append(session.Members[:mIdx], session.Members[mIdx+1:]...)
		return nil
	})
}

// renderWorkspaceBackfillPrompt renders the floating multi-select box shown
// while prompt == promptWorkspaceBackfill: a title naming the repo and
// whether it was attached or detached, one checkbox line per session in the
// target workspace, and a hint. Mirrors renderWsDeleteConfirm's floating-box
// composition (workspace_delete.go) but for a checklist instead of a
// probe-annotated confirm.
func (m model) renderWorkspaceBackfillPrompt() string {
	repoName := filepath.Base(m.wsBackfillRepo)
	var title string
	if m.wsBackfillAttached {
		title = fmt.Sprintf("%s attached to %q — add it to which sessions?", repoName, m.wsBackfillWorkspace)
	} else {
		title = fmt.Sprintf("%s detached from %q — remove it from which sessions?", repoName, m.wsBackfillWorkspace)
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render(title))
	if len(m.wsBackfillSessions) == 0 {
		b.WriteString("\n  " + dimStyle.Render("(no sessions in this workspace)"))
	}
	for i, sess := range m.wsBackfillSessions {
		box := "[ ]"
		if m.wsBackfillSelected[sess] {
			box = "[x]"
		}
		line := fmt.Sprintf("  %s %s", box, sess)
		if i == m.wsBackfillCursor {
			line = wtCursorStyle.Render(line)
		}
		b.WriteString("\n" + line)
	}
	b.WriteString("\n" + dimStyle.Render("↑↓ move · space toggle · enter apply · esc skip"))
	return paletteBoxStyle.Render(b.String())
}
