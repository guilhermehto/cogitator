package ui

// chrome.go — the application frame around the content pane: the header
// (brand, view tabs, live summary) and the context-aware key hint bar.

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/settings"
)

var (
	tabActiveStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Background(lipgloss.Color("63")).Padding(0, 1)
	tabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	hintKeyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
)

// viewTabs lists the Tab-cycled views in display order with their labels.
var viewTabs = []struct {
	view  viewMode
	label string
}{
	{viewWorkspaces, "Workspaces"},
	{viewSessions, "Repos"},
}

// headerSummary is the live-session tally shown on the right of the header.
type headerSummary struct {
	live, recent int
	recentWindow time.Duration
	updatedAt    time.Time
}

// renderHeader draws the top line: brand and view tabs on the left (so Tab's
// effect is visible), the live/recent tally right-aligned. The tally is
// dropped when the terminal is too narrow to hold both.
func renderHeader(width int, active viewMode, sum headerSummary) string {
	tabs := make([]string, 0, len(viewTabs))
	for _, t := range viewTabs {
		style := tabInactiveStyle
		if t.view == active {
			style = tabActiveStyle
		}
		tabs = append(tabs, style.Render(t.label))
	}
	left := titleStyle.Render("cogitator") + " " + strings.Join(tabs, " ")

	liveGlyph := attnInactiveStyle.Render(glyphInactive)
	if sum.live > 0 {
		liveGlyph = attnActiveStyle.Render(glyphActive)
	}
	right := liveGlyph + dimStyle.Render(fmt.Sprintf(" %d live · %d recent (≤%dm) · %s ",
		sum.live, sum.recent, int(sum.recentWindow.Minutes()), sum.updatedAt.Format("15:04:05")))

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return ansi.Truncate(left, width, "")
	}
	return left + strings.Repeat(" ", gap) + right
}

// keyHint is one "key action" pair in the hint bar.
type keyHint struct {
	key, desc string
}

// helpHint is pinned to the right end of the hint bar so the way to the full
// reference survives narrow terminals.
var helpHint = keyHint{"?", "help"}

// renderKeyHints lays hints out left to right, dropping trailing hints that
// do not fit rather than cutting one mid-word, with pinned (if set)
// right-aligned.
func renderKeyHints(hints []keyHint, pinned keyHint, width int) string {
	const sep = "   "
	var right string
	if pinned.key != "" {
		right = hintKeyStyle.Render(pinned.key) + " " + dimStyle.Render(pinned.desc)
	}
	budget := width - 1 - lipgloss.Width(right) - len(sep)

	var b strings.Builder
	used := 0
	for i, h := range hints {
		cell := hintKeyStyle.Render(h.key) + " " + dimStyle.Render(h.desc)
		cellW := lipgloss.Width(cell)
		if i > 0 {
			cellW += len(sep)
		}
		if used+cellW > budget {
			break
		}
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(cell)
		used += cellW
	}
	gap := max(len(sep), width-1-used-lipgloss.Width(right))
	if right == "" {
		return " " + b.String()
	}
	return " " + b.String() + strings.Repeat(" ", gap) + right
}

// keyHints returns the bindings most relevant to what is under the cursor in
// the active view, followed by the always-available globals.
func (m model) keyHints() []keyHint {
	var hints []keyHint
	if m.view == viewWorkspaces {
		hints = m.workspaceKeyHints()
	} else {
		hints = m.repoKeyHints()
	}
	next := "repos"
	if m.view != viewWorkspaces {
		next = "workspaces"
	}
	return append(hints,
		keyHint{"tab", next},
		keyHint{"ctrl+p", "switch"},
		keyHint{"q", "quit"},
	)
}

func (m model) workspaceKeyHints() []keyHint {
	if _, sess, ok := m.wsSessionUnderCursor(); ok {
		launch := "resume"
		if sess.State == settings.StateRunning {
			launch = "jump"
		}
		return []keyHint{{"enter", launch}, {"P", "pull"}, {"D", "delete session"}, {"n", "new session"}}
	}
	ws, ok := m.wsUnderCursor()
	if !ok {
		return []keyHint{{"N", "new workspace"}}
	}
	if len(ws.Members) == 0 {
		return []keyHint{{"e", "add repos"}, {"N", "new workspace"}, {"D", "delete workspace"}}
	}
	return []keyHint{{"n", "new session"}, {"e", "repos"}, {"N", "new workspace"}, {"D", "delete"}}
}

func (m model) repoKeyHints() []keyHint {
	if len(m.workspaceRows) == 0 {
		return []keyHint{{"A", "add repo"}}
	}
	return []keyHint{{"enter", "open"}, {"n", "new worktree"}, {"F", "fetch"}, {"P", "pull"}, {"D", "delete"}, {"A", "add repo"}, {"R", "untrack"}}
}
