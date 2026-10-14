package ui

// sessionpalette_test.go — unit tests for the ctrl+P session switcher: opening
// the palette, live fuzzy filtering, cursor clamping, and the jump-on-enter
// dispatch (including the tmux-unavailable / missing / creating guards). All
// tmux operations are injected via fakeMux; no real tmux server is used.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/settings"
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

// openSearch opens the lightweight '/' session search on m and returns the
// resulting model.
func openSearch(t *testing.T, m model) model {
	t.Helper()
	updated, _ := m.Update(keyMsg("/"))
	m2 := updated.(model)
	if m2.prompt != promptSearchSession {
		t.Fatalf("'/' should open session search; prompt = %v", m2.prompt)
	}
	return m2
}

func TestSlash_OpensSearchOnlyInSessionsView(t *testing.T) {
	rows := []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
	}
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, rows)

	m2 := openSearch(t, m)
	if len(m2.sessionPaletteMatches) != 1 {
		t.Fatalf("empty search should show every row; got %d matches", len(m2.sessionPaletteMatches))
	}
	if m2.input.Placeholder != "search sessions" {
		t.Errorf("placeholder = %q, want %q", m2.input.Placeholder, "search sessions")
	}

	m.view = viewWorkspaces
	updated, cmd := m.Update(keyMsg("/"))
	m3 := updated.(model)
	if m3.prompt != promptIdle {
		t.Errorf("'/' in Workspaces view must stay idle; prompt = %v", m3.prompt)
	}
	if cmd != nil {
		t.Error("'/' in Workspaces view must return a nil cmd")
	}
}

func TestSlash_NoRowsSetsHintAndStaysIdle(t *testing.T) {
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, nil)

	updated, cmd := m.Update(keyMsg("/"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("'/' with no rows must stay idle; prompt = %v", m2.prompt)
	}
	if cmd != nil {
		t.Error("'/' with no rows must return a nil cmd")
	}
	if m2.tmuxHint != "no sessions to search" {
		t.Errorf("hint = %q, want %q", m2.tmuxHint, "no sessions to search")
	}
}

func TestSessionSearch_EnterMovesCursorWithoutJumping(t *testing.T) {
	tmuxFake := &fakeMux{available: true}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []settings.Row{
		// The scattered match appears first in list order, while fuzzy ranking
		// should put the contiguous match first after typing "abc".
		makeRow("/r", "/r/xaybzc", "xaybzc", "scattered", settings.StateRunning, state.AttnActive, fixedNow),
		makeRow("/r", "/r/abc", "abc", "contiguous", settings.StateRunning, state.AttnActive, fixedNow),
	})
	m = openSearch(t, m)

	for _, ch := range "abc" {
		updated, _ := m.Update(keyMsg(string(ch)))
		m = updated.(model)
	}
	if got := m.sessionPaletteMatches[0]; got != 1 {
		t.Fatalf("top fuzzy match index = %d, want 1", got)
	}

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("enter must close search; prompt = %v", m2.prompt)
	}
	if cmd != nil {
		t.Fatal("search enter must not dispatch a jump/resume command")
	}
	if m2.sessionCursor != 1 {
		t.Errorf("session cursor = %d, want 1", m2.sessionCursor)
	}
	if len(tmuxFake.findWindowCalls) != 0 || len(tmuxFake.selectCalls) != 0 {
		t.Errorf("search enter must not touch tmux; find=%v select=%v", tmuxFake.findWindowCalls, tmuxFake.selectCalls)
	}
	if len(m2.switchOrder) != 0 {
		t.Errorf("search enter must not update switch history; got %v", m2.switchOrder)
	}
}

func TestSessionSearch_EscKeepsOriginalCursor(t *testing.T) {
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/r", "/r/a", "alpha", "a", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/r", "/r/b", "beta", "b", settings.StateStopped, state.AttnInactive, fixedNow),
	})
	m.sessionCursor = 1
	m = openSearch(t, m)

	updated, _ := m.Update(keyMsg("esc"))
	m2 := updated.(model)
	if m2.sessionCursor != 1 {
		t.Errorf("esc moved session cursor to %d, want 1", m2.sessionCursor)
	}
}

// mixedTabsModel builds a model with an item in every tab: two worktree rows
// (Repos), one workspace session (Workspaces), and one agent (Agents).
func mixedTabsModel(tmuxFake *fakeMux) model {
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", settings.StateRunning, state.AttnActive, fixedNow),
	})
	m.wsStatuses = []workspace.WorkspaceStatus{
		wsStatusWithSession("payments", wsSession("Feature X", "feature-x", "/root/payments/feature-x", "fake", settings.StateStopped)),
	}
	m.agents = []agents.Agent{{
		ID: "/home/me/stray", Dir: "/home/me/stray", Kind: agents.KindStray, State: settings.StateRunning, Attention: state.AttnActive,
	}}
	return m
}

func TestCtrlP_ListsOnlyTheActiveTabsItems(t *testing.T) {
	tests := []struct {
		view            viewMode
		wantLabels      []string
		wantPlaceholder string
	}{
		{viewSessions, []string{"alpha main", "beta dev"}, "go to worktree"},
		{viewWorkspaces, []string{"payments/Feature X"}, "go to workspace session"},
		{viewAgents, []string{"/home/me/stray stray"}, "go to agent"},
	}
	for _, tt := range tests {
		m := mixedTabsModel(&fakeMux{available: true})
		m.view = tt.view

		m = openPalette(t, m)

		if strings.Join(m.sessionPaletteLabels, "|") != strings.Join(tt.wantLabels, "|") {
			t.Errorf("view %v: labels = %v, want %v", tt.view, m.sessionPaletteLabels, tt.wantLabels)
		}
		if len(m.sessionPaletteMatches) != len(tt.wantLabels) {
			t.Errorf("view %v: empty query should match every item; got %d matches", tt.view, len(m.sessionPaletteMatches))
		}
		if m.input.Placeholder != tt.wantPlaceholder {
			t.Errorf("view %v: placeholder = %q, want %q", tt.view, m.input.Placeholder, tt.wantPlaceholder)
		}
	}
}

func TestCtrlP_EmptyTabSetsTabSpecificHintAndStaysIdle(t *testing.T) {
	tests := []struct {
		view     viewMode
		wantHint string
		hint     func(model) string
	}{
		{viewSessions, "no worktrees to switch to", func(m model) string { return m.tmuxHint }},
		{viewWorkspaces, "no workspace sessions to switch to", func(m model) string { return m.wsHint }},
		{viewAgents, "no agents to switch to", func(m model) string { return m.tmuxHint }},
	}
	for _, tt := range tests {
		m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, nil)
		m.view = tt.view

		updated, cmd := m.Update(keyMsg("ctrl+p"))
		m2 := updated.(model)

		if m2.prompt != promptIdle {
			t.Errorf("view %v: ctrl+P with nothing to switch to must not open the palette; prompt = %v", tt.view, m2.prompt)
		}
		if cmd != nil {
			t.Errorf("view %v: ctrl+P with nothing to switch to must return a nil cmd", tt.view)
		}
		if got := tt.hint(m2); got != tt.wantHint {
			t.Errorf("view %v: hint = %q, want %q", tt.view, got, tt.wantHint)
		}
	}
}

func TestSessionPalette_FiltersOnType(t *testing.T) {
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", settings.StateRunning, state.AttnActive, fixedNow),
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
	tmuxFake := &fakeMux{
		available:        true,
		findWindowResult: "beta:1",
		processAlive:     true,
	}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", settings.StateRunning, state.AttnActive, fixedNow),
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
	tmuxFake := &fakeMux{available: false}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/r", "/r/a", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
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
	tmuxFake := &fakeMux{available: true}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/r", "/r/a", "main", "a", settings.StateMissing, state.AttnInactive, fixedNow),
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
	tmuxFake := &fakeMux{available: true}
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/gamma", "/home/me/gamma", "wip", "g", settings.StateStopped, state.AttnInactive, fixedNow),
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

func TestSessionPalette_TypingResetsCursorToTop(t *testing.T) {
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/gamma", "/home/me/gamma", "wip", "g", settings.StateStopped, state.AttnInactive, fixedNow),
	})

	// Seed a previous session so the palette opens on row 1, then search.
	m = jumpTo(t, m, "b")
	m = jumpTo(t, m, "g")
	m = openPalette(t, m)
	if m.sessionPaletteCursor != 1 {
		t.Fatalf("precondition: cursor = %d, want 1 (previous session)", m.sessionPaletteCursor)
	}

	updated, _ := m.Update(keyMsg("a"))
	if c := updated.(model).sessionPaletteCursor; c != 0 {
		t.Errorf("cursor = %d after typing, want 0 (first match)", c)
	}
}

func TestSessionPalette_CursorStartsAtTopWithoutHistory(t *testing.T) {
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", settings.StateStopped, state.AttnInactive, fixedNow),
	})

	m = openPalette(t, m)

	// No switches recorded yet: no genuine "previous", so the cursor stays on top.
	if m.sessionPaletteCursor != 0 {
		t.Errorf("cursor = %d, want 0 with no switch history", m.sessionPaletteCursor)
	}
}

func TestSessionPalette_FixedHeightWhileFiltering(t *testing.T) {
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", settings.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/gamma", "/home/me/gamma", "wip", "g", settings.StateStopped, state.AttnInactive, fixedNow),
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

// ---------------------------------------------------------------------------
// Workspaces tab
// ---------------------------------------------------------------------------

// twoWorkspacesModel builds a Workspaces-tab model with the cursor on
// payments/Feature X. Cursor entries: 0 payments, 1 Feature X, 2 search,
// 3 Spike.
func twoWorkspacesModel(tmuxFake *fakeMux) model {
	m := makeTestModel(tmuxFake, nil, &fakeHarnessOps{argv: []string{"fake"}}, nil)
	m.height = 40
	m.view = viewWorkspaces
	m.wsStatuses = []workspace.WorkspaceStatus{
		wsStatusWithSession("payments", wsSession("Feature X", "feature-x", "/root/payments/feature-x", "fake", settings.StateStopped)),
		wsStatusWithSession("search", wsSession("Spike", "spike", "/root/search/spike", "fake", settings.StateStopped)),
	}
	m.wsCursor = 1
	return m
}

func TestSessionPalette_WorkspacesEnterLaunchesSessionAndMovesCursorOntoIt(t *testing.T) {
	tmuxFake := &fakeMux{
		available:          true,
		findWindowErr:      mux.ErrWindowNotFound,
		ensureWindowResult: "search/Spike:0",
	}
	m := openPalette(t, twoWorkspacesModel(tmuxFake))

	// 'k' appears only in "search/Spike".
	updated, _ := m.Update(keyMsg("k"))
	m = updated.(model)
	if len(m.sessionPaletteMatches) != 1 {
		t.Fatalf("filtering to 'k' should leave one match; got %d", len(m.sessionPaletteMatches))
	}

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("enter must close the palette; prompt = %v", m2.prompt)
	}
	if m2.wsCursor != 3 {
		t.Errorf("wsCursor = %d, want 3 (the search/Spike row)", m2.wsCursor)
	}
	if cmd == nil {
		t.Fatal("enter on a workspace session must dispatch a launch cmd")
	}
	if _, ok := runCmd(cmd).(launchResultMsg); !ok {
		t.Fatal("expected launchResultMsg")
	}
	if len(tmuxFake.ensureWindowCalls) != 1 {
		t.Fatalf("expected 1 EnsureWindowMode call, got %d", len(tmuxFake.ensureWindowCalls))
	}
	ensure := tmuxFake.ensureWindowCalls[0]
	if ensure.dir != "/root/search/spike" || ensure.name != "search/Spike" {
		t.Errorf("EnsureWindowMode = dir %q name %q, want session dir and \"search/Spike\"", ensure.dir, ensure.name)
	}
	if !m2.hasSwitchRecord("/root/search/spike") {
		t.Error("switching to a workspace session must record it for MRU ordering")
	}
}

func TestSessionPalette_WorkspacesEnterWithoutTmuxShowsWorkspaceHint(t *testing.T) {
	tmuxFake := &fakeMux{available: false}
	m := openPalette(t, twoWorkspacesModel(tmuxFake))

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if cmd != nil {
		t.Error("enter with tmux unavailable must return a nil cmd")
	}
	if !strings.Contains(m2.wsHint, "tmux") {
		t.Errorf("Workspaces hint must mention tmux; got %q", m2.wsHint)
	}
}

func TestSessionPalette_WorkspacesSeedsCursorOnPreviousSession(t *testing.T) {
	m := twoWorkspacesModel(&fakeMux{available: true})

	m = jumpTo(t, m, "k") // search/Spike
	m = jumpTo(t, m, "x") // payments/Feature X
	m = openPalette(t, m)

	if got := strings.Join(m.sessionPaletteLabels, "|"); got != "payments/Feature X|search/Spike" {
		t.Errorf("labels = %q, want most-recently-switched first", got)
	}
	if m.sessionPaletteCursor != 1 {
		t.Errorf("cursor = %d, want 1 (previous session)", m.sessionPaletteCursor)
	}
}

func TestCtrlP_ExcludesPendingWorkspaceSessionPlaceholder(t *testing.T) {
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, nil)
	m.view = viewWorkspaces
	m.wsStatuses = []workspace.WorkspaceStatus{
		wsStatusWithSession("payments", wsSession("Feature X", "", "", "", settings.StateCreating)),
	}

	updated, cmd := m.Update(keyMsg("ctrl+p"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("a pending-only workspace session must not open the palette; prompt = %v", m2.prompt)
	}
	if cmd != nil {
		t.Error("no jumpable candidates must return a nil cmd")
	}
	if m2.wsHint != "no workspace sessions to switch to" {
		t.Errorf("hint must explain there is nothing to switch to, got %q", m2.wsHint)
	}
}

// ---------------------------------------------------------------------------
// Agents tab
// ---------------------------------------------------------------------------

// agentsPaletteModel builds an Agents-tab model with one running worktree
// agent per branch, each labelled "repo·<branch>". Rows live in temp dirs,
// so on macOS the canonical agent IDs differ from the raw worktree paths.
func agentsPaletteModel(t *testing.T, tmuxFake *fakeMux, branches ...string) model {
	t.Helper()

	var rows []settings.Row
	for _, branch := range branches {
		row := worktreeRow(t.TempDir(), settings.StateRunning, state.AttnActive)
		row.Branch = branch
		rows = append(rows, row)
	}
	m := feedTestModel(tmuxFake, nil)
	m.view = viewAgents
	m.agentsList = newAgentList()

	return rebuildAgents(t, m, rows)
}

func agentOnBranch(t *testing.T, m model, branch string) agents.Agent {
	t.Helper()

	for _, a := range m.agents {
		if a.Branch == branch {
			return a
		}
	}
	t.Fatalf("no agent on branch %q in %+v", branch, m.agents)
	return agents.Agent{}
}

func rowOnBranch(t *testing.T, m model, branch string) settings.Row {
	t.Helper()

	for _, r := range m.workspaceRows {
		if r.Branch == branch {
			return r
		}
	}
	t.Fatalf("no row on branch %q", branch)
	return settings.Row{}
}

func TestSessionPalette_AgentsEnterFocusesAgentAndMovesListCursor(t *testing.T) {
	tmuxFake := &fakeMux{available: true, findWindowResult: "s:1", processAlive: true}
	m := agentsPaletteModel(t, tmuxFake, "alpha", "beta")
	beta := agentOnBranch(t, m, "beta")
	m.agentsList.cursorID = agentOnBranch(t, m, "alpha").ID
	m = openPalette(t, m)

	updated, _ := m.Update(keyMsg("b")) // only "repo·beta" has a 'b'
	m = updated.(model)
	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("enter must close the palette; prompt = %v", m2.prompt)
	}
	if m2.agentsList.cursorID != beta.ID {
		t.Errorf("agents cursor = %q, want %q", m2.agentsList.cursorID, beta.ID)
	}
	if cmd == nil {
		t.Fatal("enter on an agent must dispatch the focus cmd")
	}
	if _, ok := runCmd(cmd).(launchResultMsg); !ok {
		t.Fatal("expected launchResultMsg")
	}
	if len(tmuxFake.selectCalls) != 1 || tmuxFake.selectCalls[0] != "s:1" {
		t.Errorf("expected Select(s:1), got %v", tmuxFake.selectCalls)
	}
	if !m2.hasSwitchRecord(rowOnBranch(t, m2, "beta").Worktree) {
		t.Error("focusing an agent must record it for MRU ordering")
	}
}

func TestSessionPalette_AgentsSeedsCursorOnPreviousAgent(t *testing.T) {
	m := agentsPaletteModel(t, &fakeMux{available: true}, "alpha", "beta", "gamma")

	m = jumpTo(t, m, "b")
	m = jumpTo(t, m, "g")
	m = openPalette(t, m)

	if got := m.sessionPaletteLabels[:2]; got[0] != "repo·gamma" || got[1] != "repo·beta" {
		t.Errorf("top labels = %v, want [repo·gamma repo·beta] (most-recently-switched first)", got)
	}
	if m.sessionPaletteCursor != 1 {
		t.Fatalf("cursor = %d, want 1 (previous agent)", m.sessionPaletteCursor)
	}

	updated, _ := m.Update(keyMsg("enter"))
	if got, want := updated.(model).agentsList.cursorID, agentOnBranch(t, m, "beta").ID; got != want {
		t.Errorf("ctrl+P then enter landed on %q, want previous agent %q", got, want)
	}
}

func TestSessionPalette_AgentsOrderSharesHistoryWithReposTab(t *testing.T) {
	m := agentsPaletteModel(t, &fakeMux{available: true}, "alpha", "beta")
	m.view = viewSessions
	m = jumpTo(t, m, "b") // the beta worktree, from the Repos tab

	m.view = viewAgents
	m = openPalette(t, m)

	if m.sessionPaletteLabels[0] != "repo·beta" {
		t.Errorf("labels = %v, want the beta agent first after switching to its worktree", m.sessionPaletteLabels)
	}
}

func TestSessionPalette_EscCloses(t *testing.T) {
	m := makeTestModel(&fakeMux{available: true}, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/r", "/r/a", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
	})
	m = openPalette(t, m)

	updated, _ := m.Update(keyMsg("esc"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("esc must close the palette; prompt = %v", m2.prompt)
	}
	if m2.sessionPaletteCandidates != nil || m2.sessionPaletteMatches != nil || m2.sessionPaletteLabels != nil {
		t.Errorf("esc must reset palette state; candidates=%v labels=%v matches=%v",
			m2.sessionPaletteCandidates, m2.sessionPaletteLabels, m2.sessionPaletteMatches)
	}
}
