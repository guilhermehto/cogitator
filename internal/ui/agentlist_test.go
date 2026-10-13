package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
)

var agentListNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func worktreeAgent(id, repo, branch string, group agents.Group, attn state.Attention, st settings.RowState) agents.Agent {
	return agents.Agent{
		ID: id, Dir: id, Kind: agents.KindWorktree, Repo: repo, Branch: branch,
		Harness: "opencode", Group: group, Attention: attn, State: st,
		LastActivity: agentListNow.Add(-5 * time.Minute),
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

func TestAgentListShowsGroupHeadersInOrder(t *testing.T) {
	l := newAgentList()
	l.SetAgents(agentsInEveryGroup())

	view := l.View(agentListNow, 100, 40)

	last := -1
	for _, title := range []string{"Needs you · 1", "Done · 1", "Working · 1", "Idle · 1", "Stopped · 1"} {
		idx := strings.Index(view, title)
		if idx < 0 || idx < last {
			t.Fatalf("header %q missing or out of order in:\n%s", title, view)
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

func TestAgentListShowsTitleAndHarnessOnWideRowsOnly(t *testing.T) {
	a := worktreeAgent("/a", "/r/api", "main", agents.GroupIdle, state.AttnInactive, settings.StateRunning)
	a.Title = "Fix flaky test"
	l := newAgentList()
	l.SetAgents([]agents.Agent{a})

	wide, narrow := l.View(agentListNow, 100, 40), l.View(agentListNow, 50, 40)

	if !strings.Contains(wide, "Fix flaky test") || !strings.Contains(wide, "opencode") {
		t.Fatalf("wide row missing title or harness:\n%s", wide)
	}
	if strings.Contains(narrow, "Fix flaky test") || strings.Contains(narrow, "opencode") {
		t.Fatalf("narrow row should keep only status, name and age:\n%s", narrow)
	}
	if !strings.Contains(narrow, "api·main") || !strings.Contains(narrow, "5m") {
		t.Fatalf("narrow row missing name or age:\n%s", narrow)
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
	if l.cursorID != "/b" {
		t.Fatalf("cursor = %q, want /b", l.cursorID)
	}

	reordered := []agents.Agent{list[3], list[1], list[0], list[4], list[2]}
	l.SetAgents(reordered)

	if l.cursorID != "/b" {
		t.Fatalf("cursor moved to %q after re-sort, want /b", l.cursorID)
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
	if l.cursorID != "/e" {
		t.Fatalf("G cursor = %q, want /e", l.cursorID)
	}

	typeKeys(&l, "g", "g")
	if l.cursorID != "/a" {
		t.Fatalf("gg cursor = %q, want /a", l.cursorID)
	}

	typeKeys(&l, "j", "j", "k")
	if l.cursorID != "/b" {
		t.Fatalf("j j k cursor = %q, want /b", l.cursorID)
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

	if result.intent != agentIntentSeen || result.agentID != "/b" {
		t.Fatalf("result = %+v, want seen /b", result)
	}
}

func TestAgentListWindowKeepsCursorVisible(t *testing.T) {
	l := newAgentList()
	l.SetAgents(agentsInEveryGroup())
	typeKeys(&l, "G")

	view := l.View(agentListNow, 100, 4)

	if got := len(strings.Split(view, "\n")); got != 4 {
		t.Fatalf("view has %d lines, want 4", got)
	}
	if !strings.Contains(view, "docs·main") {
		t.Fatalf("cursor row scrolled out of view:\n%s", view)
	}
}
