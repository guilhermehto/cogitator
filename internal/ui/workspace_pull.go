package ui

// workspace_pull.go — 'P' in the Workspaces view. On a workspace header it
// fast-forwards every member repo's base checkout (so the next session
// branches from the latest code); on a session row it fast-forwards the
// session branch in each member worktree. Both reuse the Repos view's
// `git pull --ff-only` (gitOps.Pull), run members concurrently off the UI
// goroutine, and report one outcome per member in wsHint.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/settings"
)

// wsPullJob is one member repo to pull. branch empty means "whatever the
// checkout at path is on" (a workspace member's base checkout); otherwise
// branch is the session branch, pulled only when origin has it.
type wsPullJob struct {
	label    string
	repoPath string
	path     string
	branch   string
}

// wsPullOutcome is one member's result: skip names why no pull was attempted
// (e.g. the session branch was never pushed); otherwise summary/err carry
// git's result.
type wsPullOutcome struct {
	label   string
	skip    string
	summary string
	err     error
}

// wsPullFinishedMsg reports every member's outcome for one 'P'.
type wsPullFinishedMsg struct {
	target   wsTarget
	outcomes []wsPullOutcome
}

// wsPullPending reports whether a pull for exactly target is in flight.
func (m model) wsPullPending(target wsTarget) bool {
	_, ok := m.wsPulling[target]
	return ok
}

// updateWorkspacePull handles 'P' in the Workspaces view. Returns
// handled=false for any other key, or when there is nothing under the cursor.
func (m model) updateWorkspacePull(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if msg.String() != "P" {
		return m, nil, false
	}
	if ws, sess, ok := m.wsSessionUnderCursor(); ok {
		target := wsTarget{workspace: ws.Workspace.Name, session: sess.Session.Name}
		switch {
		case m.wsDeletePending(target.workspace, target.session):
			m.wsHint = fmt.Sprintf("session %q is being deleted", sess.Session.Name)
			return m, nil, true
		case sess.State == settings.StateCreating:
			m.wsHint = fmt.Sprintf("session %q is still being created", sess.Session.Name)
			return m, nil, true
		case sess.State == settings.StateMissing:
			m.wsHint = fmt.Sprintf("session %q directory is missing — cannot pull", sess.Session.Name)
			return m, nil, true
		case len(sess.Session.Members) == 0:
			m.wsHint = fmt.Sprintf("session %q has no member repos to pull", sess.Session.Name)
			return m, nil, true
		case m.wsPullPending(target):
			return m, nil, true
		}
		jobs := make([]wsPullJob, 0, len(sess.Session.Members))
		for _, mem := range sess.Session.Members {
			jobs = append(jobs, wsPullJob{
				label:    filepath.Base(mem.RepoPath),
				repoPath: mem.RepoPath,
				path:     mem.WorktreePath,
				branch:   sess.Session.Branch,
			})
		}
		next, cmd := m.startWsPull(target, jobs)
		return next, cmd, true
	}
	if ws, ok := m.wsUnderCursor(); ok {
		target := wsTarget{workspace: ws.Name}
		if m.wsDeletePending(ws.Name, "") {
			m.wsHint = fmt.Sprintf("workspace %q is being deleted", ws.Name)
			return m, nil, true
		}
		if m.wsPullPending(target) {
			return m, nil, true
		}
		var jobs []wsPullJob
		for _, mem := range ws.Members {
			if mem.Missing {
				continue
			}
			jobs = append(jobs, wsPullJob{label: filepath.Base(mem.Path), repoPath: mem.Path, path: mem.Path})
		}
		if len(jobs) == 0 {
			m.wsHint = fmt.Sprintf("workspace %q has no member repos to pull", ws.Name)
			return m, nil, true
		}
		next, cmd := m.startWsPull(target, jobs)
		return next, cmd, true
	}
	return m, nil, false
}

// startWsPull marks target pending (its row animates "pulling…") and
// dispatches the pull alongside the shared spinner.
func (m model) startWsPull(target wsTarget, jobs []wsPullJob) (model, tea.Cmd) {
	if m.wsPulling == nil {
		m.wsPulling = map[wsTarget]struct{}{}
	}
	m.wsPulling[target] = struct{}{}
	var spinnerC tea.Cmd
	if !m.spinnerActive {
		m.spinnerActive = true
		spinnerC = spinnerTickCmd()
	}
	return m, tea.Batch(wsPullCmd(m.gitOp, target, jobs), spinnerC)
}

// wsPullCmd runs every job concurrently — each is an independent repo and a
// network round-trip — and reports the outcomes in job order.
func wsPullCmd(gitOp gitOps, target wsTarget, jobs []wsPullJob) tea.Cmd {
	if gitOp == nil {
		gitOp = realGitOps{}
	}
	return func() tea.Msg {
		outcomes := make([]wsPullOutcome, len(jobs))
		var wg sync.WaitGroup
		for i, job := range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				outcomes[i] = runWsPullJob(gitOp, job)
			}()
		}
		wg.Wait()
		return wsPullFinishedMsg{target: target, outcomes: outcomes}
	}
}

func runWsPullJob(gitOp gitOps, job wsPullJob) wsPullOutcome {
	out := wsPullOutcome{label: job.label}
	branch := job.branch
	if branch == "" {
		current, err := gitOp.CurrentBranch(job.path)
		if err != nil {
			out.err = err
			return out
		}
		if current == "" {
			out.skip = "detached HEAD"
			return out
		}
		branch = current
	} else if !gitOp.RemoteBranchExists(job.repoPath, branch) {
		// A session branch is created locally; until it is pushed there is
		// nothing on origin to pull, which is expected rather than a failure.
		out.skip = "no upstream"
		return out
	}
	out.summary, out.err = gitOp.Pull(job.path, branch)
	return out
}

// wsPullHint phrases a finished pull as one line: the target, then each
// member's outcome.
func wsPullHint(target wsTarget, outcomes []wsPullOutcome) string {
	label := target.workspace
	if target.session != "" {
		label += "/" + target.session
	}
	parts := make([]string, len(outcomes))
	for i, o := range outcomes {
		switch {
		case o.err != nil:
			parts[i] = fmt.Sprintf("%s: failed (%s)", o.label, firstLine(o.err.Error()))
		case o.skip != "":
			parts[i] = o.label + ": " + o.skip
		case o.summary != "":
			parts[i] = o.label + ": " + o.summary
		default:
			parts[i] = o.label + ": pulled"
		}
	}
	return "pull " + label + " — " + strings.Join(parts, " · ")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
