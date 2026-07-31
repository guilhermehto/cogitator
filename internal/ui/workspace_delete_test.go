package ui

// workspace_delete_test.go — tests for step 13's Workspaces-view deletion
// flow: 'D' on a session row or a workspace header/hint row opens a two-step
// confirm annotated with each member repo's branch merge status, then tears
// down worktrees/branches/directory/tmux target via deleteWsSessionCmd or
// deleteWorkspaceCmd.
//
// Teardown itself (workspace.TeardownSession) shells out to the real git
// binary with no injectable seam, exactly like AssembleSession
// (workspace_cmd_test.go). Rather than spinning up real git fixture repos for
// every case, most tests here use a Session whose Members are either empty
// (TeardownSession's only real work is os.RemoveAll on a plain temp
// directory — success, no git involved) or point at a plain, non-git
// directory (a real, deterministic `git worktree remove` failure whose error
// text names RepoPath verbatim, exactly what the "names that repo" done-when
// bullet needs) — enough to exercise the store bookkeeping and tmux cleanup
// this step owns without needing a real repo fixture.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// ---------------------------------------------------------------------------
// fakeDeleteStoreOps
// ---------------------------------------------------------------------------

type removeSessionCall struct {
	workspaceName string
	sessionName   string
}

// fakeDeleteStoreOps is a minimal in-memory storeOps for this file's tests.
// Distinct from workspace_cmd_test.go's fakeStoreOps: that type's
// RemoveSession/RemoveWorkspace are hard-coded no-ops with no call recording
// or configurable error, which this file's "not dropped from the store on
// failure" assertions need.
type fakeDeleteStoreOps struct {
	workspaces []workspace.Workspace
	loadErr    error

	removeSessionErr   error
	removeWorkspaceErr error

	removeSessionCalls   []removeSessionCall
	removeWorkspaceCalls []string
}

func (f *fakeDeleteStoreOps) LoadWorkspaces() ([]workspace.Workspace, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.workspaces, nil
}

func (f *fakeDeleteStoreOps) SaveWorkspaces(workspaces []workspace.Workspace) error {
	f.workspaces = workspaces
	return nil
}

func (f *fakeDeleteStoreOps) AddWorkspace(name string) (workspace.Workspace, error) {
	return workspace.Workspace{}, fmt.Errorf("fakeDeleteStoreOps: AddWorkspace not supported")
}

func (f *fakeDeleteStoreOps) RemoveWorkspace(name string) error {
	f.removeWorkspaceCalls = append(f.removeWorkspaceCalls, name)
	return f.removeWorkspaceErr
}

func (f *fakeDeleteStoreOps) AddSession(workspaceName string, session workspace.Session) error {
	return fmt.Errorf("fakeDeleteStoreOps: AddSession not supported")
}

func (f *fakeDeleteStoreOps) RemoveSession(workspaceName, sessionName string) error {
	f.removeSessionCalls = append(f.removeSessionCalls, removeSessionCall{workspaceName, sessionName})
	return f.removeSessionErr
}

func (f *fakeDeleteStoreOps) AttachRepo(workspaceName, repoPath string) error { return nil }
func (f *fakeDeleteStoreOps) DetachRepo(workspaceName, repoPath string) error { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// nonGitDir creates a plain (non-git) directory inside t.TempDir(), used as a
// SessionMember.RepoPath that deterministically fails `git worktree remove`
// with an error naming the path itself — the cheapest way to exercise
// TeardownSession's failure path without a real git fixture repo.
func nonGitDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "not-a-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

// ---------------------------------------------------------------------------
// 'D' — resolving what the cursor targets
// ---------------------------------------------------------------------------

func TestWorkspaceDelete_DOnSessionRowOpensSessionConfirm(t *testing.T) {
	sess := workspace.Session{
		Name: "Feature X", Dir: "/ws/payments/feature-x", Branch: "feature-x",
		Members: []workspace.SessionMember{
			{RepoPath: "/repo/a", WorktreePath: "/ws/payments/feature-x/a"},
			{RepoPath: "/repo/b", WorktreePath: "/ws/payments/feature-x/b"},
		},
	}
	m := model{
		width: 120, height: 40, view: viewWorkspaces,
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", workspace.SessionStatus{Session: sess})},
		wsCursor:   1, // header=0, session=1
		gitOp:      &fakeGitOps{mergeState: git.MergeNotMerged, mergeBase: "main"},
	}

	updated, cmd := m.Update(keyMsg("D"))
	m2 := updated.(model)

	if m2.prompt != promptConfirmDeleteWsSession {
		t.Fatalf("D on a session row must open promptConfirmDeleteWsSession, got %v", m2.prompt)
	}
	if m2.wsDeleteWorkspace != "payments" || m2.wsDeleteSession != "Feature X" {
		t.Errorf("unexpected delete target: workspace=%q session=%q", m2.wsDeleteWorkspace, m2.wsDeleteSession)
	}
	if len(m2.wsDeleteMembers) != 2 {
		t.Fatalf("expected 2 members captured, got %d: %+v", len(m2.wsDeleteMembers), m2.wsDeleteMembers)
	}
	if cmd == nil {
		t.Fatal("opening the confirm must dispatch the merge-status probes")
	}
}

func TestWorkspaceDelete_DOnWorkspaceHeaderOpensWorkspaceConfirm(t *testing.T) {
	sess := workspace.Session{Name: "Feature X", Dir: "/ws/payments/feature-x", Branch: "feature-x"}
	m := model{
		width: 120, height: 40, view: viewWorkspaces,
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", workspace.SessionStatus{Session: sess})},
		wsCursor:   0, // the workspace's header line
	}

	updated, _ := m.Update(keyMsg("D"))
	m2 := updated.(model)

	if m2.prompt != promptConfirmDeleteWorkspace {
		t.Fatalf("D on a workspace header must open promptConfirmDeleteWorkspace, got %v", m2.prompt)
	}
	if m2.wsDeleteWorkspace != "payments" || m2.wsDeleteSession != "" {
		t.Errorf("unexpected delete target: workspace=%q session=%q", m2.wsDeleteWorkspace, m2.wsDeleteSession)
	}
	if len(m2.wsDeleteMembers) != 0 {
		t.Errorf("session-less workspace must capture no members, got %+v", m2.wsDeleteMembers)
	}
}

func TestWorkspaceDelete_DWithNoWorkspacesIsNoop(t *testing.T) {
	m := model{width: 120, height: 40, view: viewWorkspaces, input: newTestInput()}

	updated, cmd := m.Update(keyMsg("D"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("D with no workspaces must stay idle, got %v", m2.prompt)
	}
	if cmd != nil {
		t.Error("D with no workspaces must return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// Merge-status probes: "checking…" until they land, guarded against stale
// results.
// ---------------------------------------------------------------------------

func TestWorkspaceDelete_MergeStatusFillsInAsProbesReturn(t *testing.T) {
	sess := workspace.Session{
		Name: "Feature X", Dir: "/ws/payments/feature-x", Branch: "feature-x",
		Members: []workspace.SessionMember{
			{RepoPath: "/repo/a", WorktreePath: "/ws/payments/feature-x/a"},
			{RepoPath: "/repo/b", WorktreePath: "/ws/payments/feature-x/b"},
		},
	}
	m := model{
		width: 120, height: 40, view: viewWorkspaces,
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", workspace.SessionStatus{Session: sess})},
		wsCursor:   1,
	}
	updated, _ := m.Update(keyMsg("D"))
	m2 := updated.(model)

	before := m2.renderWsDeleteConfirm()
	if got := strings.Count(before, "checking merge status…"); got != 2 {
		t.Fatalf("both members must render checking… before any probe returns (got %d):\n%s", got, before)
	}

	updated2, _ := m2.Update(mergeStatusMsg{path: "/ws/payments/feature-x/a", state: git.MergeMerged, base: "main"})
	m3 := updated2.(model)

	if got := m3.wsDeleteMergeInfo["/ws/payments/feature-x/a"]; got != "merged into main" {
		t.Errorf("merge info for member a = %q, want %q", got, "merged into main")
	}
	after := m3.renderWsDeleteConfirm()
	if got := strings.Count(after, "checking merge status…"); got != 1 {
		t.Errorf("only the unresolved member must still show checking… (got %d):\n%s", got, after)
	}
	if !strings.Contains(after, "merged into main") {
		t.Errorf("resolved member's status must render, got:\n%s", after)
	}

	// A probe result for a path not among the current members (e.g. a stale
	// result delivered after cancel/retarget) must be dropped.
	updated3, _ := m3.Update(mergeStatusMsg{path: "/stale/path", state: git.MergeMerged, base: "main"})
	m4 := updated3.(model)
	if _, ok := m4.wsDeleteMergeInfo["/stale/path"]; ok {
		t.Error("a stale probe result must not be recorded")
	}
}

// ---------------------------------------------------------------------------
// First confirm: any key other than 'y' cancels.
// ---------------------------------------------------------------------------

func TestWorkspaceDelete_FirstConfirmNonYCancels(t *testing.T) {
	cases := []promptMode{promptConfirmDeleteWsSession, promptConfirmDeleteWorkspace}
	for _, start := range cases {
		m := model{
			width: 120, prompt: start,
			wsDeleteWorkspace: "payments", wsDeleteSession: "Feature X",
			wsDeleteMembers:   []wsDeleteMember{{session: "Feature X", repoPath: "/repo/a", worktreePath: "/wt/a", branch: "feature-x"}},
			wsDeleteMergeInfo: map[string]string{},
		}

		updated, cmd := m.Update(keyMsg("n"))
		m2 := updated.(model)

		if m2.prompt != promptIdle {
			t.Errorf("prompt %v: non-y must cancel to promptIdle, got %v", start, m2.prompt)
		}
		if m2.wsDeleteWorkspace != "" || m2.wsDeleteSession != "" || m2.wsDeleteMembers != nil || m2.wsDeleteMergeInfo != nil {
			t.Errorf("prompt %v: cancel must clear the delete target, got %+v", start, m2)
		}
		if cmd != nil {
			t.Errorf("prompt %v: cancel must return nil cmd", start)
		}
	}
}

func TestWorkspaceDelete_FirstConfirmYAdvancesToSecond(t *testing.T) {
	cases := []struct {
		start promptMode
		want  promptMode
	}{
		{promptConfirmDeleteWsSession, promptConfirmDeleteWsSession2},
		{promptConfirmDeleteWorkspace, promptConfirmDeleteWorkspace2},
	}
	for _, c := range cases {
		m := model{width: 120, prompt: c.start, wsDeleteWorkspace: "payments"}

		updated, cmd := m.Update(keyMsg("y"))
		m2 := updated.(model)

		if m2.prompt != c.want {
			t.Errorf("prompt %v: y must advance to %v, got %v", c.start, c.want, m2.prompt)
		}
		if cmd != nil {
			t.Errorf("prompt %v: advancing to the second confirm must not dispatch anything yet", c.start)
		}
	}
}

// ---------------------------------------------------------------------------
// Second confirm: default is cancel; only 'y' dispatches.
// ---------------------------------------------------------------------------

func TestWorkspaceDelete_SecondConfirmDefaultCancels(t *testing.T) {
	for _, prompt := range []promptMode{promptConfirmDeleteWsSession2, promptConfirmDeleteWorkspace2} {
		for _, key := range []string{"esc", "enter", "n", "N"} {
			m := model{
				width: 120, prompt: prompt,
				wsDeleteWorkspace: "payments", wsDeleteSession: "Feature X",
			}
			updated, cmd := m.Update(keyMsg(key))
			m2 := updated.(model)

			if m2.prompt != promptIdle {
				t.Errorf("prompt %v key %q: must cancel to promptIdle, got %v", prompt, key, m2.prompt)
			}
			if cmd != nil {
				t.Errorf("prompt %v key %q: must not dispatch a delete", prompt, key)
			}
		}
	}
}

func TestWorkspaceDelete_SecondConfirmYDispatchesDeleteWsSessionCmd(t *testing.T) {
	store := &fakeDeleteStoreOps{loadErr: errors.New("boom")}
	m := model{
		width: 120, prompt: promptConfirmDeleteWsSession2,
		wsDeleteWorkspace: "payments", wsDeleteSession: "Feature X",
		store: store,
	}

	updated, cmd := m.Update(keyMsg("y"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("y must return to promptIdle, got %v", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("y must dispatch the delete cmd")
	}

	result, ok := runCmd(cmd).(wsSessionDeletedMsg)
	if !ok {
		t.Fatalf("expected wsSessionDeletedMsg, got %T", runCmd(cmd))
	}
	if result.workspaceName != "payments" || result.sessionName != "Feature X" {
		t.Errorf("unexpected result target: %+v", result)
	}
	if result.err == nil {
		t.Fatal("expected the configured load error to surface")
	}
}

func TestWorkspaceDelete_SecondConfirmYDispatchesDeleteWorkspaceCmd(t *testing.T) {
	store := &fakeDeleteStoreOps{loadErr: errors.New("boom")}
	m := model{
		width: 120, prompt: promptConfirmDeleteWorkspace2,
		wsDeleteWorkspace: "payments",
		store:             store,
	}

	updated, cmd := m.Update(keyMsg("y"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("y must return to promptIdle, got %v", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("y must dispatch the delete cmd")
	}

	result, ok := runCmd(cmd).(wsWorkspaceDeletedMsg)
	if !ok {
		t.Fatalf("expected wsWorkspaceDeletedMsg, got %T", runCmd(cmd))
	}
	if result.workspaceName != "payments" {
		t.Errorf("unexpected result target: %+v", result)
	}
	if result.err == nil {
		t.Fatal("expected the configured load error to surface")
	}
}

// ---------------------------------------------------------------------------
// deleteWsSessionCmd — teardown, tmux cleanup, store bookkeeping
// ---------------------------------------------------------------------------

func TestWorkspaceDelete_DeleteWsSessionCmd_SuccessRemovesSessionFromStore(t *testing.T) {
	dir := t.TempDir()
	session := workspace.Session{Name: "Feature X", Dir: dir, Branch: "feature-x"}
	store := &fakeDeleteStoreOps{workspaces: []workspace.Workspace{
		{Name: "payments", Sessions: []workspace.Session{session}},
	}}
	tmuxFake := &fakeTmuxOps{available: true, findWindowErr: errors.New("no window")}

	msg := runCmd(deleteWsSessionCmd(store, tmuxFake, "payments", "Feature X", tmuxctl.ModeWindow))
	result, ok := msg.(wsSessionDeletedMsg)
	if !ok {
		t.Fatalf("expected wsSessionDeletedMsg, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if len(store.removeSessionCalls) != 1 || store.removeSessionCalls[0] != (removeSessionCall{"payments", "Feature X"}) {
		t.Errorf("RemoveSession must be called with (payments, Feature X), got %v", store.removeSessionCalls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("session directory must be removed, got err=%v", err)
	}
	if len(tmuxFake.findWindowCalls) != 1 || tmuxFake.findWindowCalls[0] != dir {
		t.Errorf("must look for a tmux target attached to the session directory, got %v", tmuxFake.findWindowCalls)
	}
}

func TestWorkspaceDelete_DeleteWsSessionCmd_TeardownFailureKeepsSessionInStore(t *testing.T) {
	sessionDir := t.TempDir()
	badRepo := nonGitDir(t)
	session := workspace.Session{
		Name: "Feature X", Dir: sessionDir, Branch: "feature-x",
		Members: []workspace.SessionMember{
			{RepoPath: badRepo, WorktreePath: filepath.Join(sessionDir, "not-a-repo")},
		},
	}
	store := &fakeDeleteStoreOps{workspaces: []workspace.Workspace{
		{Name: "payments", Sessions: []workspace.Session{session}},
	}}
	tmuxFake := &fakeTmuxOps{available: true}

	msg := runCmd(deleteWsSessionCmd(store, tmuxFake, "payments", "Feature X", tmuxctl.ModeWindow))
	result, ok := msg.(wsSessionDeletedMsg)
	if !ok {
		t.Fatalf("expected wsSessionDeletedMsg, got %T", msg)
	}
	if result.err == nil {
		t.Fatal("expected a teardown failure naming the bad repo")
	}
	if !strings.Contains(result.err.Error(), badRepo) {
		t.Errorf("error must name the failing repo %q, got %q", badRepo, result.err.Error())
	}
	if len(store.removeSessionCalls) != 0 {
		t.Errorf("a failed teardown must not drop the session from the store, got %v", store.removeSessionCalls)
	}
	if len(tmuxFake.findWindowCalls) != 1 {
		t.Error("the tmux target must still be looked up even when teardown failed")
	}
	if len(tmuxFake.killWindowCalls) != 1 {
		t.Error("the tmux target must still be closed (ModeWindow) even when teardown failed")
	}
}

// ---------------------------------------------------------------------------
// deleteWorkspaceCmd — every session torn down; workspace removed only on
// full success.
// ---------------------------------------------------------------------------

func TestWorkspaceDelete_DeleteWorkspaceCmd_SuccessRemovesWorkspace(t *testing.T) {
	dirX, dirY := t.TempDir(), t.TempDir()
	store := &fakeDeleteStoreOps{workspaces: []workspace.Workspace{{
		Name: "payments",
		Sessions: []workspace.Session{
			{Name: "Feature X", Dir: dirX, Branch: "feature-x"},
			{Name: "Feature Y", Dir: dirY, Branch: "feature-y"},
		},
	}}}
	tmuxFake := &fakeTmuxOps{available: true, findWindowErr: errors.New("no window")}

	msg := runCmd(deleteWorkspaceCmd(store, tmuxFake, "payments", tmuxctl.ModeSession))
	result, ok := msg.(wsWorkspaceDeletedMsg)
	if !ok {
		t.Fatalf("expected wsWorkspaceDeletedMsg, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if len(store.removeWorkspaceCalls) != 1 || store.removeWorkspaceCalls[0] != "payments" {
		t.Errorf("RemoveWorkspace must be called with \"payments\", got %v", store.removeWorkspaceCalls)
	}
	for _, dir := range []string{dirX, dirY} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("session directory %q must be removed, got err=%v", dir, err)
		}
	}
}

func TestWorkspaceDelete_DeleteWorkspaceCmd_PartialFailureKeepsWorkspaceAndAllSessions(t *testing.T) {
	goodDir := t.TempDir()
	goodSession := workspace.Session{Name: "Feature Y", Dir: goodDir, Branch: "feature-y"}

	badSessionDir := t.TempDir()
	badRepo := nonGitDir(t)
	badSession := workspace.Session{
		Name: "Feature X", Dir: badSessionDir, Branch: "feature-x",
		Members: []workspace.SessionMember{
			{RepoPath: badRepo, WorktreePath: filepath.Join(badSessionDir, "not-a-repo")},
		},
	}

	store := &fakeDeleteStoreOps{workspaces: []workspace.Workspace{{
		Name:     "payments",
		Sessions: []workspace.Session{goodSession, badSession},
	}}}
	tmuxFake := &fakeTmuxOps{available: true}

	msg := runCmd(deleteWorkspaceCmd(store, tmuxFake, "payments", tmuxctl.ModeWindow))
	result, ok := msg.(wsWorkspaceDeletedMsg)
	if !ok {
		t.Fatalf("expected wsWorkspaceDeletedMsg, got %T", msg)
	}
	if result.err == nil {
		t.Fatal("expected an error naming the failing session and repo")
	}
	if !strings.Contains(result.err.Error(), "Feature X") || !strings.Contains(result.err.Error(), badRepo) {
		t.Errorf("error must name the failing session and repo, got %q", result.err.Error())
	}
	if len(store.removeWorkspaceCalls) != 0 {
		t.Error("a partial failure must not remove the workspace")
	}
	// Best-effort teardown still ran per session: the session that succeeded
	// had its directory removed even though the workspace record — and the
	// failing session's own record — both stay in the store.
	if _, err := os.Stat(goodDir); !os.IsNotExist(err) {
		t.Errorf("the succeeding session's directory must still be torn down, got err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// wsSessionDeletedMsg / wsWorkspaceDeletedMsg — Update handling
// ---------------------------------------------------------------------------

func TestWorkspaceDelete_SessionDeletedMsgSuccessDispatchesReload(t *testing.T) {
	m := model{width: 120}

	updated, cmd := m.Update(wsSessionDeletedMsg{workspaceName: "payments", sessionName: "Feature X"})
	m2 := updated.(model)

	if !m2.wsBuilding {
		t.Error("success must dispatch a reload (wsBuilding true)")
	}
	if cmd == nil {
		t.Fatal("success must return a reload cmd")
	}
	if m2.wsHint != "" {
		t.Errorf("success must clear wsHint, got %q", m2.wsHint)
	}
}

func TestWorkspaceDelete_SessionDeletedMsgFailureSetsHint(t *testing.T) {
	m := model{width: 120}

	updated, cmd := m.Update(wsSessionDeletedMsg{
		workspaceName: "payments", sessionName: "Feature X",
		err: fmt.Errorf("/repo/b: locked working tree"),
	})
	m2 := updated.(model)

	if m2.wsBuilding {
		t.Error("failure must not dispatch a reload")
	}
	if cmd != nil {
		t.Error("failure must return nil cmd")
	}
	if m2.wsHint == "" || !strings.Contains(m2.wsHint, "/repo/b") {
		t.Errorf("failure must surface a hint naming the failing repo, got %q", m2.wsHint)
	}
}

func TestWorkspaceDelete_WorkspaceDeletedMsgSuccessDispatchesReload(t *testing.T) {
	m := model{width: 120}

	updated, cmd := m.Update(wsWorkspaceDeletedMsg{workspaceName: "payments"})
	m2 := updated.(model)

	if !m2.wsBuilding {
		t.Error("success must dispatch a reload (wsBuilding true)")
	}
	if cmd == nil {
		t.Fatal("success must return a reload cmd")
	}
}

func TestWorkspaceDelete_WorkspaceDeletedMsgFailureSetsHint(t *testing.T) {
	m := model{width: 120}

	updated, cmd := m.Update(wsWorkspaceDeletedMsg{
		workspaceName: "payments",
		err:           fmt.Errorf(`session "Feature X": /repo/b: locked working tree`),
	})
	m2 := updated.(model)

	if m2.wsBuilding {
		t.Error("failure must not dispatch a reload")
	}
	if cmd != nil {
		t.Error("failure must return nil cmd")
	}
	if m2.wsHint == "" || !strings.Contains(m2.wsHint, "Feature X") {
		t.Errorf("failure must surface a hint naming the failing session, got %q", m2.wsHint)
	}
}
