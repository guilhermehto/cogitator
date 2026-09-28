package ui

// workspace_safe.go — the "(safe to delete)" tag on Workspaces-view session
// rows. Workspaces hold only in-progress work and are deleted once done, so a
// session whose every member branch is merged into the default branch and
// whose every worktree is clean is flagged: deleting it loses nothing. The
// probe shells out to git per member, so it runs off the UI goroutine on a
// slow timer (and after a pull), never per snapshot.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// wsSafeProbeInterval is how often the safe-to-delete probe re-runs. Merge
// and dirty state change on human timescales; a coarse interval keeps the
// per-member git cost negligible.
const wsSafeProbeInterval = 15 * time.Second

// wsSafeTickMsg marks a safe-to-delete probe as due.
type wsSafeTickMsg struct{}

func wsSafeTickCmd() tea.Cmd {
	return tea.Tick(wsSafeProbeInterval, func(time.Time) tea.Msg { return wsSafeTickMsg{} })
}

// wsSafeMsg carries a finished probe: safe-to-delete per session Dir.
type wsSafeMsg struct {
	safe map[string]bool
}

// maybeProbeWsSafe dispatches a probe when one is due, none is in flight,
// and there are sessions to probe. Sessions still being created, being torn
// down, or missing from disk are skipped (they render no tag).
func (m model) maybeProbeWsSafe() (model, tea.Cmd) {
	if !m.wsSafeDue || m.wsSafeProbing {
		return m, nil
	}
	var sessions []workspace.Session
	for _, ws := range m.wsStatuses {
		for _, sess := range ws.Sessions {
			if sess.State == settings.StateCreating || sess.State == settings.StateMissing {
				continue
			}
			if m.wsDeletePending(ws.Workspace.Name, sess.Session.Name) {
				continue
			}
			sessions = append(sessions, sess.Session)
		}
	}
	if len(sessions) == 0 {
		return m, nil
	}
	m.wsSafeDue = false
	m.wsSafeProbing = true
	return m, wsSafeCmd(m.gitOp, sessions)
}

func wsSafeCmd(gitOp gitOps, sessions []workspace.Session) tea.Cmd {
	if gitOp == nil {
		gitOp = realGitOps{}
	}
	return func() tea.Msg {
		safe := make(map[string]bool, len(sessions))
		for _, sess := range sessions {
			safe[sess.Dir] = sessionSafeToDelete(gitOp, sess)
		}
		return wsSafeMsg{safe: safe}
	}
}

// sessionSafeToDelete reports whether deleting sess loses nothing: every
// member branch is merged into its repo's default branch and every member
// worktree has no uncommitted or untracked changes. Any unknown (git error,
// no default branch) counts as unsafe, as does a session with no members.
func sessionSafeToDelete(gitOp gitOps, sess workspace.Session) bool {
	if len(sess.Members) == 0 {
		return false
	}
	for _, mem := range sess.Members {
		if merged, _ := gitOp.BranchMergeStatus(mem.RepoPath, sess.Branch); merged != git.MergeMerged {
			return false
		}
		if dirty, err := gitOp.IsDirty(mem.WorktreePath); err != nil || dirty {
			return false
		}
	}
	return true
}
