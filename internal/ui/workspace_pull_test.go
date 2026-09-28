package ui

import (
	"errors"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// execFor runs cmd — unwrapping a tea.BatchMsg one level — and returns the
// first message of type T. Tests set spinnerActive so no blocking spinner
// tick is part of the batch.
func execFor[T any](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	var zero T
	if cmd == nil {
		t.Fatalf("expected a %T-producing cmd, got nil", zero)
	}
	msg := cmd()
	if got, ok := msg.(T); ok {
		return got
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if got, ok := c().(T); ok {
				return got
			}
		}
	}
	t.Fatalf("no %T produced; got %T", zero, msg)
	return zero
}

func sortedPullCalls(g *fakeGitOps) []pullCall {
	calls := append([]pullCall(nil), g.pullCalls...)
	sort.Slice(calls, func(i, j int) bool { return calls[i].worktreePath < calls[j].worktreePath })
	return calls
}

func TestWorkspacePull_HeaderPullsEachMemberBaseCheckoutOnItsCurrentBranch(t *testing.T) {
	gitOp := &fakeGitOps{
		pullResult:    "Already up to date.",
		currentBranch: map[string]string{"/repo/api": "main", "/repo/web": "develop"},
	}
	ws := wsMemberWorkspace("payments", "/repo/api", "/repo/web")
	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		spinnerActive: true, gitOp: gitOp,
		wsStatuses: []workspace.WorkspaceStatus{ws},
	}

	updated, cmd := m.Update(keyMsg("P"))
	m2 := updated.(model)
	if !m2.wsPullPending(wsTarget{workspace: "payments"}) {
		t.Fatal("header pull must be marked pending while in flight")
	}
	done := execFor[wsPullFinishedMsg](t, cmd)

	want := []pullCall{{worktreePath: "/repo/api", branch: "main"}, {worktreePath: "/repo/web", branch: "develop"}}
	got := sortedPullCalls(gitOp)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pull calls = %+v, want %+v", got, want)
	}

	updated3, _ := m2.Update(done)
	m3 := updated3.(model)
	if m3.wsPullPending(wsTarget{workspace: "payments"}) {
		t.Error("finished pull must clear the pending marker")
	}
	if !strings.Contains(m3.wsHint, "api: Already up to date.") || !strings.Contains(m3.wsHint, "web: Already up to date.") {
		t.Errorf("hint must report each member; got %q", m3.wsHint)
	}
}

func TestWorkspacePull_HeaderSkipsDetachedAndMissingMembers(t *testing.T) {
	gitOp := &fakeGitOps{currentBranch: map[string]string{"/repo/api": ""}}
	ws := wsMemberWorkspace("payments", "/repo/api", "/repo/gone")
	ws.Workspace.Members[1].Missing = true
	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		spinnerActive: true, gitOp: gitOp,
		wsStatuses: []workspace.WorkspaceStatus{ws},
	}

	_, cmd := m.Update(keyMsg("P"))
	done := execFor[wsPullFinishedMsg](t, cmd)

	if len(gitOp.pullCalls) != 0 {
		t.Errorf("detached HEAD must not be pulled; calls = %+v", gitOp.pullCalls)
	}
	if len(done.outcomes) != 1 || done.outcomes[0].skip != "detached HEAD" {
		t.Errorf("outcomes = %+v, want one detached-HEAD skip (missing member excluded)", done.outcomes)
	}
}

func TestWorkspacePull_SessionPullsSessionBranchOnlyWhereItWasPushed(t *testing.T) {
	gitOp := &fakeGitOps{
		pullResult:     "Updating a..b",
		remoteBranches: map[string]bool{"/repo/api": true},
		pullErrs:       map[string]error{},
	}
	sess := makeSessionStatus("feature-x", "feature-x", settings.StateStopped, "/repo/api", "/repo/web")
	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		spinnerActive: true, gitOp: gitOp,
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", sess)},
		wsCursor:   1,
	}

	updated, cmd := m.Update(keyMsg("P"))
	done := execFor[wsPullFinishedMsg](t, cmd)

	if len(gitOp.pullCalls) != 1 || gitOp.pullCalls[0] != (pullCall{worktreePath: "/repo/api/feature-x", branch: "feature-x"}) {
		t.Fatalf("pull calls = %+v, want only the pushed member's worktree on the session branch", gitOp.pullCalls)
	}
	updated2, _ := updated.(model).Update(done)
	hint := updated2.(model).wsHint
	for _, want := range []string{"payments/feature-x", "api: Updating a..b", "web: no upstream"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q missing %q", hint, want)
		}
	}
}

func TestWorkspacePull_FailedMemberReportedWithoutHidingOthers(t *testing.T) {
	gitOp := &fakeGitOps{
		pullResult:     "Already up to date.",
		remoteBranches: map[string]bool{"/repo/api": true, "/repo/web": true},
		pullErrs:       map[string]error{"/repo/web/feature-x": errors.New("git pull: Not possible to fast-forward, aborting.\nhint: diverged")},
	}
	sess := makeSessionStatus("feature-x", "feature-x", settings.StateRunning, "/repo/api", "/repo/web")
	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		spinnerActive: true, gitOp: gitOp,
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", sess)},
		wsCursor:   1,
	}

	updated, cmd := m.Update(keyMsg("P"))
	updated2, _ := updated.(model).Update(execFor[wsPullFinishedMsg](t, cmd))
	hint := updated2.(model).wsHint
	if !strings.Contains(hint, "api: Already up to date.") {
		t.Errorf("hint %q must keep the successful member", hint)
	}
	if !strings.Contains(hint, "web: failed (git pull: Not possible to fast-forward, aborting.)") {
		t.Errorf("hint %q must report the failed member's first error line", hint)
	}
}

func TestWorkspacePull_RepeatedPWhilePendingIsIgnored(t *testing.T) {
	sess := makeSessionStatus("feature-x", "feature-x", settings.StateStopped, "/repo/api")
	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		spinnerActive: true, gitOp: &fakeGitOps{},
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", sess)},
		wsCursor:   1,
		wsPulling:  map[wsTarget]struct{}{{workspace: "payments", session: "feature-x"}: {}},
	}

	_, cmd := m.Update(keyMsg("P"))
	if cmd != nil {
		t.Error("a second P on a session already pulling must not dispatch another pull")
	}
}

func TestWorkspacePull_RefusesUnpullableSessions(t *testing.T) {
	for _, st := range []settings.RowState{settings.StateMissing, settings.StateCreating} {
		sess := makeSessionStatus("feature-x", "feature-x", st, "/repo/api")
		m := model{
			width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
			gitOp:      &fakeGitOps{},
			wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", sess)},
			wsCursor:   1,
		}
		updated, cmd := m.Update(keyMsg("P"))
		if cmd != nil {
			t.Errorf("state %v: P must not dispatch a pull", st)
		}
		if updated.(model).wsHint == "" {
			t.Errorf("state %v: P must explain why it cannot pull", st)
		}
	}
}

func TestWorkspacePull_PendingRowsRenderPulling(t *testing.T) {
	sess := makeSessionStatus("feature-x", "feature-x", settings.StateStopped, "/repo/api")
	m := model{
		width: 120, height: 40, view: viewWorkspaces,
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", sess)},
		wsPulling: map[wsTarget]struct{}{
			{workspace: "payments"}:                       {},
			{workspace: "payments", session: "feature-x"}: {},
		},
	}
	out := m.View()
	if strings.Count(out, "pulling") != 2 {
		t.Errorf("header and session row must both render pulling; got:\n%s", out)
	}
}
