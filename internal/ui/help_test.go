package ui

// help_test.go — tests for the '?' floating help overlay: opening it, the
// any-key dismissal, and the View-level smoke test that the box is composited
// over the session list.

import (
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

func TestQuestionMark_OpensHelpOverlay(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, nil)

	updated, _ := m.Update(keyMsg("?"))
	if got := updated.(model).prompt; got != promptHelp {
		t.Fatalf("'?' should open the help overlay; prompt = %v", got)
	}
}

func TestHelpOverlay_DismissedByAnyKey(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, nil)
	m.prompt = promptHelp

	updated, _ := m.Update(keyMsg("j"))
	if got := updated.(model).prompt; got != promptIdle {
		t.Fatalf("any key should close the help overlay; prompt = %v", got)
	}
}

func TestView_HelpOverlaysBoxOverSessions(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
	})
	m.width, m.height = 100, 30
	m.prompt = promptHelp

	view := m.View()
	if !strings.Contains(view, "Keybindings") {
		t.Error("View must render the help title while the overlay is open")
	}
	if !strings.Contains(view, "╭") {
		t.Error("View must render the floating box's rounded border")
	}
	if !strings.Contains(view, "any key to close") {
		t.Error("View must render the help footer hint")
	}
}

func TestView_HeaderPointsAtHelp(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, nil)
	m.width, m.height = 100, 30

	if !strings.Contains(m.View(), "? help") {
		t.Error("header must advertise the '?' help overlay")
	}
}
