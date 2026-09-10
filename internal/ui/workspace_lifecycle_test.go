package ui

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

type gatedWorkspaceStore struct {
	realStoreOps
	ready   chan struct{}
	release chan struct{}
	loaded  chan struct{}
}

func (s gatedWorkspaceStore) LoadWorkspaces() ([]workspace.Workspace, error) {
	workspaces, err := s.realStoreOps.LoadWorkspaces()
	s.loaded <- struct{}{}
	return workspaces, err
}

func (s gatedWorkspaceStore) AddSession(name string, session workspace.Session) error {
	close(s.ready)
	<-s.release
	return s.realStoreOps.AddSession(name, session)
}

func (s gatedWorkspaceStore) UpdateSessionMembers(workspaceName, sessionName string, mutate func(*workspace.Session) error) error {
	close(s.ready)
	<-s.release
	return s.realStoreOps.UpdateSessionMembers(workspaceName, sessionName, mutate)
}

func TestWorkspaceDeletionWaitsForMutationToPersist(t *testing.T) {
	for _, scenario := range []string{"create then delete workspace", "backfill then delete workspace", "backfill then delete session"} {
		t.Run(scenario, func(t *testing.T) {
			setWsTestXDG(t)
			repo := newWsCmdTestRepo(t, "app")
			store, err := workspace.NewStore()
			if err != nil {
				t.Fatal(err)
			}
			ws, err := store.AddWorkspace("payments")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AttachRepo(ws.Name, repo); err != nil {
				t.Fatal(err)
			}
			backfill := strings.HasPrefix(scenario, "backfill")
			if backfill {
				result := assembleWorkspaceSessionCmd(realStoreOps{store: store}, ws.Name, "feature", "opencode")().(wsSessionAssembledMsg)
				if result.err != nil {
					t.Fatal(result.err)
				}
				repo = newWsCmdTestRepo(t, "api")
			}
			gated := gatedWorkspaceStore{realStoreOps{store: store}, make(chan struct{}), make(chan struct{}), make(chan struct{}, 4)}
			var release sync.Once
			unblock := func() { release.Do(func() { close(gated.release) }) }
			t.Cleanup(unblock)
			mutation := assembleWorkspaceSessionCmd(gated, ws.Name, "feature", "opencode")
			if backfill {
				mutation = backfillMembershipCmd(gated, ws.Name, repo, true, []string{"feature"})
			}
			mutated := make(chan tea.Msg, 1)
			go func() { mutated <- mutation() }()
			select {
			case <-gated.ready:
			case <-time.After(5 * time.Second):
				t.Fatal("mutation did not reach persistence")
			}
			if !backfill {
				<-gated.loaded
			}
			deletion := deleteWorkspaceCmd(gated, nil, ws.Name, tmuxctl.ModeWindow)
			if strings.HasSuffix(scenario, "delete session") {
				deletion = deleteWsSessionCmd(gated, nil, ws.Name, "feature", tmuxctl.ModeWindow)
			}
			deleted := make(chan tea.Msg, 1)
			go func() { deleted <- deletion() }()
			select {
			case <-gated.loaded:
				t.Error("deletion snapshotted workspace before mutation persisted")
			case <-time.After(100 * time.Millisecond):
			}
			unblock()
			for _, result := range []tea.Msg{<-mutated, <-deleted} {
				switch result := result.(type) {
				case wsSessionAssembledMsg:
					err = result.err
				case wsBackfillAppliedMsg:
					if len(result.failures) > 0 {
						t.Errorf("backfill failed: %+v", result.failures)
					}
				case wsSessionDeletedMsg:
					err = result.err
				case wsWorkspaceDeletedMsg:
					err = result.err
				}
				if err != nil {
					t.Error(err)
				}
			}
			dir, err := workspace.SessionDir(mustResolveTestRoot(t), ws.Name, "feature")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("deleted session directory remains: %v", err)
			}
			remaining, err := store.LoadWorkspaces()
			if err != nil {
				t.Fatal(err)
			}
			for _, ws := range remaining {
				if len(ws.Sessions) != 0 {
					t.Errorf("deleted session remains in store: %+v", ws.Sessions)
				}
			}
		})
	}
}

func TestLaunchFailureVisibleInActiveView(t *testing.T) {
	for _, view := range []viewMode{viewSessions, viewWorkspaces} {
		m := model{width: 80, height: 24, view: view,
			workspaceRows: []settings.Row{{Repo: "/repo", Worktree: "/repo/main", Branch: "main"}},
		}
		updated, _ := m.Update(launchResultMsg{err: errors.New("tmux launch failed")})
		if !strings.Contains(updated.View(), "launch error: tmux launch failed") {
			t.Errorf("view %v hides launch failure", view)
		}
	}
}
