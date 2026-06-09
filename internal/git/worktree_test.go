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

	if err := git.RemoveWorktree(repo, gotPath); err != nil {
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

	if err := git.RemoveWorktree(repo, gotPath); err == nil {
		t.Fatal("expected RemoveWorktree to refuse a dirty worktree, got nil error")
	}

	// The directory must still exist — nothing was deleted.
	if _, statErr := os.Stat(gotPath); statErr != nil {
		t.Errorf("dirty worktree %q was removed despite refusal: %v", gotPath, statErr)
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
