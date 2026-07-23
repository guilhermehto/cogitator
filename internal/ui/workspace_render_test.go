package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// wsAnsiRe strips SGR escape sequences so tests can assert on visible text and
// column order independent of styling.
var wsAnsiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func wsStripANSI(s string) string { return wsAnsiRe.ReplaceAllString(s, "") }

// wsRowLineContaining returns the first rendered line whose visible text
// contains want, or "" if none match.
func wsRowLineContaining(rendered, want string) string {
	for _, l := range strings.Split(rendered, "\n") {
		if strings.Contains(wsStripANSI(l), want) {
			return l
		}
	}
	return ""
}

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

func TestRenderWorkspaceRowsRunningRowShowsBranch(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "feat/login", "running session", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	row := wsRowLineContaining(got, "running session")
	if row == "" {
		t.Fatal("running row not found")
	}
	if !strings.Contains(wsStripANSI(row), "feat/login") {
		t.Fatalf("running row must show its branch annotation, got %q", row)
	}
}

// TestRenderWorkspaceRowsBranchLeadsSessionTitle asserts the worktree branch is
// rendered before the session title — the branch is the primary, navigable
// identity for a row, so it leads.
func TestRenderWorkspaceRowsBranchLeadsSessionTitle(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "feat/login", "running session", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	line := wsStripANSI(wsRowLineContaining(got, "running session"))
	if line == "" {
		t.Fatal("running row not found")
	}
	branchPos := strings.Index(line, "feat/login")
	titlePos := strings.Index(line, "running session")
	if branchPos < 0 || titlePos < 0 {
		t.Fatalf("row must show both branch and title, got %q", line)
	}
	if branchPos >= titlePos {
		t.Fatalf("branch must lead the session title: branch=%d title=%d in %q", branchPos, titlePos, line)
	}
}

// TestRenderWorkspaceRowsLongSessionTitleTruncated asserts a long session title
// is capped with an ellipsis while the branch is shown in full.
func TestRenderWorkspaceRowsLongSessionTitleTruncated(t *testing.T) {
	m := model{width: 200}
	longTitle := "This is a very long session title that must be truncated"
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "feat/auth", longTitle, workspace.StateRunning, state.AttnActive, fixedNow),
	}
	got := wsStripANSI(m.renderWorkspaceRows(200, rows, 0, fixedNow))
	if strings.Contains(got, longTitle) {
		t.Fatalf("long session title must be truncated, but full title appeared in %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("truncated session title must end with an ellipsis, got %q", got)
	}
	if !strings.Contains(got, "feat/auth") {
		t.Fatalf("branch must be shown in full, got %q", got)
	}
	if !strings.Contains(got, "This is a") {
		t.Fatalf("a leading portion of the session title must still be shown, got %q", got)
	}
}

func TestRenderWorkspaceRowsMultilineSessionTitleStaysOneRow(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "feat/auth", "first line\nsecond line", workspace.StateRunning, state.AttnActive, fixedNow),
	}

	got := wsStripANSI(m.renderWorkspaceRows(200, rows, 0, fixedNow))
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("multiline session title must not add rendered rows; got %d lines in %q", len(lines), got)
	}
	if !strings.Contains(lines[2], "first line second line") {
		t.Fatalf("row = %q, want folded session title", lines[2])
	}
}

func TestRenderWorkspaceRowsRepoHeaderShowsRepoPath(t *testing.T) {
	m := model{width: 200}
	rows := []workspace.Row{
		makeRow("/srv/code/myrepo", "/srv/code/myrepo", "main", "a session", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)
	header := wsRowLineContaining(got, "myrepo")
	if header == "" {
		t.Fatal("repo header not found")
	}
	if !strings.Contains(wsStripANSI(header), "/srv/code/myrepo") {
		t.Fatalf("repo header must show the full repo path, got %q", header)
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
		makeRow("/repo/a", "/repo/a/worktrees/feat-x", "feat-x", "", workspace.StateStopped, state.AttnInactive, time.Time{}),
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
// Status column placement and cursor highlight
// ---------------------------------------------------------------------------

// TestWorkspaceRowStatusBadgeIsLeftmostColumn asserts the session status badge
// renders in the left-most column — before the title — rather than on the far
// right. A permission-pending running row must show the permission glyph ahead
// of its title text.
func TestWorkspaceRowStatusBadgeIsLeftmostColumn(t *testing.T) {
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a/wt/perm", "feat/perm", "needs permission", workspace.StateRunning, state.AttnPermissionPending, fixedNow),
	}
	m := model{width: 120}
	line := wsRowLineContaining(m.renderWorkspaceRows(120, rows, 0, fixedNow), "needs permission")
	if line == "" {
		t.Fatal("row line for 'needs permission' not found")
	}
	badge := strings.Index(line, glyphPermission)
	title := strings.Index(line, "needs permission")
	if badge < 0 {
		t.Fatalf("permission badge glyph missing from row %q", line)
	}
	if badge >= title {
		t.Fatalf("status badge must be left of the title: badge=%d title=%d in %q", badge, title, line)
	}
}

// TestSelectedWorkspaceRowHighlightKeepsColour asserts the cursor row is
// highlighted with a background band (wtSelectedBg) that spans the whole row
// while preserving the row's foreground colours. lipgloss emits an SGR reset
// after every coloured cell that also clears the background, so the band must
// be re-asserted after each interior reset: we check the row opens with the
// background, that a reset is immediately followed by the background opener,
// and that a foreground colour still survives in the highlighted row.
func TestSelectedWorkspaceRowHighlightKeepsColour(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	orig := r.ColorProfile()
	r.SetColorProfile(termenv.ANSI256)
	defer r.SetColorProfile(orig)

	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "main", "cursor row", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	m := model{width: 80}
	line := wsRowLineContaining(m.renderWorkspaceRows(80, rows, 0, fixedNow), "cursor row")
	if line == "" {
		t.Fatal("cursor row not found")
	}

	open, reset, _ := strings.Cut(
		lipgloss.NewStyle().Background(wtSelectedBg).Render("\x00"), "\x00")
	if open == "" {
		t.Fatal("expected a non-empty background opener under ANSI256")
	}
	if !strings.HasPrefix(line, open) {
		t.Fatalf("selected row must open with the selection background, got %q", line)
	}
	if !strings.Contains(line, reset+open) {
		t.Fatalf("selection background must be re-asserted after interior resets, got %q", line)
	}
	if !strings.Contains(line, "\x1b[38") {
		t.Fatalf("selected row must preserve foreground colour, got %q", line)
	}
	if strings.Contains(line, "\x1b[7m") {
		t.Fatalf("selected row must not use reverse video, got %q", line)
	}
}

// TestUnselectedWorkspaceRowKeepsColour guards the test above: an unselected
// running row still carries its colour styling and is never reverse-video, so
// the highlight test is exercising the background-band path, not a no-op.
func TestUnselectedWorkspaceRowKeepsColour(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	orig := r.ColorProfile()
	r.SetColorProfile(termenv.ANSI256)
	defer r.SetColorProfile(orig)

	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "main", "plain row", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	m := model{width: 80}
	// cursor on a different (non-existent) index so this row is not selected.
	line := wsRowLineContaining(m.renderWorkspaceRows(80, rows, -1, fixedNow), "plain row")
	if line == "" {
		t.Fatal("row not found")
	}
	if strings.Contains(line, "\x1b[7m") {
		t.Fatalf("unselected row must not be reverse-video, got %q", line)
	}
	if !strings.Contains(line, "\x1b[38") {
		t.Fatalf("unselected running row should carry a foreground colour, got %q", line)
	}
}

// ---------------------------------------------------------------------------
// Sessions viewport scrolling
// ---------------------------------------------------------------------------

func TestRenderWorkspaceRowsViewportKeepsCursorVisible(t *testing.T) {
	rows := []workspace.Row{
		makeRow("/a", "/a/1", "a-1", "row-a-1", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/a", "/a/2", "a-2", "row-a-2", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/a", "/a/3", "a-3", "row-a-3", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/b", "/b/1", "b-1", "row-b-1", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/b", "/b/2", "b-2", "row-b-2", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/b", "/b/3", "b-3", "row-b-3", workspace.StateStopped, state.AttnInactive, fixedNow),
	}
	m := model{width: 120}

	// Five pane rows leave four for grouped content after the Sessions title.
	got := wsStripANSI(m.renderWorkspaceRowsViewport(120, 5, rows, 5, fixedNow))
	if !strings.Contains(got, "row-b-3") {
		t.Fatalf("viewport must include the selected bottom row, got %q", got)
	}
	if strings.Contains(got, "row-a-1") {
		t.Fatalf("viewport must scroll earlier rows out of view, got %q", got)
	}
	if !strings.Contains(got, "  b  /b") {
		t.Fatalf("viewport must retain the selected repo header, got %q", got)
	}
}

func TestSessionCursorMovementScrollsOnlyAtViewportEdge(t *testing.T) {
	m := model{
		width:  120,
		height: 9, // sessions inner height 5: title + four grouped list lines
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/1", "one", "row-1", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/2", "two", "row-2", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/3", "three", "row-3", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/4", "four", "row-4", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/5", "five", "row-5", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/6", "six", "row-6", workspace.StateStopped, state.AttnInactive, fixedNow),
		},
	}

	for range 4 {
		updated, _ := m.Update(keyMsg("j"))
		m = updated.(model)
	}
	if m.sessionCursor != 4 || m.sessionScroll != 2 {
		t.Fatalf("after moving below viewport: cursor=%d scroll=%d, want cursor=4 scroll=2",
			m.sessionCursor, m.sessionScroll)
	}

	updated, _ := m.Update(keyMsg("k"))
	m = updated.(model)
	if m.sessionCursor != 3 || m.sessionScroll != 2 {
		t.Fatalf("moving within viewport must preserve scroll: cursor=%d scroll=%d, want cursor=3 scroll=2",
			m.sessionCursor, m.sessionScroll)
	}

	updated, _ = m.Update(keyMsg("<"))
	m = updated.(model)
	if m.sessionCursor != 0 || m.sessionScroll != 0 {
		t.Fatalf("jumping to top must restore first repo header: cursor=%d scroll=%d",
			m.sessionCursor, m.sessionScroll)
	}
}

func TestRenderWorkspaceRowsViewportPinsPromptBelowScrollableRows(t *testing.T) {
	rows := []workspace.Row{
		makeRow("/r", "/r/1", "one", "row-1", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/r", "/r/2", "two", "row-2", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/r", "/r/3", "three", "row-3", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/r", "/r/4", "four", "row-4", workspace.StateStopped, state.AttnInactive, fixedNow),
	}
	input := textinput.New()
	input.SetValue("feature")
	m := model{
		width:           120,
		prompt:          promptNewWorktree,
		newWorktreeRepo: "/r",
		input:           input,
	}

	got := wsStripANSI(m.renderWorkspaceRowsViewport(120, 5, rows, 3, fixedNow))
	if !strings.Contains(got, "new worktree branch:") || !strings.Contains(got, "feature") {
		t.Fatalf("prompt must stay visible below the scrollable list, got %q", got)
	}
	if !strings.Contains(got, "row-4") {
		t.Fatalf("selected row must remain visible when prompt reserves space, got %q", got)
	}
	if strings.Count(got, "\n") > 5 {
		t.Fatalf("viewport rendered beyond its five-row budget: %q", got)
	}
}

func TestViewFitsLongWorkspaceListToTerminalHeight(t *testing.T) {
	m := model{
		width:         120,
		height:        9,
		sessionCursor: 5,
		tickNow:       fixedNow,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/1", "one", "row-1", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/2", "two", "row-2", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/3", "three", "row-3", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/4", "four", "row-4", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/5", "five", "row-5", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/6", "six", "row-6", workspace.StateStopped, state.AttnInactive, fixedNow),
		},
	}

	got := m.View()
	if h := lipgloss.Height(got); h != m.height {
		t.Fatalf("long workspace view height = %d, want terminal height %d", h, m.height)
	}
	plain := wsStripANSI(got)
	if !strings.Contains(plain, "row-6") {
		t.Fatalf("view must show selected row after scrolling, got %q", plain)
	}
	if strings.Contains(plain, "row-1") {
		t.Fatalf("view must clip rows above the viewport, got %q", plain)
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

func threeRowModel(cursor int) model {
	return model{
		width: 120,
		workspaceRows: []workspace.Row{
			makeRow("/r", "/r/a", "main", "row-a", workspace.StateRunning, state.AttnActive, fixedNow),
			makeRow("/r", "/r/b", "feat", "row-b", workspace.StateStopped, state.AttnInactive, fixedNow.Add(-1*time.Minute)),
			makeRow("/r", "/r/c", "fix", "row-c", workspace.StateStopped, state.AttnInactive, fixedNow.Add(-2*time.Minute)),
		},
		sessionCursor: cursor,
	}
}

func TestSessionCursorJumpsToBottomWithG(t *testing.T) {
	for _, key := range []string{"G", ">"} {
		m := threeRowModel(0)
		updated, _ := m.Update(keyMsg(key))
		if got := updated.(model).sessionCursor; got != 2 {
			t.Fatalf("cursor after %q = %d, want 2", key, got)
		}
	}
}

func TestSessionCursorJumpsToTopWithGG(t *testing.T) {
	m := threeRowModel(2)
	updated, _ := m.Update(keyMsg("g"))
	if got := updated.(model).sessionCursor; got != 2 {
		t.Fatalf("first g should not move cursor, got %d", got)
	}
	updated, _ = updated.(model).Update(keyMsg("g"))
	if got := updated.(model).sessionCursor; got != 0 {
		t.Fatalf("cursor after gg = %d, want 0", got)
	}
}

func TestSessionCursorJumpsToTopWithLessThan(t *testing.T) {
	m := threeRowModel(2)
	updated, _ := m.Update(keyMsg("<"))
	if got := updated.(model).sessionCursor; got != 0 {
		t.Fatalf("cursor after < = %d, want 0", got)
	}
}

func TestSessionSingleGDoesNotJump(t *testing.T) {
	// A lone g followed by a non-g key must not trigger jump-to-top.
	m := threeRowModel(1)
	updated, _ := m.Update(keyMsg("g"))
	updated, _ = updated.(model).Update(keyMsg("j"))
	if got := updated.(model).sessionCursor; got != 2 {
		t.Fatalf("g then j: cursor = %d, want 2", got)
	}
}

func multiRepoModel(cursor int) model {
	// Three repo groups, contiguous by repo as Merge produces them:
	// idx 0,1 -> /a ; idx 2 -> /b ; idx 3,4 -> /c
	return model{
		width: 120,
		workspaceRows: []workspace.Row{
			makeRow("/a", "/a", "main", "a-root", workspace.StateRunning, state.AttnActive, fixedNow),
			makeRow("/a", "/a/feat", "feat", "a-feat", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/b", "/b", "main", "b-root", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/c", "/c", "main", "c-root", workspace.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/c", "/c/x", "x", "c-x", workspace.StateStopped, state.AttnInactive, fixedNow),
		},
		sessionCursor: cursor,
	}
}

func TestCtrlDJumpsToNextRepo(t *testing.T) {
	cases := []struct{ from, want int }{
		{0, 2}, // mid/first of /a -> start of /b
		{1, 2}, // second row of /a -> start of /b
		{2, 3}, // /b -> start of /c
		{4, 4}, // last repo -> stays put
	}
	for _, c := range cases {
		m := multiRepoModel(c.from)
		updated, _ := m.Update(keyMsg("ctrl+d"))
		if got := updated.(model).sessionCursor; got != c.want {
			t.Fatalf("ctrl+d from %d = %d, want %d", c.from, got, c.want)
		}
	}
}

func TestCtrlUJumpsToPrevRepo(t *testing.T) {
	cases := []struct{ from, want int }{
		{1, 0}, // mid /a -> current group start
		{0, 0}, // first repo, at start -> stays
		{3, 2}, // start of /c -> start of /b
		{4, 3}, // mid /c -> current group start
	}
	for _, c := range cases {
		m := multiRepoModel(c.from)
		updated, _ := m.Update(keyMsg("ctrl+u"))
		if got := updated.(model).sessionCursor; got != c.want {
			t.Fatalf("ctrl+u from %d = %d, want %d", c.from, got, c.want)
		}
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

// ---------------------------------------------------------------------------
// promptNewWorktree visibility in the sessions pane
// ---------------------------------------------------------------------------

// makeWorktreePromptModel builds a model with promptNewWorktree active and the
// given branch text pre-filled in the input widget. twAvail controls whether
// taskwarrior is reported as installed — the sessions-pane prompt must render
// regardless of that flag.
func makeWorktreePromptModel(branchText string, twAvail bool) model {
	ti := textinput.New()
	ti.SetValue(branchText)
	return model{
		width:   200,
		twAvail: twAvail,
		prompt:  promptNewWorktree,
		input:   ti,
	}
}

// TestPromptNewWorktreeRendersInSessionsPaneTwAvailTrue asserts that the
// branch-name prompt label and the typed value appear in renderWorkspaceRows
// output when taskwarrior IS available.
func TestPromptNewWorktreeRendersInSessionsPaneTwAvailTrue(t *testing.T) {
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "main", "running", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	m := makeWorktreePromptModel("feat/my-branch", true)
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)

	if !strings.Contains(got, "new worktree branch:") {
		t.Fatalf("sessions pane must contain 'new worktree branch:' label (twAvail=true), got %q", got)
	}
	if !strings.Contains(got, "feat/my-branch") {
		t.Fatalf("sessions pane must contain typed branch value (twAvail=true), got %q", got)
	}
}

// TestPromptNewWorktreeRendersInSessionsPaneTwAvailFalse asserts that the
// branch-name prompt renders even when taskwarrior is NOT installed — the
// sessions pane must not depend on m.twAvail.
func TestPromptNewWorktreeRendersInSessionsPaneTwAvailFalse(t *testing.T) {
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "main", "running", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	m := makeWorktreePromptModel("feat/no-tw", false)
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)

	if !strings.Contains(got, "new worktree branch:") {
		t.Fatalf("sessions pane must contain 'new worktree branch:' label (twAvail=false), got %q", got)
	}
	if !strings.Contains(got, "feat/no-tw") {
		t.Fatalf("sessions pane must contain typed branch value (twAvail=false), got %q", got)
	}
}

// TestPromptNewWorktreeRendersInEmptySessionsPane asserts that the prompt
// still renders in the early-return (no worktrees configured) path.
func TestPromptNewWorktreeRendersInEmptySessionsPane(t *testing.T) {
	m := makeWorktreePromptModel("feat/empty-path", false)
	got := m.renderWorkspaceRows(200, nil, 0, fixedNow)

	if !strings.Contains(got, "new worktree branch:") {
		t.Fatalf("empty sessions pane must still show 'new worktree branch:' label, got %q", got)
	}
	if !strings.Contains(got, "feat/empty-path") {
		t.Fatalf("empty sessions pane must still show typed branch value, got %q", got)
	}
}

// TestPromptIdleDoesNotRenderWorktreePromptInSessionsPane confirms that the
// branch-name prompt is absent when no prompt is active (no regression).
func TestPromptIdleDoesNotRenderWorktreePromptInSessionsPane(t *testing.T) {
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "main", "running", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	m := model{width: 200, prompt: promptIdle}
	got := m.renderWorkspaceRows(200, rows, 0, fixedNow)

	if strings.Contains(got, "new worktree branch:") {
		t.Fatalf("sessions pane must NOT show worktree prompt when promptIdle, got %q", got)
	}
}

// TestPromptNewWorktreeShowsBaseBranchWhenRootRowKnown asserts that when the
// model knows the repo's root worktree branch, the prompt label names it
// ("new worktree off main:") instead of the generic fallback.
func TestPromptNewWorktreeShowsBaseBranchWhenRootRowKnown(t *testing.T) {
	ti := textinput.New()
	ti.SetValue("feat/new-thing")
	rootRow := workspace.Row{
		Repo:     "/repo/a",
		Worktree: "/repo/a",
		Branch:   "main",
		IsRoot:   true,
		State:    workspace.StateRunning,
	}
	m := model{
		width:           200,
		prompt:          promptNewWorktree,
		input:           ti,
		newWorktreeRepo: "/repo/a",
		workspaceRows:   []workspace.Row{rootRow},
	}
	got := wsStripANSI(m.renderWorkspaceRows(200, []workspace.Row{rootRow}, 0, fixedNow))

	if !strings.Contains(got, "new worktree off main:") {
		t.Fatalf("prompt must show 'new worktree off main:' when root branch is known, got %q", got)
	}
	if !strings.Contains(got, "feat/new-thing") {
		t.Fatalf("prompt must contain the typed branch value, got %q", got)
	}
	if strings.Contains(got, "new worktree branch:") {
		t.Fatalf("generic fallback label must not appear when base branch is known, got %q", got)
	}
}

// TestPromptFetchBranchRendersFetchLabel asserts that the 'F' branch prompt
// shows a fetch-specific label (not the new-worktree label) plus the typed
// value, so the user can tell the fetch flow apart from 'n'.
func TestPromptFetchBranchRendersFetchLabel(t *testing.T) {
	rows := []workspace.Row{
		makeRow("/repo/a", "/repo/a", "main", "running", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	ti := textinput.New()
	ti.SetValue("feature/remote-only")
	m := model{width: 200, prompt: promptFetchBranch, input: ti}
	got := wsStripANSI(m.renderWorkspaceRows(200, rows, 0, fixedNow))

	if !strings.Contains(got, "fetch branch from origin:") {
		t.Fatalf("sessions pane must contain 'fetch branch from origin:' label, got %q", got)
	}
	if !strings.Contains(got, "feature/remote-only") {
		t.Fatalf("sessions pane must contain typed branch value, got %q", got)
	}
	if strings.Contains(got, "new worktree branch:") {
		t.Fatalf("fetch prompt must not show the new-worktree label, got %q", got)
	}
}

// TestFormatCreatingRowShowsSpinnerAndFetchingVerb asserts the pending-create
// placeholder renders the branch, the active spinner glyph, and the fetch verb.
func TestFormatCreatingRowShowsSpinnerAndFetchingVerb(t *testing.T) {
	m := model{
		width:        200,
		spinnerFrame: 0,
		pendingCreates: map[string]pendingCreate{
			createKey("/repo/a", "feature/login"): {
				repo: "/repo/a", dest: "/repo/a-feature/login", branch: "feature/login", fromRemote: true,
			},
		},
	}
	rows := []workspace.Row{
		{Repo: "/repo/a", Worktree: "/repo/a-feature/login", Branch: "feature/login", State: workspace.StateCreating},
	}
	got := wsStripANSI(m.renderWorkspaceRows(200, rows, 0, fixedNow))

	if !strings.Contains(got, "feature/login") {
		t.Fatalf("creating row must show the branch, got %q", got)
	}
	if !strings.Contains(got, "(fetching…)") {
		t.Fatalf("fetch flow must show '(fetching…)', got %q", got)
	}
	if !strings.Contains(got, spinnerFrames[0]) {
		t.Fatalf("creating row must show the spinner glyph %q, got %q", spinnerFrames[0], got)
	}
}

// TestFormatCreatingRowShowsCreatingVerbForLocalFlow asserts the 'n' (local)
// flow labels the placeholder "(creating…)" rather than "(fetching…)".
func TestFormatCreatingRowShowsCreatingVerbForLocalFlow(t *testing.T) {
	m := model{
		width: 200,
		pendingCreates: map[string]pendingCreate{
			createKey("/repo/a", "feat"): {repo: "/repo/a", dest: "/repo/a-feat", branch: "feat", fromRemote: false},
		},
	}
	rows := []workspace.Row{
		{Repo: "/repo/a", Worktree: "/repo/a-feat", Branch: "feat", State: workspace.StateCreating},
	}
	got := wsStripANSI(m.renderWorkspaceRows(200, rows, 0, fixedNow))

	if !strings.Contains(got, "(creating…)") {
		t.Fatalf("local flow must show '(creating…)', got %q", got)
	}
	if strings.Contains(got, "(fetching…)") {
		t.Fatalf("local flow must not show '(fetching…)', got %q", got)
	}
}

// TestTaskPromptLineOmitsWorktreePrompts asserts that taskPromptLine does NOT
// mirror the worktree branch-name prompt: it is a sessions-pane action and must
// render in one place only (renderWorkspaceRows), not duplicated in the tasks
// pane.
func TestTaskPromptLineOmitsWorktreePrompts(t *testing.T) {
	ti := textinput.New()
	ti.SetValue("feat/task-pane")
	for _, p := range []promptMode{promptNewWorktree, promptFetchBranch, promptConfirmDeleteWorktree, promptConfirmDeleteWorktree2} {
		m := model{prompt: p, input: ti}
		if isTaskPrompt(p) {
			t.Fatalf("isTaskPrompt(%v) must be false", p)
		}
		if got := m.taskPromptLine(); got != "" {
			t.Fatalf("taskPromptLine must be empty for sessions-pane prompt %v, got %q", p, got)
		}
	}
}
