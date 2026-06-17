package ui

// palette_render_test.go — rendering tests for the floating ctrl+P switcher:
// the ANSI-aware overlay compositor, a single result row's content, and a
// View-level smoke test that the box is composited over the session list.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

func TestOverlayBox_CentersAndPreservesBackdrop(t *testing.T) {
	// 5×5 backdrop of distinct rows; a 1-line, 3-wide box centred over it.
	bg := strings.Join([]string{"aaaaa", "bbbbb", "ccccc", "ddddd", "eeeee"}, "\n")
	out := overlayBox(bg, 5, 5, "XYZ")
	lines := strings.Split(out, "\n")

	if len(lines) != 5 {
		t.Fatalf("overlay must keep the backdrop's line count; got %d", len(lines))
	}
	// Untouched rows pass through unchanged.
	if ansi.Strip(lines[0]) != "aaaaa" || ansi.Strip(lines[4]) != "eeeee" {
		t.Errorf("uncovered rows changed: %q / %q", ansi.Strip(lines[0]), ansi.Strip(lines[4]))
	}
	// Centre row: fg width 3 over field width 5 → left margin 1, right margin 1.
	if got := ansi.Strip(lines[2]); got != "cXYZc" {
		t.Errorf("centre row = %q, want %q", got, "cXYZc")
	}
}

func TestOverlayBox_PadsShortBackdropToFieldHeight(t *testing.T) {
	// Two-line backdrop, six-line field: must pad to six lines so the box can
	// centre within the full pane rather than only the populated rows.
	out := overlayBox("top\nbot", 10, 6, "box")
	if n := len(strings.Split(out, "\n")); n != 6 {
		t.Errorf("short backdrop must be padded to field height; got %d lines, want 6", n)
	}
}

func TestRenderPaletteRow_ShowsRepoAndBranch(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, nil)
	row := makeRow("/home/me/alpha", "/home/me/alpha", "main", "title", workspace.StateStopped, state.AttnInactive, fixedNow)

	got := ansi.Strip(m.renderPaletteRow(row, "alpha main", "am", 40))
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "main") {
		t.Errorf("row text = %q, want it to contain repo and branch", got)
	}
}

func TestRenderPaletteRow_RepoOnlyLabelHasNoTrailingBranch(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, nil)
	row := makeRow("/home/me/alpha", "/home/me/alpha", "", "", workspace.StateStopped, state.AttnInactive, fixedNow)

	got := strings.TrimSpace(ansi.Strip(m.renderPaletteRow(row, "alpha", "", 40)))
	// The status glyph leads, "alpha" follows; nothing should trail it.
	if !strings.HasSuffix(got, "alpha") {
		t.Errorf("repo-only row = %q, want it to end with the repo name", got)
	}
}

func TestView_PaletteOverlaysSwitcherBox(t *testing.T) {
	m := makeTestModel(&fakeTmuxOps{available: true}, nil, &fakeHarnessOps{}, []workspace.Row{
		makeRow("/home/me/alpha", "/home/me/alpha", "main", "a", workspace.StateStopped, state.AttnInactive, fixedNow),
		makeRow("/home/me/beta", "/home/me/beta", "dev", "b", workspace.StateRunning, state.AttnActive, fixedNow),
	})
	m.width, m.height = 100, 30
	m = openPalette(t, m)

	view := m.View()
	if !strings.Contains(view, "Switch session") {
		t.Error("View must render the switcher title while the palette is open")
	}
	if !strings.Contains(view, "╭") {
		t.Error("View must render the floating box's rounded border")
	}
	if !strings.Contains(view, "2 sessions") {
		t.Error("View must render the switcher footer count")
	}
}
