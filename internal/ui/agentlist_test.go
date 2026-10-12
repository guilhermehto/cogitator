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
		ID: id, Kind: agents.KindWorktree, Repo: repo, Branch: branch,
		Harness: "opencode", Group: group, Attention: attn, State: st,
		LastActivity: agentListNow.Add(-5 * time.Minute),
	}
}

func agentsInEveryGroup() []agents.Agent {
	return []agents.Agent{
		worktreeAgent("/a", "api", "fix-auth", agents.GroupNeedsYou, state.AttnPermissionPending, settings.StateRunning),
		worktreeAgent("/b", "web", "ui", agents.GroupDone, state.AttnFinished, settings.StateRunning),
		{ID: "/c", Kind: agents.KindWorkspace, Workspace: "shop", SessionName: "checkout", Harness: "claude", Group: agents.GroupWorking, Attention: state.AttnActive, State: settings.StateRunning},
		{ID: "/tmp/scratch", Kind: agents.KindStray, Harness: "opencode", Group: agents.GroupIdle, Attention: state.AttnInactive, State: settings.StateRunning},
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
	for _, title := range []string{"Needs you (1)", "Done (1)", "Working (1)", "Idle (1)", "Stopped (1)"} {
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
