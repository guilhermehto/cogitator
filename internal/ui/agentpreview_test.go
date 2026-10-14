package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

func previewAgent(id string) agents.Agent {
	a := worktreeAgent(id, "/r/api", "main", agents.GroupDone, state.AttnFinished, settings.StateRunning)
	a.WindowID = "@" + id

	return a
}

func TestPreviewRefreshRecapturesRestingCursor(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-test,1,0")
	recorder := &captureRecorder{}
	p := agentPreview{backend: tmuxctl.Backend{Runner: recorder}}
	a := previewAgent("/a")

	p.Follow(a, true)
	capture := p.Debounced(previewDebounceMsg{seq: p.seq}, a, true)
	refresh := p.Captured(capture().(previewCaptureMsg), a, true)
	if refresh == nil {
		t.Fatal("a finished capture should schedule a refresh")
	}

	capture = p.Refresh(previewRefreshMsg{seq: p.seq}, a, true)
	if capture == nil {
		t.Fatal("refresh should capture the resting cursor again")
	}
	capture()

	if len(recorder.targets) != 2 {
		t.Fatalf("capture targets = %v, want two captures of @/a", recorder.targets)
	}
}

func TestPreviewRefreshStopsAfterHideOrCursorMove(t *testing.T) {
	a, b := previewAgent("/a"), previewAgent("/b")

	tests := []struct {
		name  string
		after func(p *agentPreview)
	}{
		{"hidden", func(p *agentPreview) { p.Hide() }},
		{"cursor moved", func(p *agentPreview) { p.Follow(b, true) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := agentPreview{}
			p.Follow(a, true)
			staleSeq := p.seq

			tt.after(&p)

			if cmd := p.Refresh(previewRefreshMsg{seq: staleSeq}, a, true); cmd != nil {
				t.Fatal("a stale refresh must not capture")
			}
		})
	}
}

func TestPreviewExplainsAgentWithoutTmuxWindow(t *testing.T) {
	a := worktreeAgent("/a", "/r/api", "main", agents.GroupDone, state.AttnFinished, settings.StateRunning)

	view := agentPreview{}.View(a, agentListNow, 60, 10)

	if !strings.Contains(view, "enter resumes it") {
		t.Fatalf("preview should say enter resumes a windowless agent:\n%s", view)
	}
}

func agentsPreviewModel(t *testing.T, width int) (model, *captureRecorder) {
	t.Helper()
	t.Setenv("TMUX", "/tmp/tmux-test,1,0")

	recorder := &captureRecorder{}
	m := feedTestModel(&fakeMux{available: true}, nil)
	m.attachBadges(tmuxctl.Backend{Runner: recorder})
	m.width, m.height = width, 30
	m.agents = []agents.Agent{previewAgent("/a"), previewAgent("/b")}
	m.view = viewSessions

	return m, recorder
}

// settlePreview runs the debounce and capture the preview schedules.
func settlePreview(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a preview debounce")
	}

	updated, capture := m.Update(cmd())
	m = updated.(model)
	if capture == nil {
		t.Fatal("expected a preview capture")
	}
	updated, _ = m.Update(capture())

	return updated.(model)
}

func TestAgentsTabShowsCursorAgentPaneBesideList(t *testing.T) {
	m, recorder := agentsPreviewModel(t, 160)

	m, cmd := pressKey(t, m, "tab")
	if m.view != viewAgents {
		t.Fatalf("view = %v, want agents", m.view)
	}
	m = settlePreview(t, m, cmd)

	if !strings.Contains(m.View(), "Allow edit of main.go? [y/n]") {
		t.Fatalf("agents tab missing pane preview:\n%s", m.View())
	}
	if len(recorder.targets) != 1 || recorder.targets[0] != "@/a" {
		t.Fatalf("capture targets = %v, want [@/a]", recorder.targets)
	}
}

func TestAgentsTabShowsListAloneWhenNarrow(t *testing.T) {
	m, recorder := agentsPreviewModel(t, 90)
	m.view = viewAgents

	if cmd := m.syncAgentPreview(); cmd != nil {
		t.Fatal("narrow agents tab should not start a preview")
	}
	if len(recorder.targets) != 0 {
		t.Fatalf("capture targets = %v, want none", recorder.targets)
	}
}

func TestLeavingAgentsTabStopsPreviewRefresh(t *testing.T) {
	m, _ := agentsPreviewModel(t, 160)
	m.view = viewAgents
	m = settlePreview(t, m, m.syncAgentPreview())
	seq := m.agentPreview.seq

	m, _ = pressKey(t, m, "tab")
	updated, cmd := m.Update(previewRefreshMsg{seq: seq})

	if cmd != nil || updated.(model).agentPreview.want != "" {
		t.Fatal("refresh after leaving the agents tab must stop the preview")
	}
}
