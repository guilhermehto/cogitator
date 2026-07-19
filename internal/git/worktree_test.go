package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// initRepo creates a temporary git repository with an initial commit on "main"
// and returns its path. The caller owns cleanup via t.TempDir.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// TestListWorktrees_MainOnly verifies that a freshly initialised repo reports
// exactly one worktree (the main worktree) with a canonical path.
func TestListWorktrees_MainOnly(t *testing.T) {
	repo := initRepo(t)

	wts, err := git.ListWorktrees(repo)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(wts) != 1 {
		t.Fatalf("expected 1 worktree, got %d: %v", len(wts), wts)
	}

	wt := wts[0]
	if wt.Branch != "main" {
		t.Errorf("branch: got %q, want %q", wt.Branch, "main")
	}

	// Path must be canonical (EvalSymlinks-resolved, clean, no trailing sep).
	want, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", repo, err)
	}
	if wt.Path != want {
		t.Errorf("path: got %q, want %q", wt.Path, want)
	}
}

// TestAddWorktree_ListsWithCanonicalPath verifies the core contract:
//   - AddWorktree creates the worktree and returns a canonical path.
//   - ListWorktrees subsequently reports both worktrees with canonical paths.
//   - The path returned by AddWorktree matches the path reported by ListWorktrees.
func TestAddWorktree_ListsWithCanonicalPath(t *testing.T) {
	repo := initRepo(t)

	// Place the new worktree in a sibling temp directory so it is outside the
	// repo root (the common real-world layout).
	wtDir := filepath.Join(t.TempDir(), "feature-wt")

	gotPath, err := git.AddWorktree(repo, "feature", wtDir)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// The returned path must be canonical.
	wantPath, err := pathnorm.Canonical(wtDir)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", wtDir, err)
	}
	if gotPath != wantPath {
		t.Errorf("AddWorktree returned path %q, want canonical %q", gotPath, wantPath)
	}

	// The worktree directory must actually exist on disk.
	if _, err := os.Stat(gotPath); err != nil {
		t.Errorf("worktree dir %q does not exist after AddWorktree: %v", gotPath, err)
	}

	// ListWorktrees must now report two entries.
	wts, err := git.ListWorktrees(repo)
	if err != nil {
		t.Fatalf("ListWorktrees after add: %v", err)
	}
	if len(wts) != 2 {
		t.Fatalf("expected 2 worktrees, got %d: %v", len(wts), wts)
	}

	// Find the new worktree in the list.
	var found *git.Worktree
	for i := range wts {
		if wts[i].Path == gotPath {
			found = &wts[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("new worktree %q not found in list: %v", gotPath, wts)
	}
	if found.Branch != "feature" {
		t.Errorf("branch: got %q, want %q", found.Branch, "feature")
	}
}

// TestAddWorktree_DuplicateBranchErrors verifies that attempting to create a
// second worktree on an already-existing branch returns a non-nil error and
// does not create the destination directory.
func TestAddWorktree_DuplicateBranchErrors(t *testing.T) {
	repo := initRepo(t)

	// Create the first worktree on "feature".
	first := filepath.Join(t.TempDir(), "wt-first")
	if _, err := git.AddWorktree(repo, "feature", first); err != nil {
		t.Fatalf("first AddWorktree: %v", err)
	}

	// Attempt to create a second worktree on the same branch.
	second := filepath.Join(t.TempDir(), "wt-second")
	_, err := git.AddWorktree(repo, "feature", second)
	if err == nil {
		t.Fatal("expected error for duplicate branch, got nil")
	}

	// The second destination must not have been created.
	if _, statErr := os.Stat(second); statErr == nil {
		t.Errorf("destination %q was created despite duplicate-branch error", second)
	}

	// The error message should mention the branch name so callers can surface it.
	if !strings.Contains(err.Error(), "feature") {
		t.Logf("error does not mention branch name (acceptable): %v", err)
	}
}

// TestRemoveWorktree_RemovesCleanWorktree verifies that RemoveWorktree deletes
// a clean worktree's directory and drops it from the worktree list, while the
// repository's main worktree remains.
func TestRemoveWorktree_RemovesCleanWorktree(t *testing.T) {
	repo := initRepo(t)

	wtDir := filepath.Join(t.TempDir(), "feature-wt")
	gotPath, err := git.AddWorktree(repo, "feature", wtDir)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	if err := git.RemoveWorktree(repo, gotPath, "feature", false); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// The directory must be gone.
	if _, statErr := os.Stat(gotPath); !os.IsNotExist(statErr) {
		t.Errorf("worktree dir %q still exists after RemoveWorktree (stat err = %v)", gotPath, statErr)
	}

	// Only the main worktree should remain.
	wts, err := git.ListWorktrees(repo)
	if err != nil {
		t.Fatalf("ListWorktrees after remove: %v", err)
	}
	if len(wts) != 1 {
		t.Fatalf("expected 1 worktree after remove, got %d: %v", len(wts), wts)
	}
}

// TestRemoveWorktree_DeletesBranchAllowingRecreate reproduces the reported bug:
// removing a worktree must also delete its branch, so a new worktree can be
// created under the same name afterwards. Before the fix `git worktree remove`
// left the branch behind and the recreate failed with "branch already exists".
func TestRemoveWorktree_DeletesBranchAllowingRecreate(t *testing.T) {
	repo := initRepo(t)

	wtDir := filepath.Join(t.TempDir(), "feature-wt")
	gotPath, err := git.AddWorktree(repo, "feature", wtDir)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	if err := git.RemoveWorktree(repo, gotPath, "feature", true); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// The branch must be gone.
	if err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/feature").Run(); err == nil {
		t.Error("branch \"feature\" still exists after RemoveWorktree")
	}

	// Recreating under the same name must now succeed.
	recreated := filepath.Join(t.TempDir(), "feature-wt-again")
	if _, err := git.AddWorktree(repo, "feature", recreated); err != nil {
		t.Fatalf("recreate on same branch name after removal: %v", err)
	}
}

// TestRemoveWorktree_RefusesDirtyWorktree verifies that RemoveWorktree returns
// an error (and leaves the directory intact) when the worktree has untracked
// changes — the safety property that protects unsaved work from deletion.
func TestRemoveWorktree_RefusesDirtyWorktree(t *testing.T) {
	repo := initRepo(t)

	wtDir := filepath.Join(t.TempDir(), "dirty-wt")
	gotPath, err := git.AddWorktree(repo, "feature", wtDir)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// Introduce an untracked file so the worktree is dirty.
	if err := os.WriteFile(filepath.Join(gotPath, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	if err := git.RemoveWorktree(repo, gotPath, "feature", false); err == nil {
		t.Fatal("expected RemoveWorktree to refuse a dirty worktree, got nil error")
	}

	// The directory must still exist — nothing was deleted.
	if _, statErr := os.Stat(gotPath); statErr != nil {
		t.Errorf("dirty worktree %q was removed despite refusal: %v", gotPath, statErr)
	}
}

// TestRemoveWorktree_ForceRemovesDirtyWorktree verifies that passing force=true
// removes a dirty worktree (untracked changes) that a non-force remove refuses,
// discarding the uncommitted changes. This is the force-by-default delete path.
func TestRemoveWorktree_ForceRemovesDirtyWorktree(t *testing.T) {
	repo := initRepo(t)

	wtDir := filepath.Join(t.TempDir(), "dirty-force-wt")
	gotPath, err := git.AddWorktree(repo, "feature", wtDir)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// An untracked file makes the worktree dirty — the case a non-force remove
	// refuses; force must delete it anyway.
	if err := os.WriteFile(filepath.Join(gotPath, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	if err := git.RemoveWorktree(repo, gotPath, "feature", true); err != nil {
		t.Fatalf("force RemoveWorktree must remove a dirty worktree, got: %v", err)
	}

	// The directory must be gone.
	if _, statErr := os.Stat(gotPath); !os.IsNotExist(statErr) {
		t.Errorf("dirty worktree %q still exists after force remove (stat err = %v)", gotPath, statErr)
	}

	// Only the main worktree should remain.
	wts, err := git.ListWorktrees(repo)
	if err != nil {
		t.Fatalf("ListWorktrees after force remove: %v", err)
	}
	if len(wts) != 1 {
		t.Fatalf("expected 1 worktree after force remove, got %d: %v", len(wts), wts)
	}
}

// initRemoteWithBranch creates a temporary git repository to serve as a fetch
// origin. It has an initial commit on "main" and an extra branch named branch
// carrying one additional commit, then checks "main" back out so the feature
// branch exists only as a ref. Returns its path.
func initRemoteWithBranch(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
		{"git", "checkout", "-b", branch},
		{"git", "commit", "--allow-empty", "-m", "feature work"},
		{"git", "checkout", "main"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// addOrigin wires remote as the "origin" of repo using the standard fetch
// refspec, so `git fetch origin <branch>` populates refs/remotes/origin/<branch>.
func addOrigin(t *testing.T, repo, remote string) {
	t.Helper()
	cmd := exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
}

// cloneSingleBranch makes a single-branch clone of remote (refspec narrowed to
// +refs/heads/main:refs/remotes/origin/main, as `git clone --single-branch`
// produces) and returns its path. Unlike addOrigin's wildcard refspec, a branch
// other than main is not mapped, so fetching it would not create
// refs/remotes/origin/<branch> without FetchAndAddWorktree registering it first.
func cloneSingleBranch(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "single-branch-clone")
	cmd := exec.Command("git", "clone", "--single-branch", "--branch", "main", remote, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --single-branch: %v\n%s", err, out)
	}
	return dir
}

// TestFetchAndAddWorktree_SingleBranchCloneTracksRemote is a regression test for
// a single-branch clone, whose fetch refspec maps only main. Without registering
// the branch in origin's refspec, `git fetch origin <branch>` updates FETCH_HEAD
// only and the subsequent worktree add fails to resolve origin/<branch>. This
// asserts the branch is checked out and tracks origin/<branch> regardless.
func TestFetchAndAddWorktree_SingleBranchCloneTracksRemote(t *testing.T) {
	const branch = "mpetersen/remove-cupac-ff"
	remote := initRemoteWithBranch(t, branch)
	local := cloneSingleBranch(t, remote)

	wtDir := filepath.Join(t.TempDir(), "feature-wt")
	gotPath, err := git.FetchAndAddWorktree(local, branch, wtDir)
	if err != nil {
		t.Fatalf("FetchAndAddWorktree on single-branch clone: %v", err)
	}

	wts, err := git.ListWorktrees(local)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	var found bool
	for _, wt := range wts {
		if wt.Path == gotPath {
			found = true
			if wt.Branch != branch {
				t.Errorf("branch: got %q, want %q", wt.Branch, branch)
			}
		}
	}
	if !found {
		t.Fatalf("new worktree %q not found in list: %v", gotPath, wts)
	}

	// Tracking must be wired even though the original clone refspec excluded the
	// branch — git validates @{upstream} against the configured refspec.
	track := exec.Command("git", "rev-parse", "--abbrev-ref", branch+"@{upstream}")
	track.Dir = local
	out, err := track.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse upstream: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "origin/"+branch {
		t.Errorf("upstream: got %q, want %q", got, "origin/"+branch)
	}
}

// TestFetchAndAddWorktree_FetchesRemoteBranch verifies the core contract:
// FetchAndAddWorktree fetches a branch that exists only on origin, checks it out
// into a new worktree at a canonical path, and sets the new local branch to
// track origin/<branch>.
func TestFetchAndAddWorktree_FetchesRemoteBranch(t *testing.T) {
	const branch = "feature/remote-only"
	remote := initRemoteWithBranch(t, branch)
	local := initRepo(t)
	addOrigin(t, local, remote)

	wtDir := filepath.Join(t.TempDir(), "feature-wt")
	gotPath, err := git.FetchAndAddWorktree(local, branch, wtDir)
	if err != nil {
		t.Fatalf("FetchAndAddWorktree: %v", err)
	}

	wantPath, err := pathnorm.Canonical(wtDir)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", wtDir, err)
	}
	if gotPath != wantPath {
		t.Errorf("returned path %q, want canonical %q", gotPath, wantPath)
	}
	if _, err := os.Stat(gotPath); err != nil {
		t.Errorf("worktree dir %q does not exist after fetch: %v", gotPath, err)
	}

	// The worktree must check out a local branch named after the remote branch.
	wts, err := git.ListWorktrees(local)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	var found bool
	for _, wt := range wts {
		if wt.Path == gotPath {
			found = true
			if wt.Branch != branch {
				t.Errorf("branch: got %q, want %q", wt.Branch, branch)
			}
		}
	}
	if !found {
		t.Fatalf("new worktree %q not found in list: %v", gotPath, wts)
	}

	// The new local branch must track origin/<branch> so future pulls/pushes
	// target the remote it came from.
	track := exec.Command("git", "rev-parse", "--abbrev-ref", branch+"@{upstream}")
	track.Dir = local
	out, err := track.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse upstream: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "origin/"+branch {
		t.Errorf("upstream: got %q, want %q", got, "origin/"+branch)
	}
}

// TestFetchAndAddWorktree_MissingRemoteBranchErrors verifies that fetching a
// branch that does not exist on origin returns a non-nil error and creates
// nothing on disk.
func TestFetchAndAddWorktree_MissingRemoteBranchErrors(t *testing.T) {
	remote := initRemoteWithBranch(t, "feature/remote-only")
	local := initRepo(t)
	addOrigin(t, local, remote)

	wtDir := filepath.Join(t.TempDir(), "nope-wt")
	_, err := git.FetchAndAddWorktree(local, "does/not/exist", wtDir)
	if err == nil {
		t.Fatal("expected error fetching a nonexistent remote branch, got nil")
	}
	if _, statErr := os.Stat(wtDir); statErr == nil {
		t.Errorf("destination %q was created despite fetch failure", wtDir)
	}
}

// TestListWorktrees_BranchNames verifies that branch names with slashes
// (e.g. "feat/foo") are preserved correctly after stripping the refs/heads/ prefix.
func TestListWorktrees_BranchNames(t *testing.T) {
	repo := initRepo(t)

	wtDir := filepath.Join(t.TempDir(), "feat-foo-wt")
	if _, err := git.AddWorktree(repo, "feat/foo", wtDir); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	wts, err := git.ListWorktrees(repo)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}

	var found bool
	for _, wt := range wts {
		if wt.Branch == "feat/foo" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("branch %q not found in worktrees: %v", "feat/foo", wts)
	}
}

// cloneRepo makes a full clone of remote into a fresh temp dir and returns its
// path. The clone has an origin/main branch, so git.Pull can fast-forward it.
func cloneRepo(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", remote, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	return dir
}

// commitOnRemote adds an empty commit to remote's currently checked-out branch
// so a subsequent fetch/pull from a clone has something to fast-forward to.
func commitOnRemote(t *testing.T, remote, msg string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", msg)
	cmd.Dir = remote
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit on remote: %v\n%s", err, out)
	}
}

func commitFile(t *testing.T, repo, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	add := exec.Command("git", "add", name)
	add.Dir = repo
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", name, err, out)
	}
	commit := exec.Command("git", "commit", "-m", msg)
	commit.Dir = repo
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit %s: %v\n%s", name, err, out)
	}
}

func tagRemote(t *testing.T, remote, name string) {
	t.Helper()
	cmd := exec.Command("git", "tag", name)
	cmd.Dir = remote
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag on remote: %v\n%s", err, out)
	}
}

// TestPull_FastForwardsFromOrigin verifies the core contract: Pull fast-forwards
// the worktree's branch to a commit that exists only on origin and returns a
// non-empty summary.
func TestPull_FastForwardsFromOrigin(t *testing.T) {
	remote := initRepo(t)
	local := cloneRepo(t, remote)

	commitOnRemote(t, remote, "remote work")
	tagRemote(t, remote, "v-remote-work")

	summary, err := git.Pull(local, "main")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if summary == "" {
		t.Error("expected a non-empty pull summary")
	}

	// The clone's main must now contain the commit that was only on the remote.
	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = local
	out, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "remote work") {
		t.Errorf("pull did not fast-forward the remote commit in; log:\n%s", out)
	}

	tagCmd := exec.Command("git", "tag", "--list", "v-remote-work")
	tagCmd.Dir = local
	tags, err := tagCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git tag: %v\n%s", err, tags)
	}
	if strings.TrimSpace(string(tags)) != "" {
		t.Errorf("pull fetched tags despite --no-tags: %s", tags)
	}
}

func TestPull_AutostashesDirtyTrackedFiles(t *testing.T) {
	remote := initRepo(t)
	commitFile(t, remote, "lsp.txt", "one\nlocal-old\nremote-old\n", "add tracked file")
	local := cloneRepo(t, remote)

	if err := os.WriteFile(filepath.Join(local, "lsp.txt"), []byte("one\nlocal-new\nremote-old\n"), 0o644); err != nil {
		t.Fatalf("dirty local file: %v", err)
	}
	commitFile(t, remote, "lsp.txt", "one\nlocal-old\nremote-new\n", "remote edit")

	if _, err := git.Pull(local, "main"); err != nil {
		t.Fatalf("Pull with dirty tracked file: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(local, "lsp.txt"))
	if err != nil {
		t.Fatalf("read local file: %v", err)
	}
	if string(got) != "one\nlocal-new\nremote-new\n" {
		t.Errorf("file after pull = %q", got)
	}
}

// TestPull_AlreadyUpToDate verifies that pulling a branch with nothing new on
// origin succeeds and reports an "up to date" summary rather than an error.
func TestPull_AlreadyUpToDate(t *testing.T) {
	remote := initRepo(t)
	local := cloneRepo(t, remote)

	summary, err := git.Pull(local, "main")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !strings.Contains(strings.ToLower(summary), "up to date") {
		t.Errorf("summary = %q, want an 'up to date' message", summary)
	}
}

// TestPull_NoOriginErrors verifies that pulling without an origin remote returns
// a non-nil error.
func TestPull_NoOriginErrors(t *testing.T) {
	repo := initRepo(t) // no origin configured

	if _, err := git.Pull(repo, "main"); err == nil {
		t.Fatal("expected Pull to error when origin is missing")
	}
}

// TestPull_DivergedHistoryErrors locks in the --ff-only safety property: when
// local and remote histories have diverged, Pull refuses rather than creating a
// merge commit or opening an editor.
func TestPull_DivergedHistoryErrors(t *testing.T) {
	remote := initRepo(t)
	local := cloneRepo(t, remote)

	commitOnRemote(t, remote, "remote work")
	commitLocal := exec.Command("git", "commit", "--allow-empty", "-m", "local work")
	commitLocal.Dir = local
	if out, err := commitLocal.CombinedOutput(); err != nil {
		t.Fatalf("local commit: %v\n%s", err, out)
	}

	if _, err := git.Pull(local, "main"); err == nil {
		t.Fatal("expected --ff-only Pull to refuse a diverged branch")
	}
}
