package ui

// harness_chooser_test.go — unit tests for the per-launch harness chooser
// (promptChooseHarness) and the create-time roster write in worktreeCreatedMsg.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// ---------------------------------------------------------------------------
// harnessChooserKinds helper
// ---------------------------------------------------------------------------

// fakeHarnessOpsWithKinds is a fakeHarnessOps variant that returns a
// configurable Kinds list, used to test the chooser without the real registry.
type fakeHarnessOpsWithKinds struct {
	kinds []harness.Kind
}

func (f *fakeHarnessOpsWithKinds) Get(kind harness.Kind) (harness.Harness, error) {
	return &fakeHarness{}, nil
}

func (f *fakeHarnessOpsWithKinds) Kinds() []harness.Kind {
	return f.kinds
}

func TestHarnessChooserKinds_SortsAndDeduplicates(t *testing.T) {
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"codex", "opencode"}}
	got := harnessChooserKinds(ops)
	if len(got) != 2 {
		t.Fatalf("expected 2 kinds, got %d: %v", len(got), got)
	}
	// Sorted: codex < opencode
	if got[0] != "codex" || got[1] != "opencode" {
		t.Errorf("expected [codex opencode], got %v", got)
	}
}

func TestHarnessChooserKinds_NilOpsFallsBackToOpencode(t *testing.T) {
	got := harnessChooserKinds(nil)
	if len(got) != 1 || got[0] != harness.KindOpenCode {
		t.Errorf("nil ops must return [opencode], got %v", got)
	}
}

func TestHarnessChooserKinds_EmptyKindsFallsBackToOpencode(t *testing.T) {
	ops := &fakeHarnessOpsWithKinds{kinds: nil}
	got := harnessChooserKinds(ops)
	if len(got) != 1 || got[0] != harness.KindOpenCode {
		t.Errorf("empty kinds must return [opencode], got %v", got)
	}
}

func TestDefaultHarnessIndex_ReturnsOpencodeIndex(t *testing.T) {
	kinds := []harness.Kind{"codex", "opencode"}
	idx := defaultHarnessIndex(kinds)
	if idx != 1 {
		t.Errorf("expected index 1 (opencode), got %d", idx)
	}
}

func TestDefaultHarnessIndex_ReturnsZeroWhenOpencodeAbsent(t *testing.T) {
	kinds := []harness.Kind{"codex"}
	idx := defaultHarnessIndex(kinds)
	if idx != 0 {
		t.Errorf("expected 0 when opencode absent, got %d", idx)
	}
}

// ---------------------------------------------------------------------------
// promptNewWorktree → promptChooseHarness transition
// ---------------------------------------------------------------------------

// makeChooserModel builds a model in promptNewWorktree with a branch typed.
func makeChooserModel(harnOp harnessOps, rows []workspace.Row) model {
	ti := newTestInput()
	ti.SetValue("feat")
	return model{
		width:           120,
		workspaceRows:   rows,
		harnOp:          harnOp,
		input:           ti,
		prompt:          promptNewWorktree,
		newWorktreeRepo: "/r",
	}
}

func TestEnterOnBranchPromptOpensChooser(t *testing.T) {
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"codex", "opencode"}}
	m := makeChooserModel(ops, []workspace.Row{
		makeRow("/r", "/r/a", "main", "a", workspace.StateStopped, state.AttnInactive, time.Time{}),
	})

	updated, _ := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptChooseHarness {
		t.Errorf("enter on branch prompt must open promptChooseHarness, got %v", m2.prompt)
	}
	if m2.newWorktreeBranch != "feat" {
		t.Errorf("branch must be carried to chooser, got %q", m2.newWorktreeBranch)
	}
	if len(m2.harnessChooserKinds) == 0 {
		t.Error("chooser kinds must be populated")
	}
}

func TestEnterOnBranchPromptEmptyBranchCancels(t *testing.T) {
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"opencode"}}
	ti := newTestInput()
	ti.SetValue("   ") // whitespace only
	m := model{
		width:           120,
		harnOp:          ops,
		input:           ti,
		prompt:          promptNewWorktree,
		newWorktreeRepo: "/r",
	}

	updated, _ := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("empty branch must cancel to promptIdle, got %v", m2.prompt)
	}
	if m2.newWorktreeBranch != "" {
		t.Errorf("empty branch must not set newWorktreeBranch, got %q", m2.newWorktreeBranch)
	}
}

func TestEscOnBranchPromptCancelsFlow(t *testing.T) {
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"opencode"}}
	m := makeChooserModel(ops, nil)

	updated, cmd := m.Update(keyMsg("esc"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("esc must cancel to promptIdle, got %v", m2.prompt)
	}
	if m2.newWorktreeRepo != "" {
		t.Errorf("esc must clear newWorktreeRepo, got %q", m2.newWorktreeRepo)
	}
	if cmd != nil {
		t.Error("esc must return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// promptChooseHarness key handling
// ---------------------------------------------------------------------------

// makeActiveChooserModel builds a model already in promptChooseHarness.
func makeActiveChooserModel(kinds []harness.Kind, cursor int) model {
	return model{
		width:                120,
		input:                newTestInput(),
		prompt:               promptChooseHarness,
		newWorktreeRepo:      "/r",
		newWorktreeBranch:    "feat",
		harnessChooserKinds:  kinds,
		harnessChooserCursor: cursor,
	}
}

func TestChooserUpDownMovesCursor(t *testing.T) {
	kinds := []harness.Kind{"codex", "opencode"}
	m := makeActiveChooserModel(kinds, 0)

	updated, _ := m.Update(keyMsg("down"))
	m2 := updated.(model)
	if m2.harnessChooserCursor != 1 {
		t.Errorf("down must move cursor to 1, got %d", m2.harnessChooserCursor)
	}

	updated2, _ := m2.Update(keyMsg("up"))
	m3 := updated2.(model)
	if m3.harnessChooserCursor != 0 {
		t.Errorf("up must move cursor back to 0, got %d", m3.harnessChooserCursor)
	}
}

func TestChooserEscCancelsFlow(t *testing.T) {
	m := makeActiveChooserModel([]harness.Kind{"codex", "opencode"}, 1)

	updated, cmd := m.Update(keyMsg("esc"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("esc must cancel to promptIdle, got %v", m2.prompt)
	}
	if m2.newWorktreeRepo != "" || m2.newWorktreeBranch != "" {
		t.Errorf("esc must clear repo and branch, got repo=%q branch=%q", m2.newWorktreeRepo, m2.newWorktreeBranch)
	}
	if cmd != nil {
		t.Error("esc must return nil cmd")
	}
}

func TestChooserEnterDispatchesNewWorktreeCmdWithChosenKind(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: true, ensureWindowResult: "main:1"}
	gitFake := &fakeGitOps{addResult: "/r-feat"}
	harnFake := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"codex", "opencode"}}

	m := model{
		width:                120,
		input:                newTestInput(),
		prompt:               promptChooseHarness,
		newWorktreeRepo:      "/r",
		newWorktreeBranch:    "feat",
		harnessChooserKinds:  []harness.Kind{"codex", "opencode"},
		harnessChooserCursor: 0, // codex selected
		tmux:                 tmuxFake,
		gitOp:                gitFake,
		harnOp:               harnFake,
	}

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("enter must return to promptIdle, got %v", m2.prompt)
	}
	if m2.newWorktreeRepo != "" || m2.newWorktreeBranch != "" {
		t.Errorf("enter must clear repo and branch")
	}
	if cmd == nil {
		t.Fatal("enter must return a newWorktreeCmd")
	}

	// Execute the cmd and verify the result carries the chosen harness kind.
	msg := runCmd(cmd)
	result, ok := msg.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("expected worktreeCreatedMsg, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if result.harnessKind != "codex" {
		t.Errorf("harnessKind = %q, want codex", result.harnessKind)
	}
}

func TestChooserDefaultsToOpencodeWhenNoDefaultHarnessSet(t *testing.T) {
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"codex", "opencode"}}
	kinds := harnessChooserKinds(ops)
	idx := defaultHarnessIndex(kinds)
	if kinds[idx] != harness.KindOpenCode {
		t.Errorf("default cursor must point to opencode, got %v", kinds[idx])
	}
}

// ---------------------------------------------------------------------------
// worktreeCreatedMsg: create-time roster write via rosterUpserts
// ---------------------------------------------------------------------------

func TestWorktreeCreatedMsgWritesRosterEntryForCodex(t *testing.T) {
	upserts := make(chan workspace.RosterEntry, 4)
	m := model{
		width:         120,
		rosterUpserts: upserts,
	}

	updated, _ := m.Update(worktreeCreatedMsg{
		canonDest:   "/r/feat",
		harnessKind: "codex",
	})
	m2 := updated.(model)

	// Launching overlay must be set.
	if m2.launching == nil || m2.launching["/r/feat"] == (time.Time{}) {
		t.Error("success must set launching overlay")
	}

	// A roster entry must have been sent to the upserts channel.
	select {
	case entry := <-upserts:
		if entry.Dir != "/r/feat" {
			t.Errorf("entry.Dir = %q, want /r/feat", entry.Dir)
		}
		if entry.Harness != "codex" {
			t.Errorf("entry.Harness = %q, want codex", entry.Harness)
		}
	default:
		t.Error("no roster entry was sent to rosterUpserts")
	}
}

func TestWorktreeCreatedMsgDefaultsHarnessToOpencodeWhenEmpty(t *testing.T) {
	upserts := make(chan workspace.RosterEntry, 4)
	m := model{
		width:         120,
		rosterUpserts: upserts,
	}

	m.Update(worktreeCreatedMsg{canonDest: "/r/feat", harnessKind: ""})

	select {
	case entry := <-upserts:
		if entry.Harness != string(harness.KindOpenCode) {
			t.Errorf("empty harnessKind must default to opencode, got %q", entry.Harness)
		}
	default:
		t.Error("no roster entry was sent to rosterUpserts")
	}
}

func TestWorktreeCreatedMsgNoUpsertWhenRosterUpsertsNil(t *testing.T) {
	// rosterUpserts is nil — must not panic.
	m := model{width: 120}
	updated, _ := m.Update(worktreeCreatedMsg{canonDest: "/r/feat", harnessKind: "codex"})
	m2 := updated.(model)
	if m2.launching == nil || m2.launching["/r/feat"] == (time.Time{}) {
		t.Error("launching overlay must still be set even when rosterUpserts is nil")
	}
}

// ---------------------------------------------------------------------------
// Recorder: applySnapshot preserves existing Harness
// ---------------------------------------------------------------------------

func TestRecorderApplySnapshotPreservesExistingHarness(t *testing.T) {
	// Set up an isolated roster directory.
	tmp := t.TempDir()
	orig, had := os.LookupEnv("XDG_STATE_HOME")
	if err := os.Setenv("XDG_STATE_HOME", tmp); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("XDG_STATE_HOME", orig)
		} else {
			os.Unsetenv("XDG_STATE_HOME")
		}
	})

	// Create the worktree directory so Load doesn't prune it.
	if err := os.MkdirAll(tmp+"/cogitator/wt", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	wtDir := tmp + "/cogitator/wt"

	// Pre-seed the roster with a codex entry.
	seed := map[string]workspace.RosterEntry{
		wtDir: {
			Dir:          wtDir,
			Harness:      "codex",
			LastActivity: time.Now().Add(-time.Hour),
		},
	}
	if err := workspace.Save(seed); err != nil {
		t.Fatalf("Save seed: %v", err)
	}

	// Feed a snapshot for the same dir (simulating an opencode live-discovery
	// event, which should NOT overwrite the codex harness).
	snap := state.Snapshot{
		Sessions: []state.SessionView{
			{
				SessionID:    "sess-1",
				Directory:    wtDir,
				LastActivity: time.Now(), // newer than seed
			},
		},
	}
	snapCh := make(chan state.Snapshot, 2)
	snapCh <- snap
	close(snapCh)

	rec := workspace.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.RunSync(snapCh)
	}()
	<-done

	loaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entry, ok := loaded[wtDir]
	if !ok {
		t.Fatalf("entry for %q not found", wtDir)
	}
	if entry.Harness != "codex" {
		t.Errorf("applySnapshot must preserve existing Harness; got %q, want codex", entry.Harness)
	}
}

// ---------------------------------------------------------------------------
// renderHarnessChooser
// ---------------------------------------------------------------------------

func TestRenderHarnessChooserShowsKindsAndCursor(t *testing.T) {
	m := model{
		width:                200,
		input:                textinput.New(),
		prompt:               promptChooseHarness,
		newWorktreeRepo:      "/r",
		newWorktreeBranch:    "feat",
		harnessChooserKinds:  []harness.Kind{"codex", "opencode"},
		harnessChooserCursor: 0,
	}
	got := m.renderHarnessChooser(200, 20)
	if !strings.Contains(got, "codex") {
		t.Errorf("chooser must show codex, got %q", got)
	}
	if !strings.Contains(got, "opencode") {
		t.Errorf("chooser must show opencode, got %q", got)
	}
	if !strings.Contains(got, "feat") {
		t.Errorf("chooser must show branch name, got %q", got)
	}
}
