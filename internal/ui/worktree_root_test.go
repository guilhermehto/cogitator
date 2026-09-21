package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

func TestNewWorktreeUsesConfiguredRoot(t *testing.T) {
	for _, fromRemote := range []bool{false, true} {
		name := "new"
		if fromRemote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := filepath.Join(t.TempDir(), "worktrees")
			if err := settings.SaveConfig(settings.Config{WorktreeRoot: root}); err != nil {
				t.Fatal(err)
			}
			want, err := pathnorm.Canonical(filepath.Join(root, "repo-fix", "auth"))
			if err != nil {
				t.Fatal(err)
			}
			gitFake := &fakeGitOps{addResult: want, fetchAddResult: want}
			tmuxFake := &fakeTmuxOps{available: true, ensureWindowResult: "main:1"}
			m := model{width: 120, gitOp: gitFake, tmux: tmuxFake, harnOp: &fakeHarnessOps{}, spinnerActive: true}
			updated, cmd := m.startNewWorktree("/src/repo", "fix/auth", "fake", fromRemote)
			if len(updated.workspaceRows) != 1 || updated.workspaceRows[0].Worktree != want {
				t.Fatalf("pending row does not use configured root: %+v", updated.workspaceRows)
			}
			if err := settings.SaveConfig(settings.Config{WorktreeRoot: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			result := worktreeCreatedFrom(t, cmd)
			if result.err != nil {
				t.Fatal(result.err)
			}
			calls := gitFake.addCalls
			if fromRemote {
				calls = gitFake.fetchAddCalls
			}
			if len(calls) != 1 || calls[0].dest != want {
				t.Fatalf("creation destination differs from pending row: %+v", calls)
			}
			if len(tmuxFake.ensureWindowCalls) != 1 || tmuxFake.ensureWindowCalls[0].dir != want {
				t.Fatalf("launch destination: %+v", tmuxFake.ensureWindowCalls)
			}
		})
	}
}

func TestNewWorktreeRejectsInvalidConfiguration(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		name := "nested root"
		if malformed {
			name = "malformed JSON"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := settings.SaveConfig(settings.Config{WorktreeRoot: repo}); err != nil {
				t.Fatal(err)
			}
			if malformed {
				path, err := settings.ConfigPath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			m := model{width: 120}
			updated, cmd := m.startNewWorktree(repo, "feat", "fake", false)
			if cmd != nil || len(updated.workspaceRows) != 0 {
				t.Fatal("invalid configuration must prevent creation")
			}
			if !strings.Contains(updated.tmuxHint, "config") && !strings.Contains(updated.tmuxHint, "worktreeRoot") {
				t.Fatalf("missing configuration error: %q", updated.tmuxHint)
			}
		})
	}
}

func TestWorktreeCreateAndDeleteOnDisk(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		customRoot, dirty, force bool
	}{
		{"default root", false, false, true},
		{"custom root with untracked files", true, true, true},
		{"protected dirty worktree", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setWsTestXDG(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			repo := newWsCmdTestRepo(t, "repo")
			cfg := settings.Config{ForceDeleteWorktree: &tc.force}
			root := filepath.Join(os.Getenv("XDG_DATA_HOME"), "cogitator", "worktrees")
			if tc.customRoot {
				root = filepath.Join(t.TempDir(), "custom")
				cfg.WorktreeRoot = root
			}
			if err := settings.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			want, err := pathnorm.Canonical(filepath.Join(root, "repo-fix", "auth"))
			if err != nil {
				t.Fatal(err)
			}
			tmuxFake := &fakeTmuxOps{available: true, ensureWindowResult: "main:1", findWindowErr: tmuxctl.ErrWindowNotFound}
			m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, nil)
			m.gitOp = realGitOps{}
			_, cmd := m.startNewWorktree(repo, "fix/auth", "fake", false)
			created := worktreeCreatedFrom(t, cmd)
			if created.err != nil {
				t.Fatal(created.err)
			}
			if created.canonDest != want {
				t.Fatalf("created %q, want %q", created.canonDest, want)
			}
			if _, err := os.Stat(filepath.Join(want, ".git")); err != nil {
				t.Fatalf("worktree missing: %v", err)
			}
			untracked := filepath.Join(want, "unsaved.txt")
			if tc.dirty {
				if err := os.WriteFile(untracked, []byte("unsaved"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			m.workspaceRows = []settings.Row{makeRow(repo, want, "fix/auth", "", settings.StateStopped, state.AttnInactive, fixedNow)}
			for _, key := range []string{"D", "y", "y"} {
				updated, next := m.Update(keyMsg(key))
				m = updated.(model)
				cmd = next
			}
			result, ok := runCmd(cmd).(worktreeDeletedMsg)
			if !ok {
				t.Fatal("expected deletion result")
			}
			if tc.dirty && !tc.force {
				if result.err == nil {
					t.Fatal("expected dirty worktree protection")
				}
				contents, err := os.ReadFile(untracked)
				if err != nil || string(contents) != "unsaved" {
					t.Fatalf("unsaved file changed: %q, %v", contents, err)
				}
				restored, _ := m.Update(result)
				if len(restored.(model).workspaceRows) != 1 {
					t.Fatal("failed deletion must restore row")
				}
				return
			}
			if result.err != nil {
				t.Fatal(result.err)
			}
			if _, err := os.Stat(want); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("worktree still exists or stat failed: %v", err)
			}
			worktrees, err := git.ListWorktrees(repo)
			if err != nil {
				t.Fatal(err)
			}
			if len(worktrees) != 1 || worktrees[0].Path != repo {
				t.Fatalf("unexpected remaining worktrees: %+v", worktrees)
			}
		})
	}
}
