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

	members := make([]SessionMember, 0, len(roots))
	for i, r := range roots {
		dest := filepath.Join(sessionDir, dirNames[i])
		wtPath, err := git.AddWorktree(r, branch, dest)
		if err != nil {
			rollbackAssembly(members, branch, sessionDir)
			return Session{}, fmt.Errorf("assemble worktree for %q: %w", r, err)
		}
		members = append(members, SessionMember{RepoPath: r, WorktreePath: wtPath})
	}

	return Session{
		Name:    sessionName,
		Dir:     sessionDir,
		Branch:  branch,
		Harness: harness,
		Members: members,
	}, nil
}

// rollbackAssembly undoes a partially-assembled session: for each member
// worktree already created it removes the worktree (force), then runs
// `git worktree prune` and an explicit forced branch delete regardless of
// whether the removal itself succeeded, so a RemoveWorktree that failed
// cannot leave a stale .git/worktrees/<name> registration or an orphan branch
// behind. Finally it removes the session directory outright — os.Remove is
// not enough, since a harness may already have written into its CWD. Every
// step here is best-effort: rollbackAssembly has no error to return, only a
// prior failure to avoid compounding.
func rollbackAssembly(created []SessionMember, branch, sessionDir string) {
	for _, m := range created {
		_ = git.RemoveWorktree(m.RepoPath, m.WorktreePath, branch, true)
		_ = git.PruneWorktrees(m.RepoPath)
		_ = git.DeleteBranch(m.RepoPath, branch, true)
	}
	_ = os.RemoveAll(sessionDir)
}

// TeardownSession removes every member worktree and branch in session, then
// removes session's directory. It is best-effort, not unconditional: a
// locked worktree (RemoveWorktree's single --force cannot override a lock)
// or any other per-repo failure does not stop the others from being torn
// down. Every per-repo failure is collected and returned together (via
// errors.Join) rather than the first one masking the rest, and a caller must
// not treat a non-nil error as "nothing was removed."
//
// TeardownSession still runs RemoveWorktree for every member even when
// session.Dir was already deleted by hand: RemoveWorktree's `git worktree
// remove` cleans up the administrative registration in that case rather than
// failing, so no member repo is left with a stale worktree entry.
func TeardownSession(session Session) error {
	var errs []error
	for _, m := range session.Members {
		if err := git.RemoveWorktree(m.RepoPath, m.WorktreePath, session.Branch, true); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.RepoPath, err))
		}
	}
	if err := os.RemoveAll(session.Dir); err != nil {
		errs = append(errs, fmt.Errorf("remove session directory %q: %w", session.Dir, err))
	}
	return errors.Join(errs...)
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
