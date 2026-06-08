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

// TestSelectedWorkspaceRowHighlightSpansWholeRow asserts the cursor row is one
// clean reverse-video span (like the tasks pane). Embedded foreground colours
// would emit an SGR reset that also clears the reverse attribute, breaking the
// highlight partway through; the selected row must therefore carry exactly the
// reverse-open and a single trailing reset, with no interior escapes.
func TestSelectedWorkspaceRowHighlightSpansWholeRow(t *testing.T) {
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
	if !strings.Contains(line, "\x1b[7m") {
		t.Fatalf("selected row must use reverse video, got %q", line)
	}
	if n := strings.Count(line, "\x1b["); n != 2 {
		t.Fatalf("selected row must be a single clean reverse span (2 escapes), got %d in %q", n, line)
	}
}

// TestUnselectedWorkspaceRowKeepsColour guards the test above: an unselected
// running row still carries its colour styling (so the highlight test is
// actually exercising the strip-before-reverse path, not a no-op).
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

// TestTaskPromptLineOmitsWorktreePrompts asserts that taskPromptLine does NOT
// mirror the worktree branch-name prompt: it is a sessions-pane action and must
// render in one place only (renderWorkspaceRows), not duplicated in the tasks
// pane.
func TestTaskPromptLineOmitsWorktreePrompts(t *testing.T) {
	ti := textinput.New()
	ti.SetValue("feat/task-pane")
	for _, p := range []promptMode{promptNewWorktree, promptConfirmDeleteWorktree, promptConfirmDeleteWorktree2} {
		m := model{prompt: p, input: ti}
		if isTaskPrompt(p) {
			t.Fatalf("isTaskPrompt(%v) must be false", p)
		}
		if got := m.taskPromptLine(); got != "" {
			t.Fatalf("taskPromptLine must be empty for sessions-pane prompt %v, got %q", p, got)
		}
	}
}
