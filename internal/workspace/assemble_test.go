package workspace_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// newAssembleTestRepo creates a temporary git repository with an initial
// commit on "main" and returns its canonical path. The caller owns cleanup
// via t.TempDir.
func newAssembleTestRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	cmds := [][]string{
		{"git", "init", "-q", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "-q", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}

	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", dir, err)
	}
	return canonical
}

// breakSmudgeFilterCheckout configures repo with a required smudge filter
// that always fails, so any future checkout in repo — including the one
// `git worktree add -b <branch>` performs — fails after git has already
// created the branch and registered the worktree. This is the same failure
// shape as a missing git-lfs/git-crypt binary or a non-zero core.hooksPath
// post-checkout hook (lefthook/husky), and unlike a HEAD pointed at a
// nonexistent ref, it fails at checkout time rather than at start-point
// resolution, so the branch and worktree registration are both left behind
// for the rollback to clean up. Passes AssembleSession's pre-flight
// (git.RepoRoot, git.BranchExists) untouched.
func breakSmudgeFilterCheckout(t *testing.T, repo string) {
	t.Helper()

	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("content\n"), 0o644); err != nil {
		t.Fatalf("write tracked file in %s: %v", repo, err)
	}
	attrs := filepath.Join(repo, ".gitattributes")
	if err := os.WriteFile(attrs, []byte("tracked.txt filter=boom\n"), 0o644); err != nil {
		t.Fatalf("write .gitattributes in %s: %v", repo, err)
	}
	for _, args := range [][]string{
		{"git", "add", "tracked.txt", ".gitattributes"},
		{"git", "commit", "-q", "-m", "add filtered file"},
		{"git", "config", "filter.boom.smudge", "false"},
		{"git", "config", "filter.boom.required", "true"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v in %s: %v\n%s", args, repo, err, out)
		}
	}
}

// TestAssembleSession_OneWorktreePerMember verifies the core contract: given
// two valid member repos and a free branch name, assembling a session
// produces a session directory containing one worktree per repo, each
// checked out on the new branch.
func TestAssembleSession_OneWorktreePerMember(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	root := t.TempDir()

	ws := workspace.Workspace{
		Name: "Payments",
		Members: []workspace.MemberRepo{
			{Path: repoA},
			{Path: repoB},
		},
	}

	session, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}

	wantDir, err := workspace.SessionDir(root, "Payments", "Feature X")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	if session.Dir != wantDir {
		t.Errorf("session.Dir = %q, want %q", session.Dir, wantDir)
	}
	wantBranch, err := workspace.SessionBranch("Feature X")
	if err != nil {
		t.Fatalf("SessionBranch: %v", err)
	}
	if session.Branch != wantBranch {
		t.Errorf("session.Branch = %q, want %q", session.Branch, wantBranch)
	}
	if session.Harness != "opencode" {
		t.Errorf("session.Harness = %q, want %q", session.Harness, "opencode")
	}
	if len(session.Members) != 2 {
		t.Fatalf("expected 2 session members, got %d: %+v", len(session.Members), session.Members)
	}

	for _, repo := range []string{repoA, repoB} {
		if !git.BranchExists(repo, wantBranch) {
			t.Errorf("branch %q was not created in %q", wantBranch, repo)
		}
		wts, err := git.ListWorktrees(repo)
		if err != nil {
			t.Fatalf("ListWorktrees(%s): %v", repo, err)
		}
		if len(wts) != 2 {
			t.Errorf("expected 2 worktrees in %s (main + session), got %d: %v", repo, len(wts), wts)
		}
	}

	for _, m := range session.Members {
		if !strings.HasPrefix(m.WorktreePath, wantDir) {
			t.Errorf("member worktree %q is not under session dir %q", m.WorktreePath, wantDir)
		}
		if _, err := os.Stat(m.WorktreePath); err != nil {
			t.Errorf("member worktree %q does not exist: %v", m.WorktreePath, err)
		}
	}
}

// TestAssembleSession_BranchExistsInSecondRepo_CreatesNothing verifies that
// when the target branch already exists in one member repo, assembly creates
// nothing at all: the session directory is never created, the first (valid)
// repo gets no new worktree or branch, and the error names the conflicting
// repo.
func TestAssembleSession_BranchExistsInSecondRepo_CreatesNothing(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	root := t.TempDir()

	branch, err := workspace.SessionBranch("Feature X")
	if err != nil {
		t.Fatalf("SessionBranch: %v", err)
	}
	// Pre-create the branch in repoB without a worktree, so BranchExists sees
	// it but no worktree is attached yet.
	cmd := exec.Command("git", "branch", branch)
	cmd.Dir = repoB
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch %s: %v\n%s", branch, err, out)
	}

	ws := workspace.Workspace{
		Name: "Payments",
		Members: []workspace.MemberRepo{
			{Path: repoA},
			{Path: repoB},
		},
	}

	_, err = workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err == nil {
		t.Fatal("expected AssembleSession to fail when the branch already exists in a member repo")
	}
	if !strings.Contains(err.Error(), repoB) {
		t.Errorf("error %q does not name the conflicting repo %q", err.Error(), repoB)
	}

	sessionDir, err := workspace.SessionDir(root, "Payments", "Feature X")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	if _, statErr := os.Stat(sessionDir); statErr == nil {
		t.Errorf("session directory %q was created despite pre-flight failure", sessionDir)
	}

	wts, err := git.ListWorktrees(repoA)
	if err != nil {
		t.Fatalf("ListWorktrees(repoA): %v", err)
	}
	if len(wts) != 1 {
		t.Errorf("repoA got a new worktree despite repoB's pre-flight failure: %v", wts)
	}
	if git.BranchExists(repoA, branch) {
		t.Errorf("branch %q was created in repoA despite repoB's pre-flight failure", branch)
	}
}

// TestAssembleSession_PreflightFailures verifies that an absent member repo,
// a hidden-basename member, and a member basename collision all fail before
// anything is created, leaving no session directory behind.
func TestAssembleSession_PreflightFailures(t *testing.T) {
	root := t.TempDir()

	assertNothingCreated := func(t *testing.T, workspaceName, sessionName string) {
		t.Helper()
		sessionDir, err := workspace.SessionDir(root, workspaceName, sessionName)
		if err != nil {
			t.Fatalf("SessionDir: %v", err)
		}
		if _, statErr := os.Stat(sessionDir); statErr == nil {
			t.Errorf("session directory %q was created despite pre-flight failure", sessionDir)
		}
	}

	t.Run("member repo absent from disk", func(t *testing.T) {
		absent := filepath.Join(t.TempDir(), "gone")
		ws := workspace.Workspace{
			Name:    "Absent",
			Members: []workspace.MemberRepo{{Path: absent}},
		}
		if _, err := workspace.AssembleSession(ws, root, "Feature X", "opencode"); err == nil {
			t.Fatal("expected error for a member repo absent from disk")
		}
		assertNothingCreated(t, "Absent", "Feature X")
	})

	t.Run("hidden basename", func(t *testing.T) {
		parent := t.TempDir()
		hidden := filepath.Join(parent, ".hidden-repo")
		if err := os.Mkdir(hidden, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", hidden, err)
		}
		for _, args := range [][]string{
			{"git", "init", "-q", "-b", "main"},
			{"git", "config", "user.email", "test@example.com"},
			{"git", "config", "user.name", "Test"},
			{"git", "commit", "-q", "--allow-empty", "-m", "init"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = hidden
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("setup %v: %v\n%s", args, err, out)
			}
		}

		ws := workspace.Workspace{
			Name:    "Hidden",
			Members: []workspace.MemberRepo{{Path: hidden}},
		}
		if _, err := workspace.AssembleSession(ws, root, "Feature X", "opencode"); err == nil {
			t.Fatal("expected error for a hidden-basename member repo")
		}
		assertNothingCreated(t, "Hidden", "Feature X")
	})

	t.Run("basename collision", func(t *testing.T) {
		parentA := filepath.Join(t.TempDir(), "a")
		parentB := filepath.Join(t.TempDir(), "b")
		if err := os.MkdirAll(parentA, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(parentB, 0o755); err != nil {
			t.Fatal(err)
		}
		repoA := newAssembleTestRepoAt(t, filepath.Join(parentA, "shared"))
		repoB := newAssembleTestRepoAt(t, filepath.Join(parentB, "shared"))

		ws := workspace.Workspace{
			Name: "Collide",
			Members: []workspace.MemberRepo{
				{Path: repoA},
				{Path: repoB},
			},
		}
		if _, err := workspace.AssembleSession(ws, root, "Feature X", "opencode"); err == nil {
			t.Fatal("expected error for colliding member basenames")
		}
		assertNothingCreated(t, "Collide", "Feature X")
	})
}

// newAssembleTestRepoAt is like newAssembleTestRepo but creates the repo at
// an exact path rather than one derived from t.TempDir(), so callers can
// control the basename directly (e.g. to force a collision).
func newAssembleTestRepoAt(t *testing.T, dir string) string {
	t.Helper()
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	cmds := [][]string{
		{"git", "init", "-q", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "-q", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}
	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", dir, err)
	}
	return canonical
}

// TestAssembleSession_RollsBackPartiallyCreatedWorktrees verifies that when
// the second member's worktree add fails AFTER git has already created its
// branch and registered its worktree (a required smudge filter, the same
// shape as a missing git-lfs binary or a failing post-checkout hook), that
// second repo is rolled back too — not only the first repo that fully
// succeeded — and the session directory is deleted: nothing is left
// half-assembled anywhere.
func TestAssembleSession_RollsBackPartiallyCreatedWorktrees(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	breakSmudgeFilterCheckout(t, repoB) // passes pre-flight, fails at git.AddWorktree's checkout step
	root := t.TempDir()

	ws := workspace.Workspace{
		Name: "Payments",
		Members: []workspace.MemberRepo{
			{Path: repoA},
			{Path: repoB},
		},
	}

	_, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err == nil {
		t.Fatal("expected AssembleSession to fail when the second repo's worktree add fails")
	}
	if !strings.Contains(err.Error(), repoB) {
		t.Errorf("error %q does not name the failing repo %q", err.Error(), repoB)
	}

	sessionDir, err := workspace.SessionDir(root, "Payments", "Feature X")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	if _, statErr := os.Stat(sessionDir); statErr == nil {
		t.Errorf("session directory %q still exists after rollback", sessionDir)
	}

	branch, err := workspace.SessionBranch("Feature X")
	if err != nil {
		t.Fatalf("SessionBranch: %v", err)
	}
	if git.BranchExists(repoA, branch) {
		t.Errorf("branch %q still exists in repoA after rollback", branch)
	}
	wts, err := git.ListWorktrees(repoA)
	if err != nil {
		t.Fatalf("ListWorktrees(repoA): %v", err)
	}
	if len(wts) != 1 {
		t.Errorf("repoA still has an extra worktree after rollback: %v", wts)
	}

	// repoB is the repo whose AddWorktree call actually failed. Before the
	// fix, rollbackAssembly only iterated members already appended to the
	// slice, and the append happened AFTER the error check — so repoB's
	// branch and worktree registration, both created by git before the
	// checkout failed, survived every rollback.
	if git.BranchExists(repoB, branch) {
		t.Errorf("branch %q still exists in repoB (the failing repo) after rollback", branch)
	}
	wtsB, err := git.ListWorktrees(repoB)
	if err != nil {
		t.Fatalf("ListWorktrees(repoB): %v", err)
	}
	if len(wtsB) != 1 {
		t.Errorf("repoB (the failing repo) still has a stale worktree registration after rollback: %v", wtsB)
	}
}

// TestTeardownSession_RemovesWorktreesBranchesAndDir verifies that tearing
// down an assembled session removes every member worktree and branch and the
// session directory itself, leaving each member repo back at its prior
// worktree set.
func TestTeardownSession_RemovesWorktreesBranchesAndDir(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	root := t.TempDir()

	ws := workspace.Workspace{
		Name: "Payments",
		Members: []workspace.MemberRepo{
			{Path: repoA},
			{Path: repoB},
		},
	}
	session, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}

	if err := workspace.TeardownSession(session); err != nil {
		t.Fatalf("TeardownSession: %v", err)
	}

	if _, statErr := os.Stat(session.Dir); statErr == nil {
		t.Errorf("session directory %q still exists after teardown", session.Dir)
	}
	for _, repo := range []string{repoA, repoB} {
		if git.BranchExists(repo, session.Branch) {
			t.Errorf("branch %q still exists in %q after teardown", session.Branch, repo)
		}
		wts, err := git.ListWorktrees(repo)
		if err != nil {
			t.Fatalf("ListWorktrees(%s): %v", repo, err)
		}
		if len(wts) != 1 {
			t.Errorf("%s did not return to its prior worktree set after teardown: %v", repo, wts)
		}
	}
}

// TestTeardownSession_DirDeletedByHand_StillSucceeds verifies that even when
// the session directory was already removed out from under cogitator (e.g. by
// the user), TeardownSession still succeeds and leaves no stale worktree
// registration in any member repo.
func TestTeardownSession_DirDeletedByHand_StillSucceeds(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	root := t.TempDir()

	ws := workspace.Workspace{
		Name:    "Payments",
		Members: []workspace.MemberRepo{{Path: repoA}},
	}
	session, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}

	if err := os.RemoveAll(session.Dir); err != nil {
		t.Fatalf("remove session dir by hand: %v", err)
	}

	if err := workspace.TeardownSession(session); err != nil {
		t.Fatalf("TeardownSession after manual directory deletion: %v", err)
	}

	wts, err := git.ListWorktrees(repoA)
	if err != nil {
		t.Fatalf("ListWorktrees(repoA): %v", err)
	}
	if len(wts) != 1 {
		t.Errorf("repoA has a stale worktree registration after teardown: %v", wts)
	}
}

// TestTeardownSession_LockedWorktreeReportsFailureButRemovesOthers verifies
// that a locked worktree in one member repo does not stop TeardownSession
// from removing the other members, that the locked repo's failure is
// reported rather than swallowed into an overall success, that the lock's
// whole purpose — protecting uncommitted work — is honoured (the file must
// still be readable on disk afterwards, not merely "some worktree still
// registered"), and that the session directory survives so a retry after the
// lock is released can still complete.
func TestTeardownSession_LockedWorktreeReportsFailureButRemovesOthers(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	root := t.TempDir()

	ws := workspace.Workspace{
		Name: "Payments",
		Members: []workspace.MemberRepo{
			{Path: repoA},
			{Path: repoB},
		},
	}
	session, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}

	var lockedMember workspace.SessionMember
	for _, m := range session.Members {
		if m.RepoPath == repoB {
			lockedMember = m
		}
	}

	const uncommittedContent = "work in progress\n"
	uncommitted := filepath.Join(lockedMember.WorktreePath, "unsaved.txt")
	if err := os.WriteFile(uncommitted, []byte(uncommittedContent), 0o644); err != nil {
		t.Fatalf("write uncommitted file: %v", err)
	}

	lockCmd := exec.Command("git", "worktree", "lock", lockedMember.WorktreePath)
	lockCmd.Dir = repoB
	if out, err := lockCmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree lock: %v\n%s", err, out)
	}

	err = workspace.TeardownSession(session)
	if err == nil {
		t.Fatal("expected TeardownSession to report the locked repo's failure")
	}
	if !strings.Contains(err.Error(), repoB) {
		t.Errorf("error %q does not name the locked repo %q", err.Error(), repoB)
	}

	if git.BranchExists(repoA, session.Branch) {
		t.Errorf("branch %q still exists in repoA despite repoB being locked", session.Branch)
	}
	wtsA, err := git.ListWorktrees(repoA)
	if err != nil {
		t.Fatalf("ListWorktrees(repoA): %v", err)
	}
	if len(wtsA) != 1 {
		t.Errorf("repoA was not torn down despite repoB being locked: %v", wtsA)
	}

	// The lock exists to protect this file. A worktree registration
	// surviving `git worktree list` proves nothing about the file itself —
	// RemoveWorktree can fail on the lock and a caller could still wipe the
	// directory out from under it, which is exactly the defect this guards.
	gotInfo, statErr := os.Stat(uncommitted)
	if statErr != nil {
		t.Fatalf("uncommitted file %q did not survive the failed teardown: %v", uncommitted, statErr)
	}
	if gotInfo.IsDir() {
		t.Fatalf("uncommitted file %q became a directory", uncommitted)
	}
	gotContent, err := os.ReadFile(uncommitted)
	if err != nil {
		t.Fatalf("read uncommitted file %q: %v", uncommitted, err)
	}
	if string(gotContent) != uncommittedContent {
		t.Errorf("uncommitted file content = %q, want %q", gotContent, uncommittedContent)
	}
	if _, err := os.Stat(session.Dir); err != nil {
		t.Errorf("session directory %q was removed despite repoB's failed teardown: %v", session.Dir, err)
	}

	// Release the lock and retry with the same session: the member that
	// already succeeded must not be re-attempted (it is no longer a
	// registered worktree at all), the previously-locked member must now be
	// removed cleanly, and the session directory reclaimed.
	unlockCmd := exec.Command("git", "worktree", "unlock", lockedMember.WorktreePath)
	unlockCmd.Dir = repoB
	if out, err := unlockCmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree unlock: %v\n%s", err, out)
	}

	if err := workspace.TeardownSession(session); err != nil {
		t.Fatalf("TeardownSession after releasing the lock: %v", err)
	}
	if _, err := os.Stat(session.Dir); err == nil {
		t.Errorf("session directory %q still exists after the successful retry", session.Dir)
	}
	if git.BranchExists(repoB, session.Branch) {
		t.Errorf("branch %q still exists in repoB after the successful retry", session.Branch)
	}
	wtsB, err := git.ListWorktrees(repoB)
	if err != nil {
		t.Fatalf("ListWorktrees(repoB): %v", err)
	}
	if len(wtsB) != 1 {
		t.Errorf("repoB was not torn down after the successful retry: %v", wtsB)
	}
}

// TestTeardownSession_UnregisteredMemberBranchStillDeleted verifies that when
// one member's worktree was removed by hand (e.g. `git worktree remove` run
// directly, leaving the branch behind since that bypasses cogitator's
// RemoveWorktree), TeardownSession still deletes that member's branch, still
// removes the other member normally, still removes the session directory,
// and still reports success — so a session of the same name can be
// reassembled afterwards instead of failing forever on "branch already
// exists".
func TestTeardownSession_UnregisteredMemberBranchStillDeleted(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	root := t.TempDir()

	ws := workspace.Workspace{
		Name: "Payments",
		Members: []workspace.MemberRepo{
			{Path: repoA},
			{Path: repoB},
		},
	}
	session, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}

	var repoBMember workspace.SessionMember
	for _, m := range session.Members {
		if m.RepoPath == repoB {
			repoBMember = m
		}
	}

	// Simulate the user removing repoB's worktree by hand: git leaves the
	// branch behind because this bypasses git.RemoveWorktree entirely.
	rmCmd := exec.Command("git", "worktree", "remove", repoBMember.WorktreePath)
	rmCmd.Dir = repoB
	if out, err := rmCmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree remove (by hand): %v\n%s", err, out)
	}
	if !git.BranchExists(repoB, session.Branch) {
		t.Fatalf("precondition failed: branch %q missing from repoB right after manual worktree remove", session.Branch)
	}

	if err := workspace.TeardownSession(session); err != nil {
		t.Fatalf("TeardownSession: %v", err)
	}

	if git.BranchExists(repoB, session.Branch) {
		t.Errorf("branch %q still exists in repoB after teardown of the unregistered member", session.Branch)
	}
	if git.BranchExists(repoA, session.Branch) {
		t.Errorf("branch %q still exists in repoA after teardown", session.Branch)
	}
	wtsA, err := git.ListWorktrees(repoA)
	if err != nil {
		t.Fatalf("ListWorktrees(repoA): %v", err)
	}
	if len(wtsA) != 1 {
		t.Errorf("repoA was not torn down: %v", wtsA)
	}
	if _, statErr := os.Stat(session.Dir); statErr == nil {
		t.Errorf("session directory %q still exists after teardown", session.Dir)
	}
}

// TestTeardownSession_UnregisteredMemberBranchAlreadyGone_StillSucceeds
// verifies that when a member's worktree AND branch were both removed by
// hand (or by a prior TeardownSession call), a missing branch is not treated
// as an error: teardown still succeeds and removes everything else, which is
// what lets a retry after a partial failure converge.
func TestTeardownSession_UnregisteredMemberBranchAlreadyGone_StillSucceeds(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	root := t.TempDir()

	ws := workspace.Workspace{
		Name: "Payments",
		Members: []workspace.MemberRepo{
			{Path: repoA},
			{Path: repoB},
		},
	}
	session, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}

	var repoBMember workspace.SessionMember
	for _, m := range session.Members {
		if m.RepoPath == repoB {
			repoBMember = m
		}
	}

	rmCmd := exec.Command("git", "worktree", "remove", repoBMember.WorktreePath)
	rmCmd.Dir = repoB
	if out, err := rmCmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree remove (by hand): %v\n%s", err, out)
	}
	if err := git.DeleteBranch(repoB, session.Branch, true); err != nil {
		t.Fatalf("DeleteBranch (by hand): %v", err)
	}

	if err := workspace.TeardownSession(session); err != nil {
		t.Fatalf("TeardownSession: %v", err)
	}

	if git.BranchExists(repoA, session.Branch) {
		t.Errorf("branch %q still exists in repoA after teardown", session.Branch)
	}
	wtsA, err := git.ListWorktrees(repoA)
	if err != nil {
		t.Fatalf("ListWorktrees(repoA): %v", err)
	}
	if len(wtsA) != 1 {
		t.Errorf("repoA was not torn down: %v", wtsA)
	}
	if _, statErr := os.Stat(session.Dir); statErr == nil {
		t.Errorf("session directory %q still exists after teardown", session.Dir)
	}
}

// TestAssembleMember_AddsOneWorktreeOnExistingBranch_LeavesOthersUntouched
// verifies that assembling a single newly-attached member into an existing
// session adds exactly one worktree on the session's branch, without
// disturbing the session's other members.
func TestAssembleMember_AddsOneWorktreeOnExistingBranch_LeavesOthersUntouched(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	root := t.TempDir()

	ws := workspace.Workspace{
		Name:    "Payments",
		Members: []workspace.MemberRepo{{Path: repoA}},
	}
	session, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}
	originalMember := session.Members[0]

	newMember, err := workspace.AssembleMember(session, repoB)
	if err != nil {
		t.Fatalf("AssembleMember: %v", err)
	}

	if newMember.RepoPath != repoB {
		t.Errorf("newMember.RepoPath = %q, want %q", newMember.RepoPath, repoB)
	}
	if !strings.HasPrefix(newMember.WorktreePath, session.Dir) {
		t.Errorf("newMember.WorktreePath %q is not under session dir %q", newMember.WorktreePath, session.Dir)
	}
	if !git.BranchExists(repoB, session.Branch) {
		t.Errorf("branch %q was not created in newly-attached repo %q", session.Branch, repoB)
	}

	// The original member must be untouched: same worktree, still present.
	if _, err := os.Stat(originalMember.WorktreePath); err != nil {
		t.Errorf("original member worktree %q was disturbed: %v", originalMember.WorktreePath, err)
	}
	wtsA, err := git.ListWorktrees(repoA)
	if err != nil {
		t.Fatalf("ListWorktrees(repoA): %v", err)
	}
	if len(wtsA) != 2 {
		t.Errorf("repoA's worktree set changed after adding an unrelated member: %v", wtsA)
	}
}

// TestTeardownMember_RemovesOnlyThatWorktreeAndBranch verifies that tearing
// down one member of a multi-member session removes only that member's
// worktree and branch, leaving the other member and the session directory
// itself untouched.
func TestTeardownMember_RemovesOnlyThatWorktreeAndBranch(t *testing.T) {
	repoA := newAssembleTestRepo(t, "repo-a")
	repoB := newAssembleTestRepo(t, "repo-b")
	root := t.TempDir()

	ws := workspace.Workspace{
		Name: "Payments",
		Members: []workspace.MemberRepo{
			{Path: repoA},
			{Path: repoB},
		},
	}
	session, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}

	var target workspace.SessionMember
	for _, m := range session.Members {
		if m.RepoPath == repoB {
			target = m
		}
	}

	if err := workspace.TeardownMember(session, target); err != nil {
		t.Fatalf("TeardownMember: %v", err)
	}

	if git.BranchExists(repoB, session.Branch) {
		t.Errorf("branch %q still exists in repoB after TeardownMember", session.Branch)
	}
	wtsB, err := git.ListWorktrees(repoB)
	if err != nil {
		t.Fatalf("ListWorktrees(repoB): %v", err)
	}
	if len(wtsB) != 1 {
		t.Errorf("repoB was not torn down: %v", wtsB)
	}

	if !git.BranchExists(repoA, session.Branch) {
		t.Errorf("branch %q was removed from repoA by an unrelated TeardownMember call", session.Branch)
	}
	if _, err := os.Stat(session.Dir); err != nil {
		t.Errorf("session directory %q was removed by a per-member teardown: %v", session.Dir, err)
	}
}
