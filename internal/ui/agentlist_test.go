package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
)

var agentListNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func worktreeAgent(id, repo, branch string, group agents.Group, attn state.Attention, st settings.RowState) agents.Agent {
	last := agentListNow.Add(-5 * time.Minute)
	return agents.Agent{
		ID: id, Dir: id, Kind: agents.KindWorktree, Repo: repo, Branch: branch,
		Harness: "opencode", Group: group, Attention: attn, State: st,
		LastActivity: last, OrderedAt: last,
	}
}

func agentsInEveryGroup() []agents.Agent {
	return []agents.Agent{
		worktreeAgent("/a", "api", "fix-auth", agents.GroupNeedsYou, state.AttnPermissionPending, settings.StateRunning),
		worktreeAgent("/b", "web", "ui", agents.GroupDone, state.AttnFinished, settings.StateRunning),
		{ID: "/c", Dir: "/c", Kind: agents.KindWorkspace, Workspace: "shop", SessionName: "checkout", Harness: "claude", Group: agents.GroupWorking, Attention: state.AttnActive, State: settings.StateRunning},
		{ID: "/tmp/scratch", Dir: "/tmp/scratch", Kind: agents.KindStray, Harness: "opencode", Group: agents.GroupIdle, Attention: state.AttnInactive, State: settings.StateRunning},
		worktreeAgent("/e", "docs", "main", agents.GroupStopped, state.AttnInactive, settings.StateStopped),
	}
}

func typeKeys(l *agentList, keys ...string) agentListResult {
	var result agentListResult
	for _, k := range keys {
		result = l.HandleKey(keyMsg(k))
	}

	return result
}

func TestAgentListMatchesSidebarSectionsAndOldestFirstOrder(t *testing.T) {
	at := func(branch string, group agents.Group, age time.Duration) agents.Agent {
		a := worktreeAgent("/"+branch, "r", branch, group, state.AttnInactive, settings.StateRunning)
		a.LastActivity = agentListNow.Add(-age)
		if age == 0 {
			a.LastActivity = time.Time{}
		}
		a.OrderedAt = a.LastActivity
		return a
	}
	l := newAgentList()
	l.SetAgents([]agents.Agent{
		at("ask-new", agents.GroupNeedsYou, time.Minute),
		at("ask-old", agents.GroupNeedsYou, 10*time.Minute),
		at("done", agents.GroupDone, 3*time.Minute),
		at("unknown", agents.GroupWorking, 0),
		at("idle", agents.GroupIdle, 8*time.Minute),
		at("stopped", agents.GroupStopped, 20*time.Minute),
	})

	view := ansi.Strip(l.View(agentListNow, 60, 40))

	last := -1
	for _, want := range []string{"Needs you · 2", "r·ask-old", "r·ask-new", "Working · 1", "r·unknown", "Done · 1", "r·done", "Idle · 2", "r·stopped", "r·idle"} {
		idx := strings.Index(view, want)
		if idx < 0 || idx < last {
			t.Fatalf("%q missing or out of order in:\n%s", want, view)
		}
		last = idx
	}
}

func TestAgentListNamesRowsByKind(t *testing.T) {
	l := newAgentList()
	l.SetAgents(agentsInEveryGroup())

	view := l.View(agentListNow, 100, 40)

	for _, want := range []string{"api·fix-auth", "shop/checkout", "scratch stray"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestAgentListLabelsWorktreesByRepoNameOrHomeRelativeDir(t *testing.T) {
	previous := agentHomeDir
	agentHomeDir = "/Users/x"
	t.Cleanup(func() { agentHomeDir = previous })

	tests := []struct {
		name  string
		agent agents.Agent
		want  string
	}{
		{"uses repo basename and branch", worktreeAgent("/Users/x/src/pi-platform", "/Users/x/src/pi-platform", "main", agents.GroupIdle, state.AttnInactive, settings.StateRunning), "pi-platform·main"},
		{"falls back to the home-relative dir without a branch", worktreeAgent("/Users/x/src/pi-platform/svc/api", "/Users/x/src/pi-platform", "", agents.GroupIdle, state.AttnInactive, settings.StateRunning), "~/src/pi-platform/svc/api"},
		{"keeps dirs outside home absolute", worktreeAgent("/opt/svc", "/opt/svc", "", agents.GroupIdle, state.AttnInactive, settings.StateRunning), "/opt/svc"},
		{"abbreviates stray dirs and marks them", agents.Agent{ID: "/Users/x/scratch", Dir: "/Users/x/scratch", Kind: agents.KindStray}, "~/scratch stray"},
		{"leads with the tab label", agents.Agent{ID: "/Users/x/.lexicanum#s1", Dir: "/Users/x/.lexicanum", Kind: agents.KindStray, Tab: "LEX-123"}, "LEX-123 · ~/.lexicanum stray"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentName(tt.agent); got != tt.want {
				t.Fatalf("agentName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentListShowsFullClaudeCodeHarnessAtPopupWidth(t *testing.T) {
	a := worktreeAgent("/a", "/r/api", "main", agents.GroupIdle, state.AttnInactive, settings.StateRunning)
	a.Harness = "claude-code"
	l := newAgentList()
	l.SetAgents([]agents.Agent{a})

	view := l.View(agentListNow, 100, 40)

	if !strings.Contains(view, "claude-code") {
		t.Fatalf("harness truncated in:\n%s", view)
	}
}

func TestAgentListShowsHarnessOnWideRowsOnly(t *testing.T) {
	a := worktreeAgent("/a", "/r/api", "main", agents.GroupIdle, state.AttnInactive, settings.StateRunning)
	l := newAgentList()
	l.SetAgents([]agents.Agent{a})

	wide, narrow := l.View(agentListNow, 100, 40), l.View(agentListNow, 50, 40)

	if !strings.Contains(wide, "opencode") {
		t.Fatalf("wide row missing harness:\n%s", wide)
	}
	if strings.Contains(narrow, "opencode") {
		t.Fatalf("narrow row should keep only status, name and age:\n%s", narrow)
	}
	if !strings.Contains(narrow, "api·main") || !strings.Contains(narrow, "5m") {
		t.Fatalf("narrow row missing name or age:\n%s", narrow)
	}
}

func TestAgentListPutsTabLabelAndTitleUnderTheName(t *testing.T) {
	a := worktreeAgent("/a", "/r/api", "main", agents.GroupIdle, state.AttnInactive, settings.StateRunning)
	a.Tab = "LEX-123"
	a.Title = "Fix flaky test"
	l := newAgentList()
	l.SetAgents([]agents.Agent{a})

	lines := strings.Split(ansi.Strip(l.View(agentListNow, 50, 40)), "\n")

	if len(lines) != 3 {
		t.Fatalf("want header and a two-line row, got:\n%s", strings.Join(lines, "\n"))
	}
	if name := lines[1]; !strings.HasPrefix(name, glyphInactive+" api·main ") || !strings.HasSuffix(name, " 5m") {
		t.Fatalf("first line = %q, want status, name and age", name)
	}
	if detail := strings.TrimRight(lines[2], " "); detail != "  LEX-123  Fix flaky test" {
		t.Fatalf("second line = %q, want tab label and title under the name", detail)
	}
}

func TestAgentListCursorBandCoversBothRowLines(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	orig := r.ColorProfile()
	r.SetColorProfile(termenv.ANSI256)
	defer r.SetColorProfile(orig)

	l := newAgentList()
	l.SetAgents(agentsInEveryGroup()[:2])
	open, _, _ := strings.Cut(lipgloss.NewStyle().Background(wtSelectedBg).Render("\x00"), "\x00")

	lines := strings.Split(l.View(agentListNow, 50, 40), "\n")

	for _, i := range []int{1, 2} {
		if !strings.HasPrefix(lines[i], open) {
			t.Fatalf("cursor row line %d lacks the selection band: %q", i, lines[i])
		}
	}
	if strings.Contains(strings.Join(lines[3:], "\n"), open) {
		t.Fatalf("band leaked past the cursor row:\n%s", strings.Join(lines, "\n"))
	}
}

func TestAgentListMutesRepoAndDimsItForStoppedAgents(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	orig := r.ColorProfile()
	r.SetColorProfile(termenv.ANSI256)
	defer r.SetColorProfile(orig)

	running := worktreeAgent("/r", "/src/api", "main", agents.GroupIdle, state.AttnInactive, settings.StateRunning)
	stopped := worktreeAgent("/s", "/src/docs", "main", agents.GroupStopped, state.AttnInactive, settings.StateStopped)
	l := newAgentList()
	l.SetAgents([]agents.Agent{running, stopped})

	view := l.View(agentListNow, 60, 40)

	for _, want := range []string{"38;5;103mapi", "38;5;240mdocs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%q", want, view)
		}
	}
}

func TestAgentListTellsStoppedAgentsFromIdleOnesByGlyph(t *testing.T) {
	idle := worktreeAgent("/i", "/src/api", "main", agents.GroupIdle, state.AttnInactive, settings.StateRunning)
	stopped := worktreeAgent("/s", "/src/docs", "main", agents.GroupStopped, state.AttnInactive, settings.StateStopped)
	l := newAgentList()
	l.SetAgents([]agents.Agent{idle, stopped})

	rows := map[string]string{}
	for _, line := range strings.Split(ansi.Strip(l.View(agentListNow, 60, 40)), "\n") {
		for _, name := range []string{"api·main", "docs·main"} {
			if strings.Contains(line, name) {
				rows[name] = line
			}
		}
	}

	if !strings.HasPrefix(rows["api·main"], glyphInactive) {
		t.Fatalf("idle row = %q, want the hollow circle", rows["api·main"])
	}
	if !strings.HasPrefix(rows["docs·main"], glyphWtStopped) || strings.Contains(rows["docs·main"], glyphInactive) {
		t.Fatalf("stopped row = %q, want the stop glyph only", rows["docs·main"])
	}
}

func TestAgentListShowsTabLabelOnNarrowRowsWithoutRepeatingItAsTitle(t *testing.T) {
	a := agents.Agent{ID: "/l#s1", Dir: "/l", Kind: agents.KindStray, Harness: "omp", Title: "Fix login", Tab: "Fix login", State: settings.StateRunning}
	l := newAgentList()
	l.SetAgents([]agents.Agent{a})

	narrow, wide := l.View(agentListNow, 50, 40), l.View(agentListNow, 100, 40)

	if !strings.Contains(narrow, "Fix login") {
		t.Fatalf("narrow row missing tab label:\n%s", narrow)
	}
	if got := strings.Count(wide, "Fix login"); got != 1 {
		t.Fatalf("wide row shows the label %d times:\n%s", got, wide)
	}
}

func TestAgentListUsesStatusGlyphsOfMainTUI(t *testing.T) {
	l := newAgentList()
	l.SetAgents(agentsInEveryGroup())

	view := l.View(agentListNow, 100, 40)

	for _, glyph := range []string{glyphPermission, glyphFinished, glyphActive, glyphInactive} {
		if !strings.Contains(view, glyph) {
			t.Fatalf("view missing glyph %q:\n%s", glyph, view)
		}
	}
}

func TestAgentListCursorStaysOnAgentAcrossReSort(t *testing.T) {
	l := newAgentList()
	list := agentsInEveryGroup()
	l.SetAgents(list)
	typeKeys(&l, "j")
	if l.cursorID != "/c" {
		t.Fatalf("cursor = %q, want /c", l.cursorID)
	}

	reordered := []agents.Agent{list[3], list[1], list[0], list[4], list[2]}
	l.SetAgents(reordered)

	if l.cursorID != "/c" {
		t.Fatalf("cursor moved to %q after re-sort, want /c", l.cursorID)
	}
}

func TestAgentListCursorFallsBackWhenAgentDisappears(t *testing.T) {
	l := newAgentList()
	list := agentsInEveryGroup()
	l.SetAgents(list)
	typeKeys(&l, "G")

	l.SetAgents(list[:2])

	if l.cursorID != "/a" {
		t.Fatalf("cursor = %q, want first row /a", l.cursorID)
	}
}

func TestAgentListNavigationKeys(t *testing.T) {
	l := newAgentList()
	l.SetAgents(agentsInEveryGroup())

	typeKeys(&l, "G")
	if l.cursorID != "/tmp/scratch" {
		t.Fatalf("G cursor = %q, want /tmp/scratch", l.cursorID)
	}

	typeKeys(&l, "g", "g")
	if l.cursorID != "/a" {
		t.Fatalf("gg cursor = %q, want /a", l.cursorID)
	}

	typeKeys(&l, "j", "j", "k")
	if l.cursorID != "/c" {
		t.Fatalf("j j k cursor = %q, want /c", l.cursorID)
	}
}

func TestAgentListFilterKeepsOnlyMatchesAndJumpsToThem(t *testing.T) {
	l := newAgentList()
	l.SetAgents(agentsInEveryGroup())

	typeKeys(&l, "/", "a", "p", "i")
	view := l.View(agentListNow, 100, 40)
	if strings.Contains(view, "web·ui") || strings.Contains(view, "docs·main") {
		t.Fatalf("non-matches still shown:\n%s", view)
	}

	result := typeKeys(&l, "enter")
	if result.intent != agentIntentJump || result.agentID != "/a" {
		t.Fatalf("result = %+v, want jump to /a", result)
	}
}

func TestAgentListEscapeClearsFilterBeforeQuitting(t *testing.T) {
	l := newAgentList()
	l.SetAgents(agentsInEveryGroup())
	typeKeys(&l, "/", "a", "p", "i")

	result := typeKeys(&l, "esc")

	if result.intent != agentIntentNone || len(l.visible()) != 5 {
		t.Fatalf("esc should only clear the filter; result=%+v visible=%d", result, len(l.visible()))
	}
	if got := typeKeys(&l, "q"); got.intent != agentIntentQuit {
		t.Fatalf("q intent = %v, want quit", got.intent)
	}
}

func TestAgentListSeenTargetsCursorAgent(t *testing.T) {
	l := newAgentList()
	l.SetAgents(agentsInEveryGroup())
	typeKeys(&l, "j")

	result := typeKeys(&l, "x")

	if result.intent != agentIntentSeen || result.agentID != "/c" {
		t.Fatalf("result = %+v, want seen /c", result)
	}
}

func TestAgentListWindowKeepsWholeCursorRowVisible(t *testing.T) {
	list := agentsInEveryGroup()
	list[3].Tab = "notes"
	l := newAgentList()
	l.SetAgents(list)
	typeKeys(&l, "G")

	for _, height := range []int{2, 4} {
		view := l.View(agentListNow, 100, height)

		if got := len(strings.Split(view, "\n")); got != height {
			t.Fatalf("height %d: view has %d lines", height, got)
		}
		if !strings.Contains(view, "scratch stray") || !strings.Contains(view, "notes") {
			t.Fatalf("height %d: cursor row not wholly in view:\n%s", height, view)
		}
	}
}
