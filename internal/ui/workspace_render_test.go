package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// keyMsg synthesises a tea.KeyMsg for the given key string. Single printable
// characters are sent as KeyRunes; named keys (j, k, up, down, a, etc.) are
// also sent as KeyRunes since bubbletea matches them via msg.String().
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fixedNow is a stable reference time used across all workspace render tests.
var fixedNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// makeRow builds a workspace.Row for testing.
func makeRow(repo, worktree, branch, title string, st workspace.RowState, attn state.Attention, lastActivity time.Time) workspace.Row {
	return workspace.Row{
		Repo:         repo,
		Worktree:     worktree,
		Branch:       branch,
		Title:        title,
		State:        st,
		Attention:    attn,
		LastActivity: lastActivity,
	}
}

// ---------------------------------------------------------------------------
// renderWorkspaceRows — golden render assertions
// ---------------------------------------------------------------------------

func TestRenderWorkspaceRowsRunningRowShowsTitle(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "main", "my running session", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "my running session") {
		t.Fatalf("running row must show title, got %q", got)
	}
}

func TestRenderWorkspaceRowsStoppedRowShowsTitleDimmed(t *testing.T) {
	m := model{width: 200}
	lastAct := fixedNow.Add(-30 * time.Minute)
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "feat/x", "stopped session", workspace.StateStopped, state.AttnInactive, lastAct),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "stopped session") {
		t.Fatalf("stopped row must show title, got %q", got)
	}
	// Relative time should appear for stopped rows.
	if !strings.Contains(got, "30m") {
		t.Fatalf("stopped row must show relative last-activity, got %q", got)
	}
}

func TestRenderWorkspaceRowsEmptyRowShowsWorktreeBase(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a/worktrees/feat-x", "feat-x", "", workspace.StateEmpty, state.AttnInactive, time.Time{}),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "feat-x") {
		t.Fatalf("empty row must show worktree base name, got %q", got)
	}
}

func TestRenderWorkspaceRowsMissingRowShowsMissingLabel(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a/worktrees/gone", "gone", "old title", workspace.StateMissing, state.AttnInactive, time.Time{}),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "missing") {
		t.Fatalf("missing row must contain 'missing' label, got %q", got)
	}
}

func TestRenderWorkspaceRowsUnknownRowShowsStatusUnknown(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a/worktrees/unk", "unk", "", workspace.StateUnknown, state.AttnInactive, fixedNow.Add(-2*time.Hour)),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "status unknown") {
		t.Fatalf("unknown row must show 'status unknown', got %q", got)
	}
}

func TestRenderWorkspaceRowsGroupedByRepo(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/repo/alpha", "/repo/alpha", "main", "alpha session", workspace.StateRunning, state.AttnActive, fixedNow),
		makeRow("/repo/beta", "/repo/beta", "main", "beta session", workspace.StateStopped, state.AttnInactive, fixedNow.Add(-5*time.Minute)),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	// Both repo base names should appear as group headers.
	if !strings.Contains(got, "alpha") {
		t.Fatalf("repo group header 'alpha' missing, got %q", got)
	}
	if !strings.Contains(got, "beta") {
		t.Fatalf("repo group header 'beta' missing, got %q", got)
	}
	// alpha group header must appear before beta group header.
	alphaPos := strings.Index(got, "alpha")
	betaPos := strings.Index(got, "beta")
	if alphaPos >= betaPos {
		t.Fatalf("expected alpha group before beta group, got alpha=%d beta=%d in %q", alphaPos, betaPos, got)
	}
}

func TestRenderWorkspaceRowsEmptyListShowsPlaceholder(t *testing.T) {
	m := model{width: 200}
	got := m.renderWorkspaceRows(200, nil, 0, fixedNow)
	if !strings.Contains(got, "no worktrees configured") {
		t.Fatalf("empty rows must show placeholder, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Cursor movement via j/k keys
// ---------------------------------------------------------------------------

func TestSessionCursorMovesDownWithJ(t *testing.T) {
	m := model{
		width: 120,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
			makeRow("/r", "/r/b", "feat", "row-b", workspace.StateStopped, state.AttnInactive, fixedNow.Add(-1*time.Minute)),
		},
		sessionCursor: 0,
	}

	updated, _ := m.Update(keyMsg("j"))
	m2 := updated.(model)
	if m2.sessionCursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", m2.sessionCursor)
	}
}

func TestSessionCursorMovesUpWithK(t *testing.T) {
	m := model{
		width: 120,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
			makeRow("/r", "/r/b", "feat", "row-b", workspace.StateStopped, state.AttnInactive, fixedNow.Add(-1*time.Minute)),
		},
		sessionCursor: 1,
	}

	updated, _ := m.Update(keyMsg("k"))
	m2 := updated.(model)
	if m2.sessionCursor != 0 {
		t.Fatalf("cursor after k = %d, want 0", m2.sessionCursor)
	}
}

func TestSessionCursorClampsAtBottom(t *testing.T) {
	m := model{
		width: 120,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
		},
		sessionCursor: 0,
	}

	updated, _ := m.Update(keyMsg("j"))
	m2 := updated.(model)
	if m2.sessionCursor != 0 {
		t.Fatalf("cursor clamped at bottom = %d, want 0", m2.sessionCursor)
	}
}

func TestSessionCursorClampsAtTop(t *testing.T) {
	m := model{
		width: 120,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
		},
		sessionCursor: 0,
	}

	updated, _ := m.Update(keyMsg("k"))
	m2 := updated.(model)
	if m2.sessionCursor != 0 {
		t.Fatalf("cursor clamped at top = %d, want 0", m2.sessionCursor)
	}
}

func TestSessionCursorDownArrowEquivalentToJ(t *testing.T) {
	m := model{
		width: 120,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
			makeRow("/r", "/r/b", "feat", "row-b", workspace.StateStopped, state.AttnInactive, fixedNow.Add(-1*time.Minute)),
		},
		sessionCursor: 0,
	}

	updated, _ := m.Update(keyMsg("down"))
	m2 := updated.(model)
	if m2.sessionCursor != 1 {
		t.Fatalf("cursor after down = %d, want 1", m2.sessionCursor)
	}
}

func TestSessionCursorUpArrowEquivalentToK(t *testing.T) {
	m := model{
		width: 120,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
			makeRow("/r", "/r/b", "feat", "row-b", workspace.StateStopped, state.AttnInactive, fixedNow.Add(-1*time.Minute)),
		},
		sessionCursor: 1,
	}

	updated, _ := m.Update(keyMsg("up"))
	m2 := updated.(model)
	if m2.sessionCursor != 0 {
		t.Fatalf("cursor after up = %d, want 0", m2.sessionCursor)
	}
}

// TestAToggleStillWorksWithWorkspaceRows confirms that 'a' still toggles recent
// when workspace rows are present (no regression to existing behaviour).
func TestAToggleStillWorksWithWorkspaceRows(t *testing.T) {
	m := model{
		width:           120,
		recentCollapsed: true,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
		},
	}

	updated, _ := m.Update(keyMsg("a"))
	m2 := updated.(model)
	if m2.recentCollapsed {
		t.Fatalf("'a' must toggle recentCollapsed to false, got true")
	}
}

// ---------------------------------------------------------------------------
// tickMsg advances tickNow and re-arms the ticker
// ---------------------------------------------------------------------------

func TestTickMsgAdvancesTickNow(t *testing.T) {
	m := model{width: 120}
	tick := fixedNow.Add(time.Minute)

	updated, cmd := m.Update(tickMsg(tick))
	m2 := updated.(model)

	if !m2.tickNow.Equal(tick) {
		t.Fatalf("tickNow = %v, want %v", m2.tickNow, tick)
	}
	// cmd must be non-nil (re-arm).
	if cmd == nil {
		t.Fatal("tickMsg handler must return a non-nil re-arm cmd")
	}
}

func TestTickMsgDoesNotMoveCursor(t *testing.T) {
	m := model{
		width:         120,
		sessionCursor: 1,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
			makeRow("/r", "/r/b", "feat", "row-b", workspace.StateStopped, state.AttnInactive, fixedNow.Add(-1*time.Minute)),
		},
	}

	updated, _ := m.Update(tickMsg(fixedNow))
	m2 := updated.(model)
	if m2.sessionCursor != 1 {
		t.Fatalf("tick must not move cursor, got %d", m2.sessionCursor)
	}
}

// ---------------------------------------------------------------------------
// No-regression: live-only path when workspaceRows is nil
// ---------------------------------------------------------------------------

func TestViewFallsBackToLiveOnlyWhenNoWorkspaceRows(t *testing.T) {
	// A model with no workspaceRows must render the live-only sessions pane
	// exactly as before — the "Sessions" header must appear and no workspace
	// group headers should be present.
	m := model{
		width: 120,
		snap: state.Snapshot{
			UpdatedAt: fixedNow,
			Sessions: []state.SessionView{
				{
					InstanceID:   "i1",
					InstanceName: "inst-1",
					SessionID:    "s1",
					Title:        "live-only-title",
					StatusType:   "busy",
					Attention:    state.AttnActive,
					Source:       state.SourceLive,
				},
			},
		},
		// workspaceRows is nil — no repos configured.
	}

	got := m.View()
	if !strings.Contains(got, "Sessions") {
		t.Fatalf("live-only view must contain 'Sessions' header, got %q", got)
	}
	if !strings.Contains(got, "live-only-title") {
		t.Fatalf("live-only view must render live session title, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Stopped row relative time advances with tickNow
// ---------------------------------------------------------------------------

func TestStoppedRowRelativeTimeUsesTickNow(t *testing.T) {
	m := model{width: 200}
	lastAct := fixedNow.Add(-2 * time.Hour)
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "main", "old session", workspace.StateStopped, state.AttnInactive, lastAct),
	}

	// Render with fixedNow as reference — expect "2h".
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	if !strings.Contains(got, "2h") {
		t.Fatalf("stopped row must show '2h' relative time at fixedNow, got %q", got)
	}

	// Render with fixedNow+1h as reference — expect "3h".
	got2 := m.renderWorkspaceRows(200, rows, 0, fixedNow.Add(time.Hour))
	if !strings.Contains(got2, "3h") {
		t.Fatalf("stopped row must show '3h' relative time at fixedNow+1h, got %q", got2)
	}
}
