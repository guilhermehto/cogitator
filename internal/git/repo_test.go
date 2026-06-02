package git_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// TestRepoRoot_RepoRootReturnsCanonical verifies that RepoRoot on a repo root
// returns its canonical path.
func TestRepoRoot_RepoRootReturnsCanonical(t *testing.T) {
	repo := initRepo(t)

	got, err := git.RepoRoot(repo)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	want, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", repo, err)
	}
	if got != want {
		t.Errorf("RepoRoot: got %q, want %q", got, want)
	}
}

// TestRepoRoot_SubdirResolvesToRoot verifies that selecting a directory inside
// a repo resolves to the repo root, not the subdirectory.
func TestRepoRoot_SubdirResolvesToRoot(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	got, err := git.RepoRoot(sub)
	if err != nil {
		t.Fatalf("RepoRoot(subdir): %v", err)
	}
	want, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", repo, err)
	}
	if got != want {
		t.Errorf("RepoRoot(subdir): got %q, want %q", got, want)
	}
}

// TestRepoRoot_NotARepo verifies that a plain directory (no git) errors.
func TestRepoRoot_NotARepo(t *testing.T) {
	dir := t.TempDir()

	if _, err := git.RepoRoot(dir); err == nil {
		t.Fatalf("RepoRoot on non-repo %q: expected error, got nil", dir)
	}
}
