package git_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/guilhermehto/cogitator/internal/git"
)

// initRepoOn creates a temporary git repository whose initial branch is named
// branch, with one empty commit, and returns its path.
func initRepoOn(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", branch},
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

// commitInWorktree creates an empty commit in the worktree at dir so its branch
// advances past the default branch.
func commitInWorktree(t *testing.T, dir, message string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", message)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit in %q: %v\n%s", dir, err, out)
	}
}

// TestBranchMergeStatus_FreshBranchIsMerged verifies that a branch created from
// main with no new commits is reported as merged into main.
func TestBranchMergeStatus_FreshBranchIsMerged(t *testing.T) {
	repo := initRepoOn(t, "main")

	wtDir := filepath.Join(t.TempDir(), "feature-wt")
	if _, err := git.AddWorktree(repo, "feature", wtDir); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	state, base := git.BranchMergeStatus(repo, "feature")
	if state != git.MergeMerged {
		t.Errorf("state = %v, want MergeMerged", state)
	}
	if base != "main" {
		t.Errorf("base = %q, want main", base)
	}
}

// TestBranchMergeStatus_AdvancedBranchIsNotMerged verifies that a branch with a
// commit not present on main is reported as not merged.
func TestBranchMergeStatus_AdvancedBranchIsNotMerged(t *testing.T) {
	repo := initRepoOn(t, "main")

	wtDir := filepath.Join(t.TempDir(), "feature-wt")
	gotPath, err := git.AddWorktree(repo, "feature", wtDir)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	commitInWorktree(t, gotPath, "feature work")

	state, base := git.BranchMergeStatus(repo, "feature")
	if state != git.MergeNotMerged {
		t.Errorf("state = %v, want MergeNotMerged", state)
	}
	if base != "main" {
		t.Errorf("base = %q, want main", base)
	}
}

// TestBranchMergeStatus_MasterBase verifies that the default branch probe falls
// back to "master" when "main" does not exist.
func TestBranchMergeStatus_MasterBase(t *testing.T) {
	repo := initRepoOn(t, "master")

	wtDir := filepath.Join(t.TempDir(), "feature-wt")
	if _, err := git.AddWorktree(repo, "feature", wtDir); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	state, base := git.BranchMergeStatus(repo, "feature")
	if state != git.MergeMerged {
		t.Errorf("state = %v, want MergeMerged", state)
	}
	if base != "master" {
		t.Errorf("base = %q, want master", base)
	}
}

// TestBranchMergeStatus_EmptyBranchIsUnknown verifies that an empty branch name
// (e.g. detached HEAD) yields MergeUnknown with no base.
func TestBranchMergeStatus_EmptyBranchIsUnknown(t *testing.T) {
	repo := initRepoOn(t, "main")

	state, base := git.BranchMergeStatus(repo, "")
	if state != git.MergeUnknown {
		t.Errorf("state = %v, want MergeUnknown", state)
	}
	if base != "" {
		t.Errorf("base = %q, want empty", base)
	}
}
