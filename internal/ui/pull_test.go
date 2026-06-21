package ui

// pull_test.go — unit tests for the 'P' pull action: key dispatch, guards,
// in-flight indicator, and result-hint handling. Git operations are injected
// via fakeGitOps so no real repo is required.

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// pullFinishedFrom runs cmd — which may be a tea.Batch of the pull Cmd and the
// spinner ticker — and returns the first pullFinishedMsg produced, running
// batched cmds in order so the sleeping spinner ticker is never executed.
func pullFinishedFrom(t *testing.T, cmd tea.Cmd) pullFinishedMsg {
	t.Helper()
	msg := runCmd(cmd)
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if pf, ok := c().(pullFinishedMsg); ok {
				return pf
			}
		}
		t.Fatal("no pullFinishedMsg produced by batched cmd")
		return pullFinishedMsg{}
	}
	if pf, ok := msg.(pullFinishedMsg); ok {
		return pf
	}
	t.Fatalf("expected pullFinishedMsg, got %T", msg)
	return pullFinishedMsg{}
}

// TestPullKeyDispatchesPullForHighlightedRow verifies 'P' marks the highlighted
// worktree as pulling, starts the spinner, and dispatches git.Pull against that
// worktree path carrying its branch through to the result message.
func TestPullKeyDispatchesPullForHighlightedRow(t *testing.T) {
	gitFake := &fakeGitOps{pullResult: "Updating abc..def"}
	m := makeTestModel(&fakeTmuxOps{available: true}, gitFake, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r", "main", "", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	updated, cmd := m.Update(keyMsg("P"))
	m2 := updated.(model)

	if !m2.pulling["/r"] {
		t.Error("P must mark the highlighted worktree as pulling")
	}
	if !m2.spinnerActive {
		t.Error("P must activate the spinner ticker")
	}

	got := pullFinishedFrom(t, cmd)
	if len(gitFake.pullCalls) != 1 || gitFake.pullCalls[0] != (pullCall{worktreePath: "/r", branch: "main"}) {
		t.Errorf("expected one Pull(/r, main) call, got %v", gitFake.pullCalls)
	}
	if got.path != "/r" || got.branch != "main" {
		t.Errorf("pullFinishedMsg = %+v, want path=/r branch=main", got)
	}
}

// TestPullKeyRejectsDetachedHead verifies a row with no branch cannot be pulled:
// no Cmd is dispatched, nothing is marked pulling, and a hint explains why.
func TestPullKeyRejectsDetachedHead(t *testing.T) {
	gitFake := &fakeGitOps{}
	m := makeTestModel(&fakeTmuxOps{available: true}, gitFake, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r/wt", "", "", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	updated, cmd := m.Update(keyMsg("P"))
	m2 := updated.(model)

	if cmd != nil {
		t.Error("P on a detached-HEAD row must not dispatch a pull")
	}
	if len(gitFake.pullCalls) != 0 {
		t.Errorf("Pull must not be called for a detached HEAD, got %v", gitFake.pullCalls)
	}
	if len(m2.pulling) != 0 {
		t.Error("a rejected pull must not mark the worktree pulling")
	}
	if m2.tmuxHint == "" {
		t.Error("a rejected pull must surface a hint")
	}
}

// TestPullKeyIgnoresRepeatWhileInFlight verifies a second 'P' on a worktree
// already pulling is a no-op (no duplicate dispatch).
func TestPullKeyIgnoresRepeatWhileInFlight(t *testing.T) {
	gitFake := &fakeGitOps{}
	m := makeTestModel(&fakeTmuxOps{available: true}, gitFake, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r", "main", "", workspace.StateStopped, state.AttnInactive, fixedNow),
	})
	m.addPulling("/r")

	updated, cmd := m.Update(keyMsg("P"))
	_ = updated.(model)

	if cmd != nil {
		t.Error("a repeated P while a pull is in flight must not dispatch again")
	}
	if len(gitFake.pullCalls) != 0 {
		t.Errorf("a repeated P must not call Pull, got %v", gitFake.pullCalls)
	}
}

// TestPullFinishedMsgClearsPullingAndReportsSummary verifies a successful pull
// clears the in-flight indicator and surfaces git's summary in the hint.
func TestPullFinishedMsgClearsPullingAndReportsSummary(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, &fakeGitOps{}, &fakeHarnessOps{}, nil)
	m.addPulling("/r")

	updated, _ := m.Update(pullFinishedMsg{path: "/r", branch: "main", summary: "Already up to date."})
	m2 := updated.(model)

	if m2.pulling["/r"] {
		t.Error("pullFinishedMsg must clear the in-flight indicator")
	}
	if !strings.Contains(m2.tmuxHint, "main") || !strings.Contains(m2.tmuxHint, "Already up to date.") {
		t.Errorf("hint = %q, want it to name the branch and summary", m2.tmuxHint)
	}
}

// TestPullFinishedMsgSurfacesError verifies a failed pull clears the indicator
// and surfaces the error in the hint.
func TestPullFinishedMsgSurfacesError(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, &fakeGitOps{}, &fakeHarnessOps{}, nil)
	m.addPulling("/r")

	updated, _ := m.Update(pullFinishedMsg{path: "/r", branch: "main", err: errors.New("diverged")})
	m2 := updated.(model)

	if m2.pulling["/r"] {
		t.Error("a failed pull must still clear the in-flight indicator")
	}
	if !strings.Contains(m2.tmuxHint, "failed") {
		t.Errorf("hint = %q, want it to report a failure", m2.tmuxHint)
	}
}

// TestSpinnerTickContinuesWhilePulling locks in that the shared spinner ticker
// keeps animating while a pull is in flight even with no pending creates, and
// stops once the pull clears.
func TestSpinnerTickContinuesWhilePulling(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, &fakeGitOps{}, &fakeHarnessOps{}, nil)
	m.addPulling("/r")
	m.spinnerActive = true

	updated, cmd := m.Update(spinnerTickMsg(fixedNow))
	m2 := updated.(model)
	if m2.spinnerFrame != 1 {
		t.Errorf("frame must advance while pulling, got %d", m2.spinnerFrame)
	}
	if cmd == nil {
		t.Error("spinner must re-arm while a pull is in flight")
	}

	delete(m2.pulling, "/r")
	updated2, cmd2 := m2.Update(spinnerTickMsg(fixedNow))
	m3 := updated2.(model)
	if m3.spinnerActive {
		t.Error("spinner must deactivate when no pulls or creates remain")
	}
	if cmd2 != nil {
		t.Error("spinner must not re-arm when no pulls or creates remain")
	}
}

// TestCanPullWorktreeRejects verifies the guard rejects rows that cannot be
// pulled, each with a non-empty reason.
func TestCanPullWorktreeRejects(t *testing.T) {
	cases := map[string]workspace.Row{
		"empty worktree": {Repo: "/r", Branch: "main"},
		"creating":       {Repo: "/r", Worktree: "/r-feat", Branch: "feat", State: workspace.StateCreating},
		"missing":        {Repo: "/r", Worktree: "/r-feat", Branch: "feat", State: workspace.StateMissing},
		"detached HEAD":  {Repo: "/r", Worktree: "/r/wt", Branch: ""},
	}
	for name, row := range cases {
		ok, reason := canPullWorktree(row)
		if ok {
			t.Errorf("%s: expected row to be unpullable", name)
		}
		if reason == "" {
			t.Errorf("%s: rejection must carry a reason", name)
		}
	}
}

// TestRenderWorkspaceRowsShowsPullingIndicator verifies an in-flight pull row
// renders the "(pulling…)" spinner suffix.
func TestRenderWorkspaceRowsShowsPullingIndicator(t *testing.T) {
	m := model{width: 200}
	m.addPulling("/r")
	rows := []workspace.Row{
		makeRow("/r", "/r", "main", "", workspace.StateStopped, state.AttnInactive, fixedNow),
	}

	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "(pulling…)") {
		t.Fatalf("a pulling row must show '(pulling…)', got %q", got)
	}
}
