package ui

// workspace_view.go — the Workspaces view (Tab): its own key handling
// (updateWorkspaceView, following updateSettings's precedent so Update stays
// out of the arm-adding business) and rendering. Cursor/scroll state
// (wsCursor/wsScroll/wsPendingG) lives on model but is only ever touched from
// here, kept separate from sessionCursor/sessionScroll/pendingG so switching
// views with Tab never disturbs the other view's position.
//
// Naming note: this file's wsDisplayLine/wsWindow/wsEntry* helpers are
// deliberately distinct from render.go's workspaceDisplayLine/workspaceWindow,
// which — despite the name — belong to the Sessions view's grouped worktree
// list (a holdover from before internal/workspace existed). Do not conflate
// the two.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// updateWorkspaceView handles key input while the Workspaces view is active:
// j/k/up/down move the cursor over workspace headers and session rows, gg
// jumps to the top, G/> jumps to the bottom, and < mirrors gg. Lifecycle keys
// (N/n/e/D/enter) belong to later steps; this one is navigation only.
func (m model) updateWorkspaceView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	total := wsEntryCount(m.wsStatuses)
	wasPendingG := m.wsPendingG
	m.wsPendingG = false

	switch msg.String() {
	case "j", "down":
		if total > 0 {
			m.wsCursor = min(m.wsCursor+1, total-1)
			m.syncWsScroll()
		}
	case "k", "up":
		if total > 0 {
			m.wsCursor = max(m.wsCursor-1, 0)
			m.syncWsScroll()
		}
	case "g":
		if wasPendingG {
			m.wsCursor = 0
			m.syncWsScroll()
		} else {
			m.wsPendingG = true
		}
	case "<":
		m.wsCursor = 0
		m.syncWsScroll()
	case "G", ">":
		if total > 0 {
			m.wsCursor = total - 1
			m.syncWsScroll()
		}
	}
	return m, nil
}

// syncWsScroll moves the Workspaces view's scroll offset just enough to keep
// the selected entry visible, mirroring syncSessionScroll for the Sessions
// view.
func (m *model) syncWsScroll() {
	lines := wsDisplayLines(m.wsStatuses)
	listHeight := m.wsListHeight()
	start, _ := wsWindow(lines, m.wsCursor, m.wsScroll, listHeight)
	m.wsScroll = start
}

// wsListHeight is the number of scrollable lines available inside the
// Workspaces pane after its title line, mirroring sessionsListHeight.
func (m model) wsListHeight() int {
	_, innerH := m.paneHeights()
	return max(0, innerH-1)
}

// wsLineKind distinguishes the kinds of line the Workspaces view renders.
type wsLineKind int

const (
	wsLineHeader wsLineKind = iota
	wsLineSession
	wsLineHint
	wsLineSpacer
)

// wsDisplayLine is one line in the Workspaces view's scrollable list. entry
// is the index into the flat set of cursor targets — workspace headers and
// session rows both are targets, since 'n'/'e'/'D' (later steps) act on the
// workspace under the cursor even when it has no sessions yet. wsLineHint
// and wsLineSpacer lines are not cursor targets (entry is -1).
type wsDisplayLine struct {
	kind      wsLineKind
	wsIndex   int
	sessIndex int
	entry     int
}

// wsDisplayLines expands the workspace/session status list into the visual
// order the Workspaces view renders: a blank spacer between workspaces, one
// header line per workspace, then either one line per session or — when it
// has none — a hint line pointing at the next step.
func wsDisplayLines(statuses []workspace.WorkspaceStatus) []wsDisplayLine {
	var lines []wsDisplayLine
	entry := 0
	for wi, ws := range statuses {
		if wi > 0 {
			lines = append(lines, wsDisplayLine{kind: wsLineSpacer, wsIndex: wi, entry: -1})
		}
		lines = append(lines, wsDisplayLine{kind: wsLineHeader, wsIndex: wi, entry: entry})
		entry++
		if len(ws.Sessions) == 0 {
			lines = append(lines, wsDisplayLine{kind: wsLineHint, wsIndex: wi, entry: -1})
			continue
		}
		for si := range ws.Sessions {
			lines = append(lines, wsDisplayLine{kind: wsLineSession, wsIndex: wi, sessIndex: si, entry: entry})
			entry++
		}
	}
	return lines
}

// wsEntryCount returns the number of cursor targets (workspace headers plus
// session rows) across statuses — the range m.wsCursor must stay within.
func wsEntryCount(statuses []workspace.WorkspaceStatus) int {
	n := 0
	for _, ws := range statuses {
		n += 1 + len(ws.Sessions)
	}
	return n
}

// wsWindow returns the visible half-open range in lines for the given cursor
// entry, mirroring workspaceWindow's scroll-preservation behaviour: the
// scroll offset stays put while the cursor remains inside the viewport and
// only moves when the selection crosses an edge. A negative height means
// unbounded rendering; zero renders no list lines.
func wsWindow(lines []wsDisplayLine, cursorEntry, scroll, height int) (start, end int) {
	if height < 0 || len(lines) <= height {
		return 0, len(lines)
	}
	if height == 0 {
		return 0, 0
	}

	cursorLine := 0
	for i, line := range lines {
		if line.entry == cursorEntry {
			cursorLine = i
			break
		}
	}

	maxStart := len(lines) - height
	start = min(max(scroll, 0), maxStart)
	switch {
	case cursorLine < start:
		start = cursorLine
	case cursorLine >= start+height:
		start = cursorLine - height + 1
	}
	start = min(max(start, 0), maxStart)
	return start, min(start+height, len(lines))
}

// renderWorkspacesView renders the Workspaces view within height rows: a
// title line, then the scrollable list of workspace headers, session rows,
// and empty-workspace hints built by wsDisplayLines. Reuses the Sessions
// view's status glyphs/styles and cursor-highlight band (render.go) so both
// views read as one system.
func (m model) renderWorkspacesView(width, height int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Workspaces") + "\n")

	if len(m.wsStatuses) == 0 {
		onboarding := strings.Split(workspacesOnboarding(), "\n")
		if height > 0 {
			onboarding = onboarding[:min(len(onboarding), max(0, height-1))]
		}
		b.WriteString(strings.Join(onboarding, "\n"))
		return b.String()
	}

	lines := wsDisplayLines(m.wsStatuses)
	listHeight := -1
	if height > 0 {
		listHeight = max(0, height-1)
	}
	start, end := wsWindow(lines, m.wsCursor, m.wsScroll, listHeight)
	spinnerGlyph := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	now := m.tickNow
	if now.IsZero() {
		now = time.Now()
	}

	for _, dl := range lines[start:end] {
		ws := m.wsStatuses[dl.wsIndex]
		switch dl.kind {
		case wsLineSpacer:
			b.WriteString("\n")
			continue
		case wsLineHint:
			b.WriteString(wsTreeGuide(true) + wsEmptyHint(ws) + "\n")
			continue
		}

		var line string
		if dl.kind == wsLineHeader {
			line = m.formatWsHeader(ws, spinnerGlyph, width-2)
		} else {
			sess := ws.Sessions[dl.sessIndex]
			switch {
			case m.wsDeletePending(ws.Workspace.Name, sess.Session.Name):
				sess = busyWsSession(sess, spinnerGlyph, "deleting")
			case m.wsPullPending(wsTarget{workspace: ws.Workspace.Name, session: sess.Session.Name}):
				sess = busyWsSession(sess, spinnerGlyph, "pulling")
			}
			last := dl.sessIndex == len(ws.Sessions)-1
			line = wsTreeGuide(last) + formatWsSessionRow(now, sess, m.wsSafe[sess.Session.Dir], width-2-wsTreeGuideW)
		}
		if dl.entry == m.wsCursor {
			line = highlightSelectedRow(line)
		}
		b.WriteString(line + "\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

// wsTreeGuideW is the cell width of the tree connector drawn before every
// line nested under a workspace header.
const wsTreeGuideW = 5

// wsTreeGuide draws the connector tying a nested line to its workspace
// header; last closes the branch.
func wsTreeGuide(last bool) string {
	if last {
		return dimStyle.Render("  └─ ")
	}
	return dimStyle.Render("  ├─ ")
}

// wsEmptyHint is the placeholder under a workspace with no sessions. A
// workspace without member repos cannot start a session yet, so it points at
// 'e' first.
func wsEmptyHint(ws workspace.WorkspaceStatus) string {
	if len(ws.Workspace.Members) == 0 {
		return wtHintStyle.Render("no repos yet — press e to add some")
	}
	return wtHintStyle.Render("no sessions yet — press n to start one")
}

// workspacesOnboarding is the empty state shown before any workspace exists:
// what a workspace is and the three keys that get one running.
func workspacesOnboarding() string {
	steps := [][2]string{
		{"N", "create a workspace"},
		{"e", "add the repos it spans"},
		{"n", "start a session — one worktree per repo"},
	}
	var b strings.Builder
	b.WriteString("\n  " + wtRepoStyle.Render("No workspaces yet") + "\n\n")
	b.WriteString("  " + dimStyle.Render("A workspace groups repos that change together. Each session gets a") + "\n")
	b.WriteString("  " + dimStyle.Render("worktree of every member repo, side by side in one directory.") + "\n\n")
	for i, s := range steps {
		b.WriteString(fmt.Sprintf("  %s  %s  %s\n", dimStyle.Render(fmt.Sprintf("%d.", i+1)), hintKeyStyle.Render(s[0]), s[1]))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// formatWsHeader renders a workspace header line: its name, the rolled-up
// attention badge of its running sessions (so a collapsed glance says whether
// anything inside needs you), its member repos, and — right-aligned — either
// the session count or the in-flight operation acting on the whole workspace.
func (m model) formatWsHeader(ws workspace.WorkspaceStatus, spinnerGlyph string, width int) string {
	detail := dimStyle.Render(pluralize(len(ws.Sessions), "session"))
	switch {
	case m.wsDeletePending(ws.Workspace.Name, ""):
		detail = wtPathStyle.Render(spinnerGlyph + " deleting…")
	case m.wsPullPending(wsTarget{workspace: ws.Workspace.Name}):
		detail = wtPathStyle.Render(spinnerGlyph + " pulling…")
	}
	line := wtRepoStyle.Render("  "+ws.Workspace.Name) + "  "
	if attn, ok := ws.Attention(); ok {
		line += attnLabel(attn, state.SourceLive)
	}

	members := make([]string, 0, len(ws.Workspace.Members))
	for _, mem := range ws.Workspace.Members {
		members = append(members, filepath.Base(mem.Path))
	}
	if len(members) > 0 {
		memberW := max(0, width-lipgloss.Width(line)-lipgloss.Width(detail)-colGap)
		line += ansi.Truncate(wtPathStyle.Render(strings.Join(members, " · ")), memberW, "…")
	}

	gap := max(colGap, width-lipgloss.Width(line)-lipgloss.Width(detail))
	return line + strings.Repeat(" ", gap) + detail
}

// pluralize formats n with noun, adding an "s" unless n is exactly one.
func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// busyWsSession returns sess relabelled with an animated "<verb>…" marker
// (deleting, pulling) for formatWsSessionRow.
func busyWsSession(sess workspace.SessionStatus, glyph, verb string) workspace.SessionStatus {
	label := sess.Session.Branch
	if label == "" {
		label = sess.Session.Name
	}
	sess.Session.Branch = fmt.Sprintf("%s %s %s…", glyph, verb, label)
	return sess
}

// formatWsSessionRow renders one session line (drawn after its tree guide):
// the live/roster status glyph, then the branch (with a "(safe to delete)"
// tag when safe), its member repo basenames, and the muted session title;
// relative last activity fills the right column for stopped sessions.
func formatWsSessionRow(now time.Time, sess workspace.SessionStatus, safe bool, width int) string {
	statusCell := worktreeStatusCell(settings.Row{State: sess.State, Attention: sess.Attention})
	sessionW := max(1, width-lipgloss.Width(statusCell)-colGap-colActivityW)

	branch := sess.Session.Branch
	if branch == "" {
		branch = sess.Session.Name
	}

	members := make([]string, 0, len(sess.Session.Members))
	for _, mem := range sess.Session.Members {
		members = append(members, filepath.Base(mem.RepoPath))
	}

	titleStr := branch
	if sess.State != settings.StateRunning {
		titleStr = wtStoppedStyle.Render(branch)
	}
	if safe {
		titleStr += " " + wsSafeStyle.Render("(safe to delete)")
	}
	if len(members) > 0 {
		titleStr += "  " + wtPathStyle.Render("["+strings.Join(members, ", ")+"]")
	}
	titleStr += sessionTitleSuffix(sess.Title)

	var activityStr string
	if !sess.LastActivity.IsZero() && sess.State == settings.StateStopped {
		activityStr = dimStyle.Render(formatRelative(now, sess.LastActivity))
	}

	return statusCell +
		padCell(ansi.Truncate(titleStr, sessionW, "…"), sessionW, lipgloss.Left) +
		strings.Repeat(" ", colGap) +
		padCell(activityStr, colActivityW, lipgloss.Right)
}
