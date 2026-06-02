package ui

// actions_test.go — unit tests for the enter/n action dispatch, launching
// overlay, and $TMUX-unset degradation.
//
// All tmux, git, and harness operations are injected via fakes so no real
// tmux server, git repo, or opencode binary is required.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// ---------------------------------------------------------------------------
// Fake implementations
// ---------------------------------------------------------------------------

// fakeTmuxOps records calls and returns canned responses.
type fakeTmuxOps struct {
	available         bool
	findWindowResult  tmuxctl.Target
	findWindowErr     error
	processAlive      bool
	processAliveErr   error
	relaunchErr       error
	ensureWindowResult tmuxctl.Target
	ensureWindowErr   error
	selectErr         error

	// Call recording.
	findWindowCalls   []string
	processAliveCalls []tmuxctl.Target
	relaunchCalls     []relaunchCall
	ensureWindowCalls []ensureWindowCall
	selectCalls       []tmuxctl.Target
}

type relaunchCall struct {
	target tmuxctl.Target
	argv   []string
}

type ensureWindowCall struct {
	dir  string
	name string
	argv []string
}

func (f *fakeTmuxOps) Available() bool { return f.available }

func (f *fakeTmuxOps) FindWindowByDir(dir string) (tmuxctl.Target, error) {
	f.findWindowCalls = append(f.findWindowCalls, dir)
	return f.findWindowResult, f.findWindowErr
}

func (f *fakeTmuxOps) WindowProcessAlive(target tmuxctl.Target) (bool, error) {
	f.processAliveCalls = append(f.processAliveCalls, target)
	return f.processAlive, f.processAliveErr
}

func (f *fakeTmuxOps) RelaunchInWindow(target tmuxctl.Target, argv []string) error {
	f.relaunchCalls = append(f.relaunchCalls, relaunchCall{target: target, argv: argv})
	return f.relaunchErr
}

func (f *fakeTmuxOps) EnsureWindow(dir, name string, argv []string) (tmuxctl.Target, error) {
	f.ensureWindowCalls = append(f.ensureWindowCalls, ensureWindowCall{dir: dir, name: name, argv: argv})
	return f.ensureWindowResult, f.ensureWindowErr
}

func (f *fakeTmuxOps) Select(target tmuxctl.Target) error {
	f.selectCalls = append(f.selectCalls, target)
	return f.selectErr
}

// fakeGitOps records AddWorktree calls and returns canned results.
type fakeGitOps struct {
	addResult string
	addErr    error
	addCalls  []addWorktreeCall
}

type addWorktreeCall struct {
	repoPath string
	branch   string
	dest     string
}

func (f *fakeGitOps) AddWorktree(repoPath, branch, dest string) (string, error) {
	f.addCalls = append(f.addCalls, addWorktreeCall{repoPath: repoPath, branch: branch, dest: dest})
	if f.addResult != "" {
		return f.addResult, f.addErr
	}
	return dest, f.addErr
}

// fakeHarnessOps returns a fixed argv for any kind.
type fakeHarnessOps struct {
	argv []string
	err  error
}

func (f *fakeHarnessOps) Get(kind harness.Kind) (harness.Harness, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &fakeHarness{argv: f.argv}, nil
}

type fakeHarness struct{ argv []string }

func (h *fakeHarness) Kind() harness.Kind                              { return "fake" }
func (h *fakeHarness) Capabilities() harness.Capabilities             { return harness.Capabilities{} }
func (h *fakeHarness) LaunchArgv(wt string, token harness.ResumeToken) []string {
	if len(h.argv) > 0 {
		return h.argv
	}
	return []string{"fake-harness", wt}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// runCmd executes a tea.Cmd synchronously and returns the resulting tea.Msg.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// makeTestModel builds a model with injected fakes and the given rows.
// The textinput is initialized so Focus() calls don't panic.
func makeTestModel(tmux *fakeTmuxOps, gitOp *fakeGitOps, harnOp *fakeHarnessOps, rows []workspace.Row) model {
	ti := newTestInput()
	return model{
		width:         120,
		workspaceRows: rows,
		tmux:          tmux,
		gitOp:         gitOp,
		harnOp:        harnOp,
		input:         ti,
	}
}

// newTestInput returns an initialized textinput.Model for use in tests.
// It mirrors the initialization in newModel but without the AcceptSuggestion
// override (not needed in tests).
func newTestInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "test"
	return ti
}

// ---------------------------------------------------------------------------
// $TMUX unset degradation
// ---------------------------------------------------------------------------

func TestEnterShowsHintWhenTmuxUnavailable(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: false}
	m := makeTestModel(tmuxFake, nil, nil, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if cmd != nil {
		t.Error("enter with tmux unavailable must return nil cmd")
	}
	if m2.tmuxHint == "" {
		t.Error("enter with tmux unavailable must set tmuxHint")
	}
	if !strings.Contains(m2.tmuxHint, "tmux") {
		t.Errorf("hint must mention tmux, got %q", m2.tmuxHint)
	}
}

func TestNShowsHintWhenTmuxUnavailable(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: false}
	m := makeTestModel(tmuxFake, nil, nil, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateEmpty, state.AttnInactive, time.Time{}),
	})

	updated, cmd := m.Update(keyMsg("n"))
	m2 := updated.(model)

	if cmd != nil {
		t.Error("n with tmux unavailable must return nil cmd")
	}
	if m2.tmuxHint == "" {
		t.Error("n with tmux unavailable must set tmuxHint")
	}
}

// ---------------------------------------------------------------------------
// enter on running row → Select (jump)
// ---------------------------------------------------------------------------

func TestEnterOnRunningRowCallsSelect(t *testing.T) {
	tmuxFake := &fakeTmuxOps{
		available:        true,
		findWindowResult: "main:1",
		findWindowErr:    nil,
		selectErr:        nil,
	}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
	})

	_, cmd := m.Update(keyMsg("enter"))
	msg := runCmd(cmd)

	result, ok := msg.(launchResultMsg)
	if !ok {
		t.Fatalf("expected launchResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("expected no error, got %v", result.err)
	}
	if len(tmuxFake.findWindowCalls) != 1 {
		t.Errorf("expected 1 FindWindowByDir call, got %d", len(tmuxFake.findWindowCalls))
	}
	if len(tmuxFake.selectCalls) != 1 || tmuxFake.selectCalls[0] != "main:1" {
		t.Errorf("expected Select(main:1), got %v", tmuxFake.selectCalls)
	}
	// No relaunch or ensure calls.
	if len(tmuxFake.relaunchCalls) != 0 {
		t.Errorf("expected no RelaunchInWindow calls, got %d", len(tmuxFake.relaunchCalls))
	}
	if len(tmuxFake.ensureWindowCalls) != 0 {
		t.Errorf("expected no EnsureWindow calls, got %d", len(tmuxFake.ensureWindowCalls))
	}
}

// ---------------------------------------------------------------------------
// enter on stopped row, window alive → Select
// ---------------------------------------------------------------------------

func TestEnterOnStoppedRowWindowAliveCallsSelect(t *testing.T) {
	tmuxFake := &fakeTmuxOps{
		available:        true,
		findWindowResult: "main:2",
		findWindowErr:    nil,
		processAlive:     true,
		selectErr:        nil,
	}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	_, cmd := m.Update(keyMsg("enter"))
	msg := runCmd(cmd)

	result, ok := msg.(launchResultMsg)
	if !ok {
		t.Fatalf("expected launchResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("expected no error, got %v", result.err)
	}
	if len(tmuxFake.relaunchCalls) != 0 {
		t.Errorf("expected no RelaunchInWindow calls (process alive), got %d", len(tmuxFake.relaunchCalls))
	}
	if len(tmuxFake.selectCalls) != 1 {
		t.Errorf("expected 1 Select call, got %d", len(tmuxFake.selectCalls))
	}
}

// ---------------------------------------------------------------------------
// enter on stopped row, window dead → RelaunchInWindow then Select
// ---------------------------------------------------------------------------

func TestEnterOnStoppedRowWindowDeadCallsRelaunchThenSelect(t *testing.T) {
	tmuxFake := &fakeTmuxOps{
		available:        true,
		findWindowResult: "main:3",
		findWindowErr:    nil,
		processAlive:     false, // dead pane
		relaunchErr:      nil,
		selectErr:        nil,
	}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{argv: []string{"fake", "/r/a"}}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	_, cmd := m.Update(keyMsg("enter"))
	msg := runCmd(cmd)

	result, ok := msg.(launchResultMsg)
	if !ok {
		t.Fatalf("expected launchResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("expected no error, got %v", result.err)
	}
	if len(tmuxFake.relaunchCalls) != 1 {
		t.Errorf("expected 1 RelaunchInWindow call, got %d", len(tmuxFake.relaunchCalls))
	}
	if tmuxFake.relaunchCalls[0].target != "main:3" {
		t.Errorf("RelaunchInWindow target = %q, want main:3", tmuxFake.relaunchCalls[0].target)
	}
	if len(tmuxFake.selectCalls) != 1 || tmuxFake.selectCalls[0] != "main:3" {
		t.Errorf("expected Select(main:3), got %v", tmuxFake.selectCalls)
	}
}

// ---------------------------------------------------------------------------
// enter on stopped row, no window → EnsureWindow then Select
// ---------------------------------------------------------------------------

func TestEnterOnStoppedRowNoWindowCallsEnsureWindowThenSelect(t *testing.T) {
	tmuxFake := &fakeTmuxOps{
		available:          true,
		findWindowErr:      tmuxctl.ErrWindowNotFound,
		ensureWindowResult: "main:4",
		ensureWindowErr:    nil,
		selectErr:          nil,
	}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{argv: []string{"fake", "/r/a"}}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	_, cmd := m.Update(keyMsg("enter"))
	msg := runCmd(cmd)

	result, ok := msg.(launchResultMsg)
	if !ok {
		t.Fatalf("expected launchResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("expected no error, got %v", result.err)
	}
	if len(tmuxFake.ensureWindowCalls) != 1 {
		t.Errorf("expected 1 EnsureWindow call, got %d", len(tmuxFake.ensureWindowCalls))
	}
	if len(tmuxFake.selectCalls) != 1 || tmuxFake.selectCalls[0] != "main:4" {
		t.Errorf("expected Select(main:4), got %v", tmuxFake.selectCalls)
	}
}

// ---------------------------------------------------------------------------
// Launching overlay: enter sets overlay; re-enter on launching row does not
// re-launch (jumps instead)
// ---------------------------------------------------------------------------

func TestEnterSetsLaunchingOverlay(t *testing.T) {
	tmuxFake := &fakeTmuxOps{
		available:          true,
		findWindowErr:      tmuxctl.ErrWindowNotFound,
		ensureWindowResult: "main:5",
	}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	updated, _ := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.launching == nil || m2.launching["/r/a"] == (time.Time{}) {
		t.Error("enter must set launching overlay for the row's dir")
	}
}

func TestEnterOnLaunchingRowDoesNotSetNewOverlay(t *testing.T) {
	// Pre-set the launching overlay for the row.
	deadline := time.Now().Add(30 * time.Second)
	tmuxFake := &fakeTmuxOps{
		available:        true,
		findWindowResult: "main:5",
		findWindowErr:    nil,
		processAlive:     true,
	}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})
	m.launching = map[string]time.Time{"/r/a": deadline}

	// Press enter again — should jump (Select) not re-launch.
	_, cmd := m.Update(keyMsg("enter"))
	msg := runCmd(cmd)

	// The cmd should be a launchCmd (jump), not a new EnsureWindow.
	if _, ok := msg.(launchResultMsg); !ok {
		t.Fatalf("expected launchResultMsg on re-enter of launching row, got %T", msg)
	}
	// EnsureWindow must NOT have been called (we jumped, not launched).
	if len(tmuxFake.ensureWindowCalls) != 0 {
		t.Errorf("re-enter on launching row must not call EnsureWindow, got %d calls", len(tmuxFake.ensureWindowCalls))
	}
}

// ---------------------------------------------------------------------------
// Launching overlay: cleared on launchResultMsg error
// ---------------------------------------------------------------------------

func TestLaunchResultMsgErrorClearsOverlay(t *testing.T) {
	m := model{
		width: 120,
		launching: map[string]time.Time{
			"/r/a": time.Now().Add(30 * time.Second),
		},
	}

	updated, _ := m.Update(launchResultMsg{dir: "/r/a", err: errors.New("tmux error")})
	m2 := updated.(model)

	if m2.launching["/r/a"] != (time.Time{}) {
		t.Error("launchResultMsg with error must clear the launching overlay")
	}
	if m2.tmuxHint == "" {
		t.Error("launchResultMsg with error must set tmuxHint")
	}
}

// ---------------------------------------------------------------------------
// Launching overlay: cleared on tickMsg timeout
// ---------------------------------------------------------------------------

func TestTickMsgExpiresLaunchingOverlay(t *testing.T) {
	// Set a deadline in the past so the tick expires it.
	pastDeadline := time.Now().Add(-1 * time.Second)
	m := model{
		width: 120,
		launching: map[string]time.Time{
			"/r/a": pastDeadline,
		},
	}

	tick := time.Now()
	updated, _ := m.Update(tickMsg(tick))
	m2 := updated.(model)

	if m2.launching["/r/a"] != (time.Time{}) {
		t.Error("tickMsg must expire launching overlay past its deadline")
	}
}

func TestTickMsgDoesNotExpireActiveLaunchingOverlay(t *testing.T) {
	// Set a deadline in the future — should NOT be expired.
	futureDeadline := time.Now().Add(30 * time.Second)
	m := model{
		width: 120,
		launching: map[string]time.Time{
			"/r/a": futureDeadline,
		},
	}

	tick := time.Now()
	updated, _ := m.Update(tickMsg(tick))
	m2 := updated.(model)

	if m2.launching["/r/a"] == (time.Time{}) {
		t.Error("tickMsg must not expire launching overlay before its deadline")
	}
}

// ---------------------------------------------------------------------------
// Launching overlay: success-path clearing depends on the launched flag
// ---------------------------------------------------------------------------

func TestLaunchResultMsgSelectOnlyClearsOverlay(t *testing.T) {
	// A pure jump/select (launched=false) resumes an already-live window.
	// There is no new agent to wait for, so the overlay must clear at once —
	// otherwise the row sits in "launching…" until the 30s timeout.
	m := model{
		width: 120,
		launching: map[string]time.Time{
			"/r/a": time.Now().Add(30 * time.Second),
		},
	}

	updated, _ := m.Update(launchResultMsg{dir: "/r/a", launched: false, err: nil})
	m2 := updated.(model)
	if m2.launching["/r/a"] != (time.Time{}) {
		t.Error("select-only launchResultMsg must clear the overlay immediately")
	}
}

func TestLaunchResultMsgLaunchedKeepsOverlay(t *testing.T) {
	// A genuine (re)launch (launched=true) keeps the overlay until the next
	// merge confirms the row is running.
	m := model{
		width: 120,
		launching: map[string]time.Time{
			"/r/a": time.Now().Add(30 * time.Second),
		},
	}

	updated, _ := m.Update(launchResultMsg{dir: "/r/a", launched: true, err: nil})
	m2 := updated.(model)
	if m2.launching["/r/a"] == (time.Time{}) {
		t.Error("launched launchResultMsg must keep the overlay until confirmed running")
	}
}

// ---------------------------------------------------------------------------
// Missing row: enter shows hint, does not launch
// ---------------------------------------------------------------------------

func TestEnterOnMissingRowShowsHint(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: true}
	m := makeTestModel(tmuxFake, nil, nil, []workspace.Row{
		makeRow("/r", "/r/gone", "main", "old title", workspace.StateMissing, state.AttnInactive, fixedNow),
	})

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if cmd != nil {
		t.Error("enter on missing row must return nil cmd")
	}
	if m2.tmuxHint == "" {
		t.Error("enter on missing row must set tmuxHint")
	}
	if !strings.Contains(m2.tmuxHint, "missing") {
		t.Errorf("hint must mention missing, got %q", m2.tmuxHint)
	}
}

// ---------------------------------------------------------------------------
// 'n' key: opens promptNewWorktree
// ---------------------------------------------------------------------------

func TestNKeyOpensNewWorktreePrompt(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: true}
	m := makeTestModel(tmuxFake, nil, nil, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateEmpty, state.AttnInactive, time.Time{}),
	})

	updated, _ := m.Update(keyMsg("n"))
	m2 := updated.(model)

	if m2.prompt != promptNewWorktree {
		t.Errorf("n must set prompt to promptNewWorktree, got %v", m2.prompt)
	}
	if m2.newWorktreeRepo == "" {
		t.Error("n must capture the repo path in newWorktreeRepo")
	}
}

func TestNKeyEscCancelsPrompt(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: true}
	m := makeTestModel(tmuxFake, nil, nil, []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateEmpty, state.AttnInactive, time.Time{}),
	})

	// Open the prompt.
	updated, _ := m.Update(keyMsg("n"))
	m2 := updated.(model)
	if m2.prompt != promptNewWorktree {
		t.Fatalf("expected promptNewWorktree, got %v", m2.prompt)
	}

	// Press esc to cancel.
	updated2, cmd := m2.Update(keyMsg("esc"))
	m3 := updated2.(model)

	if m3.prompt != promptIdle {
		t.Errorf("esc must cancel promptNewWorktree, got %v", m3.prompt)
	}
	if m3.newWorktreeRepo != "" {
		t.Errorf("esc must clear newWorktreeRepo, got %q", m3.newWorktreeRepo)
	}
	if cmd != nil {
		t.Error("esc must return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// newWorktreeCmd: calls AddWorktree + EnsureWindow + Select
// ---------------------------------------------------------------------------

func TestNewWorktreeCmdCallsAddWorktreeAndLaunch(t *testing.T) {
	tmuxFake := &fakeTmuxOps{
		available:          true,
		ensureWindowResult: "main:6",
	}
	gitFake := &fakeGitOps{addResult: "/r-feat"}
	harnFake := &fakeHarnessOps{argv: []string{"fake", "/r-feat"}}

	cmd := newWorktreeCmd(tmuxFake, gitFake, harnFake, "/r", "feat", "fake")
	msg := runCmd(cmd)

	result, ok := msg.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("expected worktreeCreatedMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("expected no error, got %v", result.err)
	}
	if result.canonDest != "/r-feat" {
		t.Errorf("canonDest = %q, want /r-feat", result.canonDest)
	}
	if len(gitFake.addCalls) != 1 {
		t.Errorf("expected 1 AddWorktree call, got %d", len(gitFake.addCalls))
	}
	if gitFake.addCalls[0].branch != "feat" {
		t.Errorf("AddWorktree branch = %q, want feat", gitFake.addCalls[0].branch)
	}
	if len(tmuxFake.ensureWindowCalls) != 1 {
		t.Errorf("expected 1 EnsureWindow call, got %d", len(tmuxFake.ensureWindowCalls))
	}
	if len(tmuxFake.selectCalls) != 1 || tmuxFake.selectCalls[0] != "main:6" {
		t.Errorf("expected Select(main:6), got %v", tmuxFake.selectCalls)
	}
}

func TestNewWorktreeCmdGitErrorReturnsMsg(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: true}
	gitFake := &fakeGitOps{addErr: errors.New("branch already exists")}
	harnFake := &fakeHarnessOps{}

	cmd := newWorktreeCmd(tmuxFake, gitFake, harnFake, "/r", "feat", "fake")
	msg := runCmd(cmd)

	result, ok := msg.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("expected worktreeCreatedMsg, got %T", msg)
	}
	if result.err == nil {
		t.Error("expected error from git failure, got nil")
	}
	if !strings.Contains(result.err.Error(), "branch already exists") {
		t.Errorf("error must mention branch already exists, got %v", result.err)
	}
}

func TestNewWorktreeCmdTmuxUnavailableReturnsMsg(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: false}
	gitFake := &fakeGitOps{}
	harnFake := &fakeHarnessOps{}

	cmd := newWorktreeCmd(tmuxFake, gitFake, harnFake, "/r", "feat", "fake")
	msg := runCmd(cmd)

	result, ok := msg.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("expected worktreeCreatedMsg, got %T", msg)
	}
	if !errors.Is(result.err, tmuxctl.ErrNotAvailable) {
		t.Errorf("expected ErrNotAvailable, got %v", result.err)
	}
}

// ---------------------------------------------------------------------------
// worktreeCreatedMsg: sets launching overlay on success
// ---------------------------------------------------------------------------

func TestWorktreeCreatedMsgSetsLaunchingOverlay(t *testing.T) {
	m := model{width: 120}

	updated, _ := m.Update(worktreeCreatedMsg{canonDest: "/r/feat", err: nil})
	m2 := updated.(model)

	if m2.launching == nil || m2.launching["/r/feat"] == (time.Time{}) {
		t.Error("worktreeCreatedMsg success must set launching overlay for canonDest")
	}
}

func TestWorktreeCreatedMsgErrorSetsHint(t *testing.T) {
	m := model{width: 120}

	updated, _ := m.Update(worktreeCreatedMsg{canonDest: "/r/feat", err: errors.New("git error")})
	m2 := updated.(model)

	if m2.tmuxHint == "" {
		t.Error("worktreeCreatedMsg error must set tmuxHint")
	}
	if m2.launching != nil && m2.launching["/r/feat"] != (time.Time{}) {
		t.Error("worktreeCreatedMsg error must not set launching overlay")
	}
}

// ---------------------------------------------------------------------------
// Render: launching row shows "launching…" glyph
// ---------------------------------------------------------------------------

func TestRenderWorkspaceRowsLaunchingRowShowsLaunchingText(t *testing.T) {
	m := model{
		width: 200,
		launching: map[string]time.Time{
			"/r/a": time.Now().Add(30 * time.Second),
		},
	}
	rows := []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateStopped, state.AttnInactive, fixedNow),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "launching") {
		t.Fatalf("launching row must show 'launching' text, got %q", got)
	}
}

func TestRenderWorkspaceRowsNonLaunchingRowNotAffected(t *testing.T) {
	m := model{
		width: 200,
		// No launching overlay.
	}
	rows := []workspace.Row{
		makeRow("/r", "/r/a", "main", "stopped session", workspace.StateStopped, state.AttnInactive, fixedNow),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if strings.Contains(got, "launching") {
		t.Fatalf("non-launching row must not show 'launching' text, got %q", got)
	}
	if !strings.Contains(got, "stopped session") {
		t.Fatalf("non-launching stopped row must show title, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Render: tmuxHint shown in workspace rows
// ---------------------------------------------------------------------------

func TestRenderWorkspaceRowsShowsTmuxHint(t *testing.T) {
	m := model{
		width:    200,
		tmuxHint: "tmux not available — start cogitator inside a tmux session",
	}
	rows := []workspace.Row{
		makeRow("/r", "/r/a", "main", "row-a", workspace.StateEmpty, state.AttnInactive, time.Time{}),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "tmux not available") {
		t.Fatalf("tmuxHint must appear in rendered output, got %q", got)
	}
}
