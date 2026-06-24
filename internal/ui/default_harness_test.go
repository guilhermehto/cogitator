package ui

// default_harness_test.go — the persistent default-harness feature: skipping
// the per-launch chooser, overriding an existing session's recorded harness on
// a cold relaunch, and the settings modal that sets/clears the default.

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// ---------------------------------------------------------------------------
// New-worktree: skip the chooser when a default is configured
// ---------------------------------------------------------------------------

func TestNewWorktreeEnter_SkipsChooserWhenDefaultResolvable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := workspace.SetDefaultHarness("opencode"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"codex", "opencode"}}
	m := makeChooserModel(ops, nil)

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Fatalf("prompt = %v, want promptIdle (chooser skipped)", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("expected a launch cmd to be dispatched")
	}
	if _, ok := m2.pendingCreates[createKey("/r", "feat")]; !ok {
		t.Error("expected an optimistic pending-create row for /r+feat")
	}
}

func TestNewWorktreeEnter_UnresolvableDefaultOpensChooser(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := workspace.SetDefaultHarness("ghost"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	// A harness ops whose Get always fails models a configured-but-unregistered
	// default; the flow must fall back to the chooser.
	m := makeChooserModel(&fakeHarnessOps{err: errors.New("not registered")}, nil)

	updated, _ := m.Update(keyMsg("enter"))
	if got := updated.(model).prompt; got != promptChooseHarness {
		t.Errorf("unresolvable default must open the chooser; prompt = %v", got)
	}
}

// ---------------------------------------------------------------------------
// Existing-session launch: the default overrides the recorded harness
// ---------------------------------------------------------------------------

func TestLaunchInner_DefaultOverridesRecordedHarness(t *testing.T) {
	tmux := &fakeTmuxOps{available: true, findWindowErr: errors.New("no window"), ensureWindowResult: "s:1"}
	row := workspace.Row{Repo: "/r", Worktree: "/r/a", Branch: "feat", Harness: "codex"}

	res := launchInner(tmux, row, &fakeHarnessOps{}, tmuxctl.ModeSession, "opencode")()

	if res.err != nil {
		t.Fatalf("unexpected err: %v", res.err)
	}
	if !res.launched {
		t.Fatal("a fresh window must report launched=true")
	}
	if res.harnessKind != "opencode" {
		t.Errorf("harnessKind = %q, want opencode (override)", res.harnessKind)
	}
}

func TestLaunchInner_NoOverrideWhenDefaultEmptyOrMatches(t *testing.T) {
	tmux := &fakeTmuxOps{available: true, findWindowErr: errors.New("no window"), ensureWindowResult: "s:1"}
	row := workspace.Row{Repo: "/r", Worktree: "/r/a", Harness: "codex"}

	if res := launchInner(tmux, row, &fakeHarnessOps{}, tmuxctl.ModeSession, "")(); res.harnessKind != "" {
		t.Errorf("empty default: harnessKind = %q, want empty (no override)", res.harnessKind)
	}
	if res := launchInner(tmux, row, &fakeHarnessOps{}, tmuxctl.ModeSession, "codex")(); res.harnessKind != "" {
		t.Errorf("matching default: harnessKind = %q, want empty (no override)", res.harnessKind)
	}
}

func TestLaunchResultMsg_OverrideUpsertsRoster(t *testing.T) {
	ch := make(chan workspace.RosterEntry, 1)
	m := model{width: 120, rosterUpserts: ch}

	m.Update(launchResultMsg{dir: "/r/a", launched: true, harnessKind: "opencode"})

	select {
	case e := <-ch:
		if e.Dir != "/r/a" || e.Harness != "opencode" || e.Provider != "opencode" {
			t.Errorf("roster entry = %+v, want dir=/r/a harness/provider=opencode", e)
		}
	default:
		t.Fatal("expected a roster upsert on harness override")
	}
}

func TestLaunchResultMsg_NoUpsertWithoutOverride(t *testing.T) {
	ch := make(chan workspace.RosterEntry, 1)
	m := model{width: 120, rosterUpserts: ch}

	m.Update(launchResultMsg{dir: "/r/a", launched: true}) // no harnessKind set

	select {
	case e := <-ch:
		t.Fatalf("unexpected roster upsert without an override: %+v", e)
	default:
	}
}

// ---------------------------------------------------------------------------
// Settings modal
// ---------------------------------------------------------------------------

func TestSettings_OpenCyclePersistClose(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"codex", "opencode"}}
	m := model{width: 120, input: newTestInput(), harnOp: ops}

	updated, _ := m.Update(keyMsg("S"))
	m = updated.(model)
	if m.prompt != promptSettings {
		t.Fatalf("S must open the settings modal; prompt = %v", m.prompt)
	}

	// Harness row (cursor 0): cycling right moves off "always ask" and persists.
	updated, _ = m.Update(keyMsg("right"))
	m = updated.(model)
	if m.settingsDefaultHarness == "" {
		t.Fatal("right on the harness row must select a concrete harness")
	}
	if cfg, _ := workspace.LoadConfig(); cfg.DefaultHarness != m.settingsDefaultHarness {
		t.Errorf("default harness not persisted: cfg=%q model=%q", cfg.DefaultHarness, m.settingsDefaultHarness)
	}

	// Launch-mode row: toggle and confirm it persists.
	updated, _ = m.Update(keyMsg("down"))
	m = updated.(model)
	updated, _ = m.Update(keyMsg("right"))
	m = updated.(model)
	if cfg, _ := workspace.LoadConfig(); cfg.LaunchMode != m.settingsLaunchMode {
		t.Errorf("launch mode not persisted: cfg=%q model=%q", cfg.LaunchMode, m.settingsLaunchMode)
	}

	updated, _ = m.Update(keyMsg("esc"))
	if got := updated.(model).prompt; got != promptIdle {
		t.Errorf("esc must close the settings modal; prompt = %v", got)
	}
}

func TestSettings_AlwaysAskClearsDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := workspace.SetDefaultHarness("opencode"); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"opencode"}}
	m := model{width: 120, input: newTestInput(), harnOp: ops}

	updated, _ := m.Update(keyMsg("S"))
	m = updated.(model)
	// options = ["", "opencode"], opened on "opencode" (index 1); cycling right
	// wraps back to "" (always ask).
	updated, _ = m.Update(keyMsg("right"))
	m = updated.(model)
	if m.settingsDefaultHarness != "" {
		t.Fatalf("cycling past the last harness must wrap to 'always ask', got %q", m.settingsDefaultHarness)
	}
	if cfg, _ := workspace.LoadConfig(); cfg.DefaultHarness != "" {
		t.Errorf("always-ask must clear the persisted default, got %q", cfg.DefaultHarness)
	}
}

func TestRenderSettings_ShowsValues(t *testing.T) {
	m := model{width: 120, settingsDefaultHarness: "codex", settingsLaunchMode: workspace.LaunchWindow}
	out := ansi.Strip(m.renderSettings(80))
	for _, want := range []string{"Settings", "default harness", "codex", "launch mode", "window"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings modal missing %q in:\n%s", want, out)
		}
	}
}
