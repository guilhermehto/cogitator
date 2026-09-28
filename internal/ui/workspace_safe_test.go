package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

func twoMemberSession() workspace.Session {
	return workspace.Session{
		Name: "feature-x", Dir: "/ws/payments/feature-x", Branch: "feature-x",
		Members: []workspace.SessionMember{
			{RepoPath: "/repo/api", WorktreePath: "/ws/payments/feature-x/api"},
			{RepoPath: "/repo/web", WorktreePath: "/ws/payments/feature-x/web"},
		},
	}
}

func TestSessionSafeToDelete(t *testing.T) {
	sess := twoMemberSession()
	tests := []struct {
		name  string
		git   *fakeGitOps
		sess  workspace.Session
		wantS bool
	}{
		{name: "all merged and clean", git: &fakeGitOps{mergeState: git.MergeMerged}, sess: sess, wantS: true},
		{name: "one member unmerged", git: &fakeGitOps{
			mergeState: git.MergeMerged, mergeByRepo: map[string]git.MergeState{"/repo/web": git.MergeNotMerged},
		}, sess: sess, wantS: false},
		{name: "merge status unknown", git: &fakeGitOps{mergeState: git.MergeUnknown}, sess: sess, wantS: false},
		{name: "one worktree dirty", git: &fakeGitOps{
			mergeState: git.MergeMerged, dirty: map[string]bool{"/ws/payments/feature-x/api": true},
		}, sess: sess, wantS: false},
		{name: "dirty check fails", git: &fakeGitOps{mergeState: git.MergeMerged, dirtyErr: errors.New("boom")}, sess: sess, wantS: false},
		{name: "no members", git: &fakeGitOps{mergeState: git.MergeMerged}, sess: workspace.Session{Name: "empty"}, wantS: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionSafeToDelete(tt.git, tt.sess); got != tt.wantS {
				t.Errorf("sessionSafeToDelete = %v, want %v", got, tt.wantS)
			}
		})
	}
}

func TestWsSafeProbe_RunsOnceDueAndNeverOverlaps(t *testing.T) {
	sess := workspace.SessionStatus{Session: twoMemberSession(), State: settings.StateStopped}
	m := model{
		gitOp:      &fakeGitOps{mergeState: git.MergeMerged},
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", sess)},
		wsSafeDue:  true,
	}

	m, cmd := m.maybeProbeWsSafe()
	if cmd == nil || !m.wsSafeProbing || m.wsSafeDue {
		t.Fatalf("due probe must dispatch and mark in flight; probing=%v due=%v", m.wsSafeProbing, m.wsSafeDue)
	}

	// A tick while the probe is in flight only re-marks it due.
	updated, _ := m.Update(wsSafeTickMsg{})
	m2 := updated.(model)
	if !m2.wsSafeDue || !m2.wsSafeProbing {
		t.Fatalf("tick during a probe must defer; probing=%v due=%v", m2.wsSafeProbing, m2.wsSafeDue)
	}

	// The result lands and the deferred probe starts.
	updated3, next := m2.Update(cmd())
	m3 := updated3.(model)
	if !m3.wsSafe[sess.Session.Dir] {
		t.Errorf("probe result must be recorded; wsSafe=%v", m3.wsSafe)
	}
	if next == nil || !m3.wsSafeProbing {
		t.Error("the deferred probe must start once the previous one reports")
	}
}

func TestWsSafeProbe_SkipsSessionsThatCannotBeProbed(t *testing.T) {
	gitOp := &fakeGitOps{mergeState: git.MergeMerged}
	creating := workspace.SessionStatus{Session: workspace.Session{Name: "new", Dir: "/ws/new"}, State: settings.StateCreating}
	missing := workspace.SessionStatus{Session: workspace.Session{Name: "gone", Dir: "/ws/gone"}, State: settings.StateMissing}
	m := model{
		gitOp:      gitOp,
		wsStatuses: []workspace.WorkspaceStatus{makeWsStatus("payments", creating, missing)},
		wsSafeDue:  true,
	}

	m, cmd := m.maybeProbeWsSafe()
	if cmd != nil {
		t.Fatal("nothing probeable: no probe must be dispatched")
	}
	if !m.wsSafeDue {
		t.Error("an undispatched probe must stay due for when sessions appear")
	}
}

func TestWsSafeProbe_FinishedPullMakesProbeDue(t *testing.T) {
	sess := workspace.SessionStatus{Session: twoMemberSession(), State: settings.StateStopped}
	m := model{
		gitOp:      &fakeGitOps{},
		wsStatuses: []workspace.WorkspaceStatus{wsStatusWithSession("payments", sess)},
		wsPulling:  map[wsTarget]struct{}{{workspace: "payments"}: {}},
	}
	updated, cmd := m.Update(wsPullFinishedMsg{target: wsTarget{workspace: "payments"}})
	if cmd == nil || !updated.(model).wsSafeProbing {
		t.Error("a finished pull must re-probe: a pulled base can newly contain session branches")
	}
}

func TestWorkspaceView_SafeSessionIsTagged(t *testing.T) {
	safe := makeSessionStatus("done-work", "done-work", settings.StateStopped, "/repo/api")
	safe.Session.Dir = "/ws/done-work"
	wip := makeSessionStatus("wip", "wip", settings.StateStopped, "/repo/api")
	wip.Session.Dir = "/ws/wip"
	m := model{
		width: 120, height: 40, view: viewWorkspaces,
		wsStatuses: []workspace.WorkspaceStatus{makeWsStatus("payments", safe, wip)},
		wsSafe:     map[string]bool{"/ws/done-work": true},
	}

	var doneLine, wipLine string
	for _, line := range strings.Split(m.View(), "\n") {
		switch {
		case strings.Contains(line, "done-work"):
			doneLine = line
		case strings.Contains(line, "wip"):
			wipLine = line
		}
	}
	if !strings.Contains(doneLine, "safe to delete") {
		t.Errorf("safe session row must be tagged; got %q", doneLine)
	}
	if strings.Contains(wipLine, "safe to delete") {
		t.Errorf("unprobed/unsafe session row must not be tagged; got %q", wipLine)
	}
}
