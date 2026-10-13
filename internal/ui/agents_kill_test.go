package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/mux"
)

// windowedAgentModel is the Agents tab with its single agent running in tmux
// window @4.
func windowedAgentModel(t *testing.T, tmux *fakeMux) model {
	t.Helper()

	m := agentsTabModel(t, tmux, &recordingViewMarker{})
	m.view = viewAgents
	dir := m.agents[0].ID
	m.panes = []mux.Pane{{PaneID: "%3", WindowID: "@4", SessionID: "$1", Cwd: dir, CogDir: dir, Command: "omp"}}
	m.refreshAgents()

	return m
}

func TestAgentsKillConfirmedClosesWindowAndDismissesAgent(t *testing.T) {
	tmux := &fakeMux{available: true}
	m := windowedAgentModel(t, tmux)

	m, cmd := pressKey(t, m, "K")
	if cmd != nil {
		t.Fatal("K must only open the confirmation")
	}
	if !strings.Contains(m.View(), "kill agent [") {
		t.Fatalf("confirmation not shown:\n%s", m.View())
	}

	m, cmd = pressKey(t, m, "y")
	if cmd == nil {
		t.Fatal("y must dispatch the kill")
	}
	updated, _ := m.Update(cmd())
	m = updated.(model)

	if len(tmux.killWindowCalls) != 1 || tmux.killWindowCalls[0] != mux.Target("@4") {
		t.Fatalf("killWindowCalls = %v, want [@4]", tmux.killWindowCalls)
	}
	if m.prompt != promptIdle {
		t.Fatalf("prompt = %v, want idle after confirming", m.prompt)
	}

	m.panes = nil
	m.refreshAgents()
	if len(m.agents) != 0 {
		t.Fatalf("killed agent still listed once its window is gone: %v", m.agents)
	}
}

func TestAgentsKillCancelledByAnyOtherKey(t *testing.T) {
	tmux := &fakeMux{available: true}
	m := windowedAgentModel(t, tmux)

	m, _ = pressKey(t, m, "K")
	m, cmd := pressKey(t, m, "esc")

	if cmd != nil || len(tmux.killWindowCalls) != 0 {
		t.Fatalf("cancel must not kill; cmd=%v calls=%v", cmd != nil, tmux.killWindowCalls)
	}
	if m.prompt != promptIdle {
		t.Fatalf("prompt = %v, want idle after cancelling", m.prompt)
	}
}

func TestAgentsKillRefusesCogitatorsOwnWindow(t *testing.T) {
	m := windowedAgentModel(t, &fakeMux{available: true})
	m.panes = append(m.panes, mux.Pane{PaneID: "%9", WindowID: "@4", SessionID: "$1", Command: "cogitator"})
	m.selfPaneID = "%9"
	m.refreshAgents()

	m, _ = pressKey(t, m, "K")

	if m.prompt == promptConfirmKillAgent {
		t.Fatal("must not offer to kill the window cogitator runs in")
	}
	if !strings.Contains(m.tmuxHint, "cogitator runs in this window") {
		t.Fatalf("tmuxHint = %q, want an explanation", m.tmuxHint)
	}
}

func TestAgentsKillDismissesAgentWithoutWindow(t *testing.T) {
	marker := &recordingViewMarker{}
	m := agentsTabModel(t, &fakeMux{available: true}, marker)
	m.view = viewAgents
	sessionID := m.agents[0].SessionID

	m, _ = pressKey(t, m, "K")

	if m.prompt == promptConfirmKillAgent {
		t.Fatal("an agent without a window has nothing to kill")
	}
	if len(m.agents) != 0 {
		t.Fatalf("dismissed agent still listed: %v", m.agents)
	}
	if len(marker.viewed) != 1 || marker.viewed[0] != sessionID {
		t.Fatalf("viewed = %v, want the dismissed session marked seen", marker.viewed)
	}
}

func TestAgentsKillFailureShowsHint(t *testing.T) {
	tmux := &fakeMux{available: true, killWindowErr: errors.New("can't find window: @4")}
	m := windowedAgentModel(t, tmux)

	m, _ = pressKey(t, m, "K")
	m, cmd := pressKey(t, m, "y")
	updated, _ := m.Update(cmd())

	if hint := updated.(model).tmuxHint; !strings.Contains(hint, "can't find window") {
		t.Fatalf("tmuxHint = %q, want the kill error", hint)
	}
}
