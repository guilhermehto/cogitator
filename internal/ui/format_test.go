package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/guilhermehto/cogitator/internal/state"
)

func TestFormatRowAgentBeforeTitleAndDedupesAgentSuffix(t *testing.T) {
	sv := state.SessionView{
		SessionID:  "s1",
		Title:      "refactor parser (scribe)",
		Agent:      "scribe",
		StatusType: "busy",
		Attention:  state.AttnActive,
		Source:     state.SourceLive,
	}

	row := formatRow(time.Now(), sv, 120, false)
	if !strings.Contains(row, "@scribe refactor parser") {
		t.Fatalf("row = %q, want agent before title", row)
	}
	if strings.Contains(row, "(scribe)") {
		t.Fatalf("row = %q, want duplicate agent suffix removed", row)
	}
}

func TestFormatRowFoldsMultilineTitleIntoOneLine(t *testing.T) {
	row := formatRow(time.Now(), state.SessionView{
		SessionID:  "s1",
		Title:      "first line\nsecond line\r\nthird line",
		StatusType: "busy",
		Attention:  state.AttnActive,
		Source:     state.SourceLive,
	}, 120, false)

	if strings.ContainsAny(row, "\r\n") {
		t.Fatalf("row must not contain embedded line breaks: %q", row)
	}
	if !strings.Contains(row, "first line second line third line") {
		t.Fatalf("row = %q, want folded title text", row)
	}
}

func TestTrimAgentSuffix(t *testing.T) {
	cases := []struct {
		name  string
		title string
		agent string
		want  string
	}{
		{"exact match", "refactor parser (scribe)", "scribe", "refactor parser"},
		{"case mismatch", "refactor parser (Scribe)", "scribe", "refactor parser"},
		{"display vs canonical", "refactor parser (general-purpose)", "general", "refactor parser"},
		{"trailing whitespace", "refactor parser (scribe)  ", "scribe", "refactor parser"},
		{"nested parens", "Foo (bar (baz))", "scribe", "Foo"},
		{"no agent keeps parens", "Fix bug (urgent)", "", "Fix bug (urgent)"},
		{"no trailing parens", "refactor parser", "scribe", "refactor parser"},
		{"whole title parenthesised", "(scribe)", "scribe", "(scribe)"},
		{"unbalanced parens", "refactor parser scribe)", "scribe", "refactor parser scribe)"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := trimAgentSuffix(tc.title, tc.agent)
			if got != tc.want {
				t.Fatalf("trimAgentSuffix(%q, %q) = %q, want %q", tc.title, tc.agent, got, tc.want)
			}
		})
	}
}

func TestFormatRelativeBoundaries(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero time renders empty", time.Time{}, ""},
		{"now (0 diff)", now, "now"},
		{"30s under a minute", now.Add(-30 * time.Second), "now"},
		{"exactly 1m", now.Add(-time.Minute), "1m"},
		{"59m just under an hour", now.Add(-59 * time.Minute), "59m"},
		{"exactly 60m flips to 1h", now.Add(-60 * time.Minute), "1h"},
		{"24h", now.Add(-24 * time.Hour), "24h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatRelative(now, c.t)
			if got != c.want {
				t.Fatalf("formatRelative(%v) = %q, want %q", c.t, got, c.want)
			}
		})
	}
}

func TestShortenDirectory(t *testing.T) {
	prev := homeDir
	defer func() { homeDir = prev }()
	homeDir = "/home/me"

	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/home/me", "~"},
		{"/home/me/src/foo", "~/src/foo"},
		{"/home/me/src/foo/bar/baz", "~/src/foo/bar/baz"},
		{"/tmp/work", "/tmp/work"},
		{"/home/melissa/src", "/home/melissa/src"},
	}
	for _, c := range cases {
		if got := shortenDirectory(c.in); got != c.want {
			t.Errorf("shortenDirectory(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortenDirectoryNoHome(t *testing.T) {
	prev := homeDir
	defer func() { homeDir = prev }()
	homeDir = ""
	if got := shortenDirectory("/Users/g/src/foo"); got != "/Users/g/src/foo" {
		t.Errorf("shortenDirectory with empty homeDir = %q, want path unchanged", got)
	}
}

func TestFormatRowShowsShortenedDirectory(t *testing.T) {
	prev := homeDir
	defer func() { homeDir = prev }()
	homeDir = "/home/me"

	sv := state.SessionView{
		SessionID:  "s1",
		Title:      "alpha",
		Directory:  "/home/me/src/foo",
		StatusType: "busy",
		Attention:  state.AttnActive,
		Source:     state.SourceLive,
	}
	row := formatRow(time.Now(), sv, 120, false)
	if !strings.Contains(row, "~/src/foo") {
		t.Fatalf("row = %q, want shortened directory ~/src/foo", row)
	}
	if strings.Contains(row, "/home/me/src/foo") {
		t.Fatalf("row = %q, want raw $HOME path replaced", row)
	}
}

func TestFormatRowOmitsDirectoryOnChild(t *testing.T) {
	prev := homeDir
	defer func() { homeDir = prev }()
	homeDir = "/home/me"

	sv := state.SessionView{
		SessionID:  "child",
		ParentID:   "root",
		Title:      "child-work",
		Directory:  "/home/me/src/foo",
		Agent:      "scribe",
		StatusType: "busy",
		Attention:  state.AttnActive,
		Source:     state.SourceLive,
	}
	row := formatRow(time.Now(), sv, 120, true)
	if strings.Contains(row, "~/src/foo") || strings.Contains(row, "/home/me/src/foo") {
		t.Fatalf("child row = %q, must not render directory", row)
	}
}

// cellOffsetAfter returns the visible cell offset just past the first
// occurrence of substr in s, or -1 if substr is not found.
func cellOffsetAfter(s, substr string) int {
	idx := strings.Index(s, substr)
	if idx < 0 {
		return -1
	}
	return lipgloss.Width(s[:idx+len(substr)])
}

func TestColumnHeaderAndRowAgreeOnWidth(t *testing.T) {
	sv := state.SessionView{
		SessionID:    "s1",
		Title:        "some title",
		Agent:        "scribe",
		StatusType:   "busy",
		Attention:    state.AttnActive,
		Source:       state.SourceLive,
		LastActivity: time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC),
	}
	now := time.Date(2026, 5, 15, 12, 5, 0, 0, time.UTC)
	for _, w := range []int{60, 80, 120, 200} {
		header := columnHeader(w)
		row := formatRow(now, sv, w, false)
		if hw := lipgloss.Width(header); hw != w {
			t.Errorf("width %d: header visible width = %d, want %d (%q)", w, hw, w, header)
		}
		if rw := lipgloss.Width(row); rw != w {
			t.Errorf("width %d: row visible width = %d, want %d (%q)", w, rw, w, row)
		}
	}
}

func TestFormatRowStatusUnderStatusHeader(t *testing.T) {
	width := 120
	header := columnHeader(width)
	row := formatRow(time.Now(), state.SessionView{
		SessionID:  "s1",
		Title:      "alpha",
		StatusType: "busy",
		Attention:  state.AttnActive,
		Source:     state.SourceLive,
	}, width, false)

	statusEnd := cellOffsetAfter(header, "STATUS")
	busyEnd := cellOffsetAfter(row, "busy")
	if statusEnd < 0 || busyEnd < 0 {
		t.Fatalf("missing fragments: STATUS=%d busy=%d\nheader=%q\nrow=%q", statusEnd, busyEnd, header, row)
	}
	if statusEnd != busyEnd {
		t.Errorf("STATUS ends at cell %d, busy ends at cell %d\nheader=%q\nrow=%q", statusEnd, busyEnd, header, row)
	}
}

func TestFormatRowChildStateCellWithinColumn(t *testing.T) {
	width := 120
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	sv := state.SessionView{
		SessionID:    "child",
		ParentID:     "root",
		Title:        "child-work",
		Agent:        "scribe",
		StatusType:   "busy",
		Attention:    state.AttnActive,
		Source:       state.SourceLive,
		LastActivity: now.Add(-5 * time.Minute),
	}
	childRow := formatRow(now, sv, width, true)
	if rw := lipgloss.Width(childRow); rw != width {
		t.Fatalf("child row visible width = %d, want %d (%q)", rw, width, childRow)
	}

	parentSv := sv
	parentSv.ParentID = ""
	parentSv.Directory = ""
	parentRow := formatRow(now, parentSv, width, false)
	parentBusy := cellOffsetAfter(parentRow, "busy")
	childBusy := cellOffsetAfter(childRow, "busy")
	if parentBusy < 0 || childBusy < 0 {
		t.Fatalf("missing 'busy' fragment: parent=%d child=%d\nparent=%q\nchild=%q", parentBusy, childBusy, parentRow, childRow)
	}
	if parentBusy != childBusy {
		t.Errorf("STATUS value ends at cell %d on parent, %d on child\nparent=%q\nchild=%q", parentBusy, childBusy, parentRow, childRow)
	}
}

func TestFormatRowActivityUnderActivityHeader(t *testing.T) {
	width := 120
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	header := columnHeader(width)
	row := formatRow(now, state.SessionView{
		SessionID:    "s1",
		Title:        "alpha",
		Attention:    state.AttnInactive,
		Source:       state.SourceLive,
		LastActivity: now.Add(-5 * time.Minute),
	}, width, false)

	activityEnd := cellOffsetAfter(header, "ACTIVITY")
	valueEnd := cellOffsetAfter(row, "5m")
	if activityEnd < 0 || valueEnd < 0 {
		t.Fatalf("missing fragments: ACTIVITY=%d value=%d\nheader=%q\nrow=%q", activityEnd, valueEnd, header, row)
	}
	if activityEnd != valueEnd {
		t.Errorf("ACTIVITY ends at cell %d, 5m ends at cell %d\nheader=%q\nrow=%q", activityEnd, valueEnd, header, row)
	}
}
