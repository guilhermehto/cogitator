package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/guilhermehto/cogitator/internal/git"
)

func runIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRemoteBranchExists_OnlyForBranchesKnownOnOrigin(t *testing.T) {
	remote := initRepo(t)
	local := cloneRepo(t, remote)
	runIn(t, local, "branch", "local-only")

	if !git.RemoteBranchExists(local, "main") {
		t.Error("main was cloned from origin; want RemoteBranchExists true")
	}
	if git.RemoteBranchExists(local, "local-only") {
		t.Error("local-only was never pushed; want RemoteBranchExists false")
	}
}

func TestCurrentBranch_ReturnsCheckedOutBranch(t *testing.T) {
	repo := initRepo(t)

	got, err := git.CurrentBranch(repo)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("CurrentBranch = %q, want main", got)
	}
}

func TestCurrentBranch_DetachedHeadIsEmpty(t *testing.T) {
	repo := initRepo(t)
	runIn(t, repo, "checkout", "--detach")

	got, err := git.CurrentBranch(repo)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if got != "" {
		t.Errorf("CurrentBranch on detached HEAD = %q, want empty", got)
	}
}

func TestIsDirty(t *testing.T) {
	repo := initRepo(t)
	commitFile(t, repo, "tracked.txt", "one\n", "add tracked")

	clean, err := git.IsDirty(repo)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if clean {
		t.Fatal("freshly committed repo reported dirty")
	}

	cases := map[string]func(dir string){
		"untracked file": func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("wip"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"modified tracked file": func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("two\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, dirty := range cases {
		t.Run(name, func(t *testing.T) {
			wt := filepath.Join(t.TempDir(), "wt")
			if _, err := git.AddWorktree(repo, "b-"+filepath.Base(t.Name()), wt); err != nil {
				t.Fatalf("AddWorktree: %v", err)
			}
			dirty(wt)
			got, err := git.IsDirty(wt)
			if err != nil {
				t.Fatalf("IsDirty: %v", err)
			}
			if !got {
				t.Error("want dirty")
			}
		})
	}
}
