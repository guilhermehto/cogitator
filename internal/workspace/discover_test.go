package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// mkRepo creates dir (and parents) and marks it as a git repo by creating a
// ".git" directory inside it.
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkRepo %s: %v", dir, err)
	}
}

// mkDir creates a plain (non-repo) directory.
func mkDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkDir %s: %v", dir, err)
	}
}

func canon(t *testing.T, p string) string {
	t.Helper()
	c, err := pathnorm.Canonical(p)
	if err != nil {
		t.Fatalf("canonical %s: %v", p, err)
	}
	return c
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestDiscoverRepos_FindsReposAndSkipsPlainDirs(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "src", "alpha"))
	mkRepo(t, filepath.Join(root, "work", "beta"))
	mkDir(t, filepath.Join(root, "src", "notarepo"))

	got, err := workspace.DiscoverRepos(root)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}

	for _, want := range []string{
		canon(t, filepath.Join(root, "src", "alpha")),
		canon(t, filepath.Join(root, "work", "beta")),
	} {
		if !contains(got, want) {
			t.Errorf("expected %q in results; got %v", want, got)
		}
	}
	if bad := canon(t, filepath.Join(root, "src", "notarepo")); contains(got, bad) {
		t.Errorf("plain dir %q must not be reported as a repo", bad)
	}
}

func TestDiscoverRepos_DoesNotDescendIntoRepos(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	mkRepo(t, outer)
	// A nested repo inside outer's working tree must not be reported: once
	// outer is identified as a repo, discovery stops descending.
	mkRepo(t, filepath.Join(outer, "nested"))

	got, err := workspace.DiscoverRepos(root)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}
	if !contains(got, canon(t, outer)) {
		t.Errorf("expected outer repo in results; got %v", got)
	}
	if nested := canon(t, filepath.Join(outer, "nested")); contains(got, nested) {
		t.Errorf("nested repo %q must be pruned once outer is a repo", nested)
	}
}

func TestDiscoverRepos_SkipsNoiseAndHiddenDirs(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "node_modules", "pkg"))
	mkRepo(t, filepath.Join(root, ".cache", "thing"))
	mkRepo(t, filepath.Join(root, "keep"))

	got, err := workspace.DiscoverRepos(root)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}
	if !contains(got, canon(t, filepath.Join(root, "keep"))) {
		t.Errorf("expected visible repo 'keep'; got %v", got)
	}
	if r := canon(t, filepath.Join(root, "node_modules", "pkg")); contains(got, r) {
		t.Errorf("repo under node_modules must be skipped; got %v", got)
	}
	if r := canon(t, filepath.Join(root, ".cache", "thing")); contains(got, r) {
		t.Errorf("repo under hidden dir must be skipped; got %v", got)
	}
}

func TestDiscoverRepos_FindsHiddenRepo(t *testing.T) {
	root := t.TempDir()
	// A hidden directory that is itself a repo (e.g. ~/.dotfiles) must be
	// discovered, even though hidden non-repo dirs are not descended into.
	mkRepo(t, filepath.Join(root, ".dotfiles"))
	// A repo nested one level under a hidden, non-repo dir stays skipped: we
	// report hidden repos but do not crawl hidden trees.
	mkRepo(t, filepath.Join(root, ".config", "buried"))

	got, err := workspace.DiscoverRepos(root)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}
	if !contains(got, canon(t, filepath.Join(root, ".dotfiles"))) {
		t.Errorf("expected hidden repo '.dotfiles'; got %v", got)
	}
	if r := canon(t, filepath.Join(root, ".config", "buried")); contains(got, r) {
		t.Errorf("repo nested under hidden dir must be skipped; got %v", got)
	}
}

func TestDiscoverRepos_RespectsDepthCap(t *testing.T) {
	root := t.TempDir()
	// Build a path deeper than the cap (7 levels of nesting) with a repo at
	// the bottom; it must not be discovered.
	deep := root
	for i := 0; i < 8; i++ {
		deep = filepath.Join(deep, "d")
	}
	mkRepo(t, deep)
	// A shallow repo for contrast.
	mkRepo(t, filepath.Join(root, "shallow"))

	got, err := workspace.DiscoverRepos(root)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}
	if !contains(got, canon(t, filepath.Join(root, "shallow"))) {
		t.Errorf("expected shallow repo; got %v", got)
	}
	if contains(got, canon(t, deep)) {
		t.Errorf("repo below depth cap must not be discovered; got %v", got)
	}
}

func TestDiscoverRepos_SortedAndDeduped(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "zeta"))
	mkRepo(t, filepath.Join(root, "alpha"))

	got, err := workspace.DiscoverRepos(root)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(got), got)
	}
	if got[0] >= got[1] {
		t.Errorf("results must be sorted ascending; got %v", got)
	}
}

func TestDiscoverRepos_MissingRootReturnsError(t *testing.T) {
	_, err := workspace.DiscoverRepos(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Errorf("expected error for missing root")
	}
}
