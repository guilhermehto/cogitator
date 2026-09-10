package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/guilhermehto/cogitator/internal/git"
)

// AssembleSession builds a brand-new Session for ws: one git worktree per
// member repo, all checked out on the same new branch (derived from
// sessionName via SessionBranch), inside a fresh directory under root (laid
// out by SessionDir).
//
// It is pre-flight-then-commit: every member repo is validated — it resolves
// via git.RepoRoot, no two members share a MemberDirName, none has a hidden
// basename, the branch name passes git.CheckRefFormat, the branch does not
// already exist in any member repo, and the session directory does not
// already exist — before anything is created. If any worktree add fails
// partway through, every worktree already created for this session is rolled
// back (best-effort), the session directory is removed, and the returned
// error names the repo that failed and why. Callers are responsible for
// persisting the returned Session (e.g. via Store.AddSession) — AssembleSession
// only touches the filesystem and git state.
//
// Each member's branch is created off whatever HEAD that repo's own main
// worktree currently has, so if repo A is on "main" and repo B is on a
// feature branch, the two new worktrees start from divergent bases.
func AssembleSession(ws Workspace, root, sessionName, harness string) (Session, error) {
	branch, err := SessionBranch(sessionName)
	if err != nil {
		return Session{}, err
	}
	if err := git.CheckRefFormat(branch); err != nil {
		return Session{}, err
	}

	sessionDir, err := SessionDir(root, ws.Name, sessionName)
	if err != nil {
		return Session{}, err
	}
	if dirExists(sessionDir) {
		return Session{}, fmt.Errorf("session directory %q already exists", sessionDir)
	}

	roots := make([]string, 0, len(ws.Members))
	for _, m := range ws.Members {
		resolved, err := git.RepoRoot(m.Path)
		if err != nil {
			return Session{}, fmt.Errorf("member repo %q: %w", m.Path, err)
		}
		roots = append(roots, resolved)
	}
	if err := CheckBasenameCollisions(roots); err != nil {
		return Session{}, err
	}
	dirNames := make([]string, len(roots))
	for i, r := range roots {
		name, err := MemberDirName(r)
		if err != nil {
			return Session{}, err
		}
		dirNames[i] = name
	}
	for _, r := range roots {
		if git.BranchExists(r, branch) {
			return Session{}, fmt.Errorf("branch %q already exists in %q", branch, r)
		}
	}

	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return Session{}, fmt.Errorf("create session directory %q: %w", sessionDir, err)
	}

	// members holds one entry per repo BEFORE its git.AddWorktree call, not
	// after: the entry is appended with the intended dest path first, then
	// overwritten with the canonical worktree path once AddWorktree succeeds.
	// That ordering is what lets rollbackAssembly clean up the repo that just
	// failed, not only the repos that already succeeded — `git worktree add
	// -b <branch>` creates the branch and registers the worktree BEFORE the
	// checkout, so a failing checkout hook or smudge filter can leave both
	// behind even though AddWorktree returns an error.
	members := make([]SessionMember, 0, len(roots))
	for i, r := range roots {
		dest := filepath.Join(sessionDir, dirNames[i])
		idx := len(members)
		members = append(members, SessionMember{RepoPath: r, WorktreePath: dest})
		wtPath, err := git.AddWorktree(r, branch, dest)
		if err != nil {
			rollbackAssembly(members, branch, sessionDir)
			return Session{}, fmt.Errorf("assemble worktree for %q: %w", r, err)
		}
		members[idx].WorktreePath = wtPath
	}

	return Session{
		Name:    sessionName,
		Dir:     sessionDir,
		Branch:  branch,
		Harness: harness,
		Members: members,
	}, nil
}

// rollbackMember undoes one intended member worktree — whether it was fully
// created, partially created (worktree and branch registered but checkout
// failed, e.g. a required smudge filter or a failing post-checkout hook), or
// never created at all: it force-removes the worktree, then runs
// `git worktree prune` and an explicit forced branch delete regardless of
// whether the removal itself succeeded, so a RemoveWorktree that failed
// cannot leave a stale .git/worktrees/<name> registration or an orphan branch
// behind. Every step here is best-effort: rollbackMember has no error to
// return, only a prior failure to avoid compounding.
func rollbackMember(m SessionMember, branch string) {
	_ = git.RemoveWorktree(m.RepoPath, m.WorktreePath, branch, true)
	_ = git.PruneWorktrees(m.RepoPath)
	_ = git.DeleteBranch(m.RepoPath, branch, true)
}

// rollbackAssembly undoes a partially-assembled session: rollbackMember for
// every member already recorded (including the one whose AddWorktree call
// just failed — see the comment on AssembleSession's members slice), then
// removes the session directory outright — os.Remove is not enough, since a
// harness may already have written into its CWD. Best-effort throughout:
// rollbackAssembly has no error to return, only a prior failure to avoid
// compounding.
func rollbackAssembly(created []SessionMember, branch, sessionDir string) {
	for _, m := range created {
		rollbackMember(m, branch)
	}
	_ = os.RemoveAll(sessionDir)
}

// TeardownSession removes every member worktree and branch in session, then
// removes session's directory — but only when every member was removed
// cleanly. It is best-effort, not unconditional: a locked worktree
// (RemoveWorktree's single --force cannot override a lock) or any other
// per-repo failure does not stop the others from being torn down, and it
// leaves session.Dir in place rather than deleting it out from under a member
// that could not be removed — the lock exists to protect that member's
// uncommitted work, and os.RemoveAll would destroy exactly what the lock
// protected while also leaving that repo with a registration pointing at a
// missing directory and no way to retry. Every per-repo failure is collected
// and returned together (via errors.Join) rather than the first one masking
// the rest, and a caller must not treat a non-nil error as "nothing was
// removed." A subsequent call with the same session (once the lock is
// released, say) removes the remaining members and then the session
// directory, since a member already removed is simply skipped.
//
// TeardownSession still runs RemoveWorktree for every member still registered
// even when session.Dir was already deleted by hand: RemoveWorktree's `git
// worktree remove` cleans up the administrative registration in that case
// rather than failing, so no member repo is left with a stale worktree entry.
// A member no longer registered at all (e.g. a prior TeardownSession call
// already removed it) is skipped rather than re-attempted, which is what
// makes a retry after a partial failure converge instead of erroring on the
// members that already succeeded.
//
// A member that is no longer registered still gets a best-effort
// git.DeleteBranch call for session.Branch in that repo. RemoveWorktree
// normally couples worktree removal with branch deletion (see
// internal/git/worktree.go), so a member removed out-of-band (e.g. the user
// ran `git worktree remove` by hand) skips that coupling and would otherwise
// leave the branch behind — permanently blocking a session of the same name
// from ever being reassembled, since AssembleSession's pre-flight refuses to
// reuse an existing branch. The branch is only gone in the common case (the
// prior removal already deleted it, or a previous TeardownSession retry
// already did), and that must stay a silent success, so git.BranchExists is
// checked first and DeleteBranch is only invoked when the branch is actually
// present. A DeleteBranch failure at that point is collected as a per-repo
// failure like every other failure in this loop, rather than swallowed: it
// means the branch is still there and blocking reuse, which is exactly the
// kind of state a caller must be told about rather than a false success.
func TeardownSession(session Session) error {
	var errs []error
	failed := false
	for _, m := range session.Members {
		registered, err := memberStillRegistered(m)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.RepoPath, err))
			failed = true
			continue
		}
		if !registered {
			if git.BranchExists(m.RepoPath, session.Branch) {
				if err := git.DeleteBranch(m.RepoPath, session.Branch, true); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", m.RepoPath, err))
					failed = true
				}
			}
			continue
		}
		if err := git.RemoveWorktree(m.RepoPath, m.WorktreePath, session.Branch, true); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.RepoPath, err))
			failed = true
		}
	}
	if failed {
		return errors.Join(errs...)
	}
	if err := os.RemoveAll(session.Dir); err != nil {
		errs = append(errs, fmt.Errorf("remove session directory %q: %w", session.Dir, err))
	}
	return errors.Join(errs...)
}

// memberStillRegistered reports whether m's worktree is still a registered
// worktree of its repo, by comparing against git.ListWorktrees. A member
// already removed by a prior TeardownSession call is no longer registered at
// all (not merely missing its directory), so this is what lets a retry skip
// it instead of erroring on a worktree that git no longer knows about.
func memberStillRegistered(m SessionMember) (bool, error) {
	wts, err := git.ListWorktrees(m.RepoPath)
	if err != nil {
		return false, err
	}
	for _, wt := range wts {
		if wt.Path == m.WorktreePath {
			return true, nil
		}
	}
	return false, nil
}

// AssembleMember adds one new worktree, on session's existing Branch, for a
// single repo (memberPath) newly attached to session's workspace. It leaves
// every other member of session untouched. Like AssembleSession, it is
// pre-flight-then-commit: memberPath resolves via git.RepoRoot, its
// MemberDirName does not collide with any existing member or have a hidden
// basename, the session's branch does not already exist in the new repo, and
// its destination directory inside session.Dir does not already exist —
// before the worktree is created.
func AssembleMember(session Session, memberPath string) (SessionMember, error) {
	repoRoot, err := git.RepoRoot(memberPath)
	if err != nil {
		return SessionMember{}, fmt.Errorf("member repo %q: %w", memberPath, err)
	}

	roots := make([]string, 0, len(session.Members)+1)
	for _, m := range session.Members {
		roots = append(roots, m.RepoPath)
	}
	roots = append(roots, repoRoot)
	if err := CheckBasenameCollisions(roots); err != nil {
		return SessionMember{}, err
	}

	dirName, err := MemberDirName(repoRoot)
	if err != nil {
		return SessionMember{}, err
	}

	if git.BranchExists(repoRoot, session.Branch) {
		return SessionMember{}, fmt.Errorf("branch %q already exists in %q", session.Branch, repoRoot)
	}

	dest := filepath.Join(session.Dir, dirName)
	if dirExists(dest) {
		return SessionMember{}, fmt.Errorf("member directory %q already exists", dest)
	}

	wtPath, err := git.AddWorktree(repoRoot, session.Branch, dest)
	if err != nil {
		// AddWorktree can fail after `git worktree add -b <branch>` has
		// already registered the worktree and created the branch (a failing
		// checkout hook or smudge filter), so roll back using the intended
		// dest rather than leaving that partial state behind.
		rollbackMember(SessionMember{RepoPath: repoRoot, WorktreePath: dest}, session.Branch)
		return SessionMember{}, fmt.Errorf("assemble worktree for %q: %w", repoRoot, err)
	}

	return SessionMember{RepoPath: repoRoot, WorktreePath: wtPath}, nil
}

// TeardownMember removes one member's worktree and branch from session,
// leaving the other members and the session directory untouched.
func TeardownMember(session Session, member SessionMember) error {
	return git.RemoveWorktree(member.RepoPath, member.WorktreePath, session.Branch, true)
}

// dirExists reports whether path exists on disk (as a directory or any other
// file), used by the pre-flight checks above to refuse creating a session or
// member destination that is already occupied.
func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
