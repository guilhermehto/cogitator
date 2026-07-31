package ui

// help_test.go — tests for the '?' floating help overlay: opening it, the
// any-key dismissal, and the View-level smoke test that the box is composited
// over the session list.

import (
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
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
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, []settings.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", settings.StateStopped, state.AttnInactive, fixedNow),
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

func TestHelpSections_WorkspacesKeys(t *testing.T) {
	var got []string
	for _, sec := range helpSections {
		if sec.title == "Workspaces" {
			for _, b := range sec.bindings {
				got = append(got, b[0])
			}
		}
	}
	want := []string{"tab", "N", "n", "e", "D", "enter"}
	if len(got) != len(want) {
		t.Fatalf("Workspaces section keys = %v, want %v", got, want)
	}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("Workspaces section keys = %v, want %v", got, want)
		}
	}
}

func TestHelpSections_NoTasksSectionOrTBinding(t *testing.T) {
	for _, sec := range helpSections {
		if sec.title == "Tasks" {
			t.Fatal("helpSections must not contain a Tasks section (Taskwarrior removed)")
		}
		for _, b := range sec.bindings {
			if b[0] == "T" {
				t.Fatalf("helpSections must not bind 'T' (Taskwarrior removed); found in section %q: %q", sec.title, b[1])
			}
		}
	}
}

func TestView_HelpOverlayAt80x24_FitsWithoutTruncatingKeys(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, nil)
	m.width, m.height = 80, 24
	m.prompt = promptHelp

	view := m.View()
	if !strings.Contains(view, "Workspaces") {
		t.Fatal("help overlay at 80x24 must show the Workspaces section title")
	}
	for _, key := range []string{"tab", "N", "n", "e", "D", "enter"} {
		if !strings.Contains(view, key) {
			t.Errorf("help overlay at 80x24 must show key %q untruncated", key)
		}
	}
}
