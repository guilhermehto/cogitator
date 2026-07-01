package ui

// sessionpalette_test.go — unit tests for the ctrl+P session switcher: opening
// the palette, live fuzzy filtering, cursor clamping, and the jump-on-enter
// dispatch (including the tmux-unavailable / missing / creating guards). All
// tmux operations are injected via fakeTmuxOps; no real tmux server is used.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// openPalette opens the session switcher on m by sending ctrl+P and returns the
// resulting model.
func openPalette(t *testing.T, m model) model {
	t.Helper()
	updated, _ := m.Update(keyMsg("ctrl+p"))
	m2 := updated.(model)
	if m2.prompt != promptSwitchSession {
		t.Fatalf("ctrl+P should open the switcher; prompt = %v", m2.prompt)
	}
	return m2
}

func TestCtrlP_OpensPaletteWithAllRows(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", workspace.StateRunning, state.AttnActive, fixedNow),
	})

	m2 := openPalette(t, m)

	if len(m2.sessionPaletteMatches) != 2 {
		t.Errorf("empty query should match all rows; got %d matches", len(m2.sessionPaletteMatches))
	}
	if len(m2.sessionPaletteLabels) != 2 {
		t.Fatalf("expected 2 labels, got %v", m2.sessionPaletteLabels)
	}
	if m2.sessionPaletteLabels[0] != "alpha main" || m2.sessionPaletteLabels[1] != "beta dev" {
		t.Errorf("labels = %v, want [\"alpha main\" \"beta dev\"]", m2.sessionPaletteLabels)
	}
}

func TestCtrlP_NoRowsSetsHintAndStaysIdle(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, nil)

	updated, cmd := m.Update(keyMsg("ctrl+p"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("ctrl+P with no rows must not open the palette; prompt = %v", m2.prompt)
	}
	if cmd != nil {
		t.Error("ctrl+P with no rows must return a nil cmd")
	}
	if m2.tmuxHint == "" {
		t.Error("ctrl+P with no rows must set a hint")
	}
}

func TestSessionPalette_FiltersOnType(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", workspace.StateRunning, state.AttnActive, fixedNow),
	})
	m = openPalette(t, m)

	// 'b' appears only in "beta dev", not in "alpha main".
	updated, _ := m.Update(keyMsg("b"))
	m2 := updated.(model)

	if len(m2.sessionPaletteMatches) != 1 {
		t.Fatalf("typing 'b' should leave one match; got %d", len(m2.sessionPaletteMatches))
	}
	if label := m2.sessionPaletteLabels[m2.sessionPaletteMatches[0]]; label != "beta dev" {
		t.Errorf("filtered match = %q, want \"beta dev\"", label)
	}
}

func TestSessionPalette_NavigationClamps(t *testing.T) {
	m := model{width: 120, prompt: promptSwitchSession, input: textinput.New()}
	m.sessionPaletteMatches = []int{0, 1}

	up, _ := m.Update(keyMsg("up"))
	if c := up.(model).sessionPaletteCursor; c != 0 {
		t.Errorf("up at top: cursor = %d, want 0", c)
	}

	down, _ := m.Update(keyMsg("down"))
	m = down.(model)
	if m.sessionPaletteCursor != 1 {
		t.Fatalf("down: cursor = %d, want 1", m.sessionPaletteCursor)
	}
	down2, _ := m.Update(keyMsg("down"))
	if c := down2.(model).sessionPaletteCursor; c != 1 {
		t.Errorf("down past end: cursor = %d, want 1 (clamped)", c)
	}
}

func TestSessionPalette_EnterJumpsAndSyncsCursor(t *testing.T) {
	tmuxFake := &fakeTmuxOps{
		available:        true,
		findWindowResult: "beta:1",
		processAlive:     true,
	}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", workspace.StateRunning, state.AttnActive, fixedNow),
	})
	m = openPalette(t, m)

	// Filter to "beta" so the second row is selected, then jump.
	updated, _ := m.Update(keyMsg("b"))
	m = updated.(model)
	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("enter must close the palette; prompt = %v", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("enter on a resumable row must dispatch a launch cmd")
	}
	if m2.sessionCursor != 1 {
		t.Errorf("sessionCursor must sync to the chosen row; got %d, want 1", m2.sessionCursor)
	}
	if msg, ok := runCmd(cmd).(launchResultMsg); !ok {
		t.Fatalf("expected launchResultMsg, got %T", msg)
	}
	if len(tmuxFake.selectCalls) != 1 || tmuxFake.selectCalls[0] != "beta:1" {
		t.Errorf("expected Select(beta:1), got %v", tmuxFake.selectCalls)
	}
}

func TestSessionPalette_EnterTmuxUnavailableSetsHint(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: false}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})
	m = openPalette(t, m)

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if cmd != nil {
		t.Error("enter with tmux unavailable must return a nil cmd")
	}
	if !strings.Contains(m2.tmuxHint, "tmux") {
		t.Errorf("hint must mention tmux; got %q", m2.tmuxHint)
	}
	if len(tmuxFake.selectCalls) != 0 {
		t.Errorf("no tmux select should be attempted; got %v", tmuxFake.selectCalls)
	}
}

func TestSessionPalette_EnterMissingRowSetsHint(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: true}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "a", workspace.StateMissing, state.AttnInactive, fixedNow),
	})
	m = openPalette(t, m)

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if cmd != nil {
		t.Error("enter on a missing row must return a nil cmd")
	}
	if !strings.Contains(m2.tmuxHint, "missing") {
		t.Errorf("hint must explain the row is missing; got %q", m2.tmuxHint)
	}
	if len(tmuxFake.findWindowCalls) != 0 {
		t.Errorf("no tmux lookup should be attempted for a missing row; got %v", tmuxFake.findWindowCalls)
	}
}

// jumpTo opens the palette, filters to a single row via a distinguishing
// character, and presses enter — recording a session switch. It returns the
// resulting model.
func jumpTo(t *testing.T, m model, filter string) model {
	t.Helper()
	m = openPalette(t, m)
	updated, _ := m.Update(keyMsg(filter))
	m = updated.(model)
	updated, _ = m.Update(keyMsg("enter"))
	return updated.(model)
}

func TestSessionPalette_OrdersByMostRecentlySwitched(t *testing.T) {
	tmuxFake := &fakeTmuxOps{available: true}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/gamma", "/home/me/gamma", "wip", "g", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	// Jump to beta, then gamma — gamma is now the current session, beta the previous.
	m = jumpTo(t, m, "b")
	m = jumpTo(t, m, "g")

	m = openPalette(t, m)

	want := []string{"gamma wip", "beta dev", "alpha main"}
	for i, w := range want {
		if m.sessionPaletteLabels[i] != w {
			t.Errorf("row %d = %q, want %q (most-recently-switched first, then alphabetical)", i, m.sessionPaletteLabels[i], w)
		}
	}
	// Cursor starts on the previous session (beta) so ctrl+P then enter returns to it.
	if m.sessionPaletteCursor != 1 {
		t.Errorf("cursor = %d, want 1 (previous session)", m.sessionPaletteCursor)
	}
}

func TestSessionPalette_CursorStartsAtTopWithoutHistory(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", workspace.StateStopped, state.AttnInactive, fixedNow),
	})

	m = openPalette(t, m)

	// No switches recorded yet: no genuine "previous", so the cursor stays on top.
	if m.sessionPaletteCursor != 0 {
		t.Errorf("cursor = %d, want 0 with no switch history", m.sessionPaletteCursor)
	}
}

func TestSessionPalette_FixedHeightWhileFiltering(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/gamma", "/home/me/gamma", "wip", "g", workspace.StateStopped, state.AttnInactive, fixedNow),
	})
	m = openPalette(t, m)

	// A pane tall enough to hold the full fixed row count.
	const fieldW, fieldH = 80, 24
	lineCount := func(s string) int { return len(strings.Split(s, "\n")) }

	full := lineCount(m.renderSessionPalette(fieldW, fieldH))

	// Narrow to a single match; the box must not shrink.
	updated, _ := m.Update(keyMsg("b")) // 'b' matches only "beta dev"
	m = updated.(model)
	if len(m.sessionPaletteMatches) != 1 {
		t.Fatalf("setup: expected 1 match after filtering, got %d", len(m.sessionPaletteMatches))
	}
	narrowed := lineCount(m.renderSessionPalette(fieldW, fieldH))

	if full != narrowed {
		t.Errorf("palette height changed while filtering: %d lines full, %d narrowed", full, narrowed)
	}
}

func TestSessionPalette_EscCloses(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/r", "/r/a", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})
	m = openPalette(t, m)

	updated, _ := m.Update(keyMsg("esc"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("esc must close the palette; prompt = %v", m2.prompt)
	}
	if m2.sessionPaletteRows != nil || m2.sessionPaletteMatches != nil || m2.sessionPaletteLabels != nil {
		t.Errorf("esc must reset palette state; rows=%v labels=%v matches=%v",
			m2.sessionPaletteRows, m2.sessionPaletteLabels, m2.sessionPaletteMatches)
	}
}
