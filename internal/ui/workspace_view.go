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

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/guilhermehto/cogitator/internal/settings"
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

// wsLineKind distinguishes the three kinds of line the Workspaces view
// renders.
type wsLineKind int

const (
	wsLineHeader wsLineKind = iota
	wsLineSession
	wsLineHint
)

// wsDisplayLine is one line in the Workspaces view's scrollable list. entry
// is the index into the flat set of cursor targets — workspace headers and
// session rows both are targets, since 'n'/'e'/'D' (later steps) act on the
// workspace under the cursor even when it has no sessions yet. wsLineHint
// lines are not cursor targets (entry is -1).
type wsDisplayLine struct {
	kind      wsLineKind
	wsIndex   int
	sessIndex int
	entry     int
}

// wsDisplayLines expands the workspace/session status list into the visual
// order the Workspaces view renders: one header line per workspace, then
// either one line per session or — when it has none — a hint line pointing
// at 'n'.
func wsDisplayLines(statuses []workspace.WorkspaceStatus) []wsDisplayLine {
	var lines []wsDisplayLine
	entry := 0
	for wi, ws := range statuses {
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
		b.WriteString(dimStyle.Render("(no workspaces configured — press N to create one)"))
		return b.String()
	}

	lines := wsDisplayLines(m.wsStatuses)
	listHeight := -1
	if height > 0 {
		listHeight = max(0, height-1)
	}
	start, end := wsWindow(lines, m.wsCursor, m.wsScroll, listHeight)

	for _, dl := range lines[start:end] {
		ws := m.wsStatuses[dl.wsIndex]
		if dl.kind == wsLineHint {
			b.WriteString("    " + wtHintStyle.Render("no sessions yet — press n to create one") + "\n")
			continue
		}

		var line string
		if dl.kind == wsLineHeader {
			line = wtRepoStyle.Render("  "+ws.Workspace.Name) + "  " +
				wtPathStyle.Render(fmt.Sprintf("%d sessions", len(ws.Sessions)))
		} else {
			line = formatWsSessionRow(ws.Sessions[dl.sessIndex], width-2)
		}
		if dl.entry == m.wsCursor {
			line = highlightSelectedRow(line)
		}
		b.WriteString(line + "\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

// formatWsSessionRow renders one session line: the live/roster status glyph
// (via worktreeStatusCell, shared with the Sessions view's status
// vocabulary), the session's branch, and its member repo basenames.
func formatWsSessionRow(sess workspace.SessionStatus, width int) string {
	statusCell := worktreeStatusCell(settings.Row{State: sess.State, Attention: sess.Attention})
	sessionW := worktreeSessionWidth(width)

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
	if len(members) > 0 {
		titleStr += "  " + wtPathStyle.Render(strings.Join(members, ", "))
	}

	cells := []string{
		padCell(statusCell, colStateW, lipgloss.Left),
		padCell(titleStr, sessionW, lipgloss.Left),
	}
	return strings.Join(cells, strings.Repeat(" ", colGap))
}
