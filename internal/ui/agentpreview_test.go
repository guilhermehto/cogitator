package ui

import (
	"image/color"
	"math"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"

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

// previewBody renders a's preview of captured and returns the lines below
// the separator rule.
func previewBody(t *testing.T, captured string, width int) []string {
	t.Helper()
	a := previewAgent("/a")
	p := agentPreview{agentID: a.ID, text: captured}

	lines := strings.Split(p.View(a, agentListNow, width, 20), "\n")
	for i, line := range lines {
		if strings.Contains(line, "───") {
			return lines[i+1:]
		}
	}

	t.Fatalf("preview has no separator:\n%s", strings.Join(lines, "\n"))
	return nil
}

func TestPreviewKeepsStylingAndDropsOtherControlSequences(t *testing.T) {
	tests := []struct {
		name     string
		captured string
		want     []string
	}{
		{
			name:     "SGR kept, screen clear and hyperlink dropped, styled blank tail trimmed",
			captured: "\x1b[31mred\x1b[0m\n\x1b[2Jx\x1b]8;;http://a\x07l\x1b]8;;\x07\n\x1b[44m   \n",
			want:     []string{"\x1b[2;31mred" + ansi.ResetStyle, "\x1b[2mxl" + ansi.ResetStyle},
		},
		{
			name:     "private CSI m, cursor moves and bare control bytes dropped",
			captured: "\x1b[>4;1ma\x1b[5;1Hb\r\tc\x07\x1b[1md",
			want:     []string{"\x1b[2mabc" + ansi.ResetStyle + "\x1b[1;2md" + ansi.ResetStyle},
		},
		{
			name:     "styling carried across a line break is repeated on the next line",
			captured: "\x1b[31m-one\n-two\x1b[0m",
			want:     []string{"\x1b[2;31m-one" + ansi.ResetStyle, "\x1b[2;31m-two" + ansi.ResetStyle},
		},
		{
			name:     "turning bold off keeps the colour",
			captured: "\x1b[1;31ma\x1b[22mb",
			want:     []string{"\x1b[1;2;31ma" + ansi.ResetStyle + "\x1b[2;31mb" + ansi.ResetStyle},
		},
		{
			name:     "pane background dropped, foreground kept muted",
			captured: "\x1b[38;2;136;192;208m\x1b[48;2;46;52;64mtext\x1b[39m\x1b[48;2;46;52;64m pad\x1b[41m!",
			want:     []string{"\x1b[2;38;2;160;187;195mtext" + ansi.ResetStyle + "\x1b[2m pad!" + ansi.ResetStyle},
		},
		{
			name:     "a colour carried through a later SGR is muted once",
			captured: "\x1b[38;2;136;192;208ma\x1b[1mb\x1b[3mc",
			want: []string{"\x1b[2;38;2;160;187;195ma" + ansi.ResetStyle + "\x1b[1;2;38;2;160;187;195mb" +
				ansi.ResetStyle + "\x1b[1;2;3;38;2;160;187;195mc" + ansi.ResetStyle},
		},
		{
			name:     "text stays faint through a reset, captured faint and normal intensity",
			captured: "a\x1b[0mb\x1b[1;2mc\x1b[22md",
			want:     []string{"\x1b[2mab" + ansi.ResetStyle + "\x1b[1;2mc" + ansi.ResetStyle + "\x1b[2md" + ansi.ResetStyle},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewBody(t, tt.captured, 60); !slices.Equal(got, tt.want) {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMutedColorKeepsThemeColoursAndHalvesFixedChroma(t *testing.T) {
	for _, c := range []color.Color{nil, ansi.Red, ansi.BrightCyan, ansi.IndexedColor(9)} {
		if got := mutedColor(c); got != c {
			t.Errorf("mutedColor(%v) = %v, want the theme colour kept", c, got)
		}
	}

	grey := ansi.RGBColor{R: 128, G: 128, B: 128}
	if got := mutedColor(grey); got != grey {
		t.Errorf("mutedColor(grey) = %v, want it unchanged", got)
	}

	for _, c := range []color.Color{ansi.IndexedColor(196), ansi.RGBColor{R: 215, G: 119, B: 87}} {
		before, _ := colorful.MakeColor(c)
		after, _ := colorful.MakeColor(mutedColor(c))
		l0, c0, h0 := before.OkLch()
		l1, c1, h1 := after.OkLch()
		if math.Abs(l1-l0) > 0.01 || math.Abs(c1-c0*previewChroma) > 0.01 || math.Abs(h1-h0) > 2 {
			t.Errorf("mutedColor(%v) = lch(%.3f %.3f %.1f), want lch(%.3f %.3f %.1f)", c, l1, c1, h1, l0, c0*previewChroma, h0)
		}
	}
}

func TestPreviewResetsStylingAtEndOfTruncatedLine(t *testing.T) {
	body := previewBody(t, "\x1b[32m"+strings.Repeat("g", 80)+"\nnext", 20)

	if len(body) != 2 {
		t.Fatalf("body = %q, want 2 lines", body)
	}
	for _, line := range body {
		if ansi.StringWidth(line) > 20 || !strings.HasSuffix(line, ansi.ResetStyle) {
			t.Errorf("line %q must fit 20 cells and end with a reset", line)
		}
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
