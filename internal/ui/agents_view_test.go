package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
)

func pressKey(t *testing.T, m model, key string) (model, tea.Cmd) {
	t.Helper()

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	switch key {
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	}

	updated, cmd := m.Update(msg)

	return updated.(model), cmd
}

func agentsTabModel(t *testing.T, tmux *fakeTmuxOps, marker *recordingViewMarker) model {
	t.Helper()

	rows := []settings.Row{worktreeRow(t.TempDir(), settings.StateRunning, state.AttnFinished)}
	m := feedTestModel(tmux, nil)
	m.viewMarker = marker
	m.width, m.height = 120, 40
	m.agentsList = newAgentList()

	return rebuildAgents(t, m, rows)
}

func TestTabCyclesWorkspacesReposAgents(t *testing.T) {
	m := feedTestModel(&fakeTmuxOps{available: true}, nil)
	m.view = viewWorkspaces

	var visited []viewMode
	for range viewTabs {
		m, _ = pressKey(t, m, "tab")
		visited = append(visited, m.view)
	}

	want := []viewMode{viewSessions, viewAgents, viewWorkspaces}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("tab sequence = %v, want %v", visited, want)
		}
	}
}

func TestHeaderShowsAllThreeTabs(t *testing.T) {
	header := renderHeader(120, viewAgents, headerSummary{})

	for _, label := range []string{"Workspaces", "Repos", "Agents"} {
		if !strings.Contains(header, label) {
			t.Fatalf("header missing %q: %s", label, header)
		}
	}
}

func TestAgentsViewListsDoneAgentUnderDoneGroup(t *testing.T) {
	m := agentsTabModel(t, &fakeTmuxOps{available: true}, &recordingViewMarker{})
	m.view = viewAgents

	out := m.View()

	if !strings.Contains(out, "Done · 1") {
		t.Fatalf("agents view missing Done group:\n%s", out)
	}
}

func TestAgentsViewEnterJumpsToAgentAndMarksItViewed(t *testing.T) {
	tmux := &fakeTmuxOps{available: true, findWindowResult: "s:1", processAlive: true}
	marker := &recordingViewMarker{}
	m := agentsTabModel(t, tmux, marker)
	m.view = viewAgents

	m, cmd := pressKey(t, m, "enter")
	if cmd == nil {
		t.Fatal("enter produced no jump command")
	}
	updated, _ := m.Update(cmd())
	m = updated.(model)

	if len(marker.viewed) != 1 || marker.viewed[0] != "ses_1" {
		t.Fatalf("viewed = %v, want [ses_1]", marker.viewed)
	}
}

func TestAgentsViewJumpLeavesAgentDoneWhileFocusPollTracksVisits(t *testing.T) {
	tmux := &fakeTmuxOps{available: true, findWindowResult: "s:1", processAlive: true}
	marker := &recordingViewMarker{}
	m := agentsTabModel(t, tmux, marker)
	m.attachBadges(newFakeTmuxServer(t.TempDir()))
	m.view = viewAgents

	m, cmd := pressKey(t, m, "enter")
	if cmd == nil {
		t.Fatal("enter produced no jump command")
	}
	m.Update(cmd())

	if len(marker.viewed) != 0 {
		t.Fatalf("viewed = %v, want none until the user leaves the agent", marker.viewed)
	}
}

func TestAgentsViewMarkSeenKeyMarksViewedWithoutJumping(t *testing.T) {
	marker := &recordingViewMarker{}
	m := agentsTabModel(t, &fakeTmuxOps{available: true}, marker)
	m.view = viewAgents

	_, cmd := pressKey(t, m, "x")

	if cmd != nil {
		t.Fatal("x should not dispatch a command")
	}
	if len(marker.viewed) != 1 {
		t.Fatalf("viewed = %v, want one mark", marker.viewed)
	}
}

func TestAgentsViewFilterKeepsQAndEscFromQuitting(t *testing.T) {
	m := agentsTabModel(t, &fakeTmuxOps{available: true}, &recordingViewMarker{})
	m.view = viewAgents

	m, _ = pressKey(t, m, "/")
	m, cmd := pressKey(t, m, "q")
	if cmd != nil && isQuit(cmd) {
		t.Fatal("q quit while filtering")
	}
	m, cmd = pressKey(t, m, "esc")
	if cmd != nil && isQuit(cmd) {
		t.Fatal("esc quit while filtering")
	}
	if m.agentsList.Filtering() {
		t.Fatal("esc should clear the filter")
	}
}

func TestAgentsViewQuitsOutsideFilter(t *testing.T) {
	m := agentsTabModel(t, &fakeTmuxOps{available: true}, &recordingViewMarker{})
	m.view = viewAgents

	_, cmd := pressKey(t, m, "q")

	if cmd == nil || !isQuit(cmd) {
		t.Fatal("q should quit from the Agents tab")
	}
}

func isQuit(cmd tea.Cmd) bool {
	_, ok := cmd().(tea.QuitMsg)

	return ok
}
