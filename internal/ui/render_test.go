package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/state"
)

func TestRenderAllSessionsRedactsInstanceHostPort(t *testing.T) {
	m := model{}
	rows := []state.SessionView{
		{
			InstanceID:   "a",
			InstanceName: "10.0.0.1:1234",
			SessionID:    "s1",
			Title:        "alpha",
			StatusType:   "idle",
			Attention:    state.AttnInactive,
			Source:       state.SourceLive,
		},
		{
			InstanceID:   "b",
			InstanceName: "10.0.0.2:5678",
			SessionID:    "s2",
			Title:        "beta",
			StatusType:   "busy",
			Attention:    state.AttnActive,
			Source:       state.SourceLive,
		},
	}

	rendered := m.renderAllSessions(120, rows, nil)
	if strings.Contains(rendered, "Instance 1") || strings.Contains(rendered, "Instance 2") {
		t.Fatalf("rendered = %q, want instance labels removed", rendered)
	}
	if strings.Contains(rendered, "10.0.0.1:1234") || strings.Contains(rendered, "10.0.0.2:5678") {
		t.Fatalf("rendered = %q, want host:port redacted", rendered)
	}
}

func TestViewDoesNotRenderNeedsAttentionPane(t *testing.T) {
	m := model{
		cfg:   config.Default(),
		width: 120,
		snap: state.Snapshot{
			UpdatedAt: time.Unix(0, 0),
			Sessions: []state.SessionView{
				{
					InstanceID:   "a",
					InstanceName: "inst-a",
					SessionID:    "s1",
					Title:        "alpha",
					StatusType:   "busy",
					Attention:    state.AttnPermissionPending,
					Source:       state.SourceLive,
				},
			},
		},
	}

	rendered := m.View()
	if strings.Contains(rendered, "Needs attention") {
		t.Fatalf("rendered = %q, want no needs-attention pane", rendered)
	}
	if !strings.Contains(rendered, "Sessions") {
		t.Fatalf("rendered = %q, want sessions pane", rendered)
	}
}

func TestRenderAllSessionsRecentMarkerSitsAboveRecentRows(t *testing.T) {
	m := model{width: 200, snap: state.Snapshot{UpdatedAt: time.Unix(0, 0)}}
	live := liveSessionView("live-row", "", "idle", state.AttnInactive)
	live.Title = "live-title"
	recent := recentSessionView("recent-row", "", "idle", state.AttnInactive)
	recent.Title = "recent-title"

	rendered := m.renderAllSessions(200, []state.SessionView{live, recent}, map[string]int{"inst-1": 1})

	livePos := strings.Index(rendered, "live-title")
	markerPos := strings.Index(rendered, "1 recent")
	recentPos := strings.Index(rendered, "recent-title")
	if livePos < 0 || markerPos < 0 || recentPos < 0 {
		t.Fatalf("missing fragment in rendered output: %q", rendered)
	}
	if !(livePos < markerPos && markerPos < recentPos) {
		t.Fatalf("expected order live -> marker -> recent, got positions live=%d marker=%d recent=%d in %q", livePos, markerPos, recentPos, rendered)
	}
}

func TestRenderAllSessionsCollapsedHidesRecentRowsButKeepsMarker(t *testing.T) {
	m := model{width: 200, recentCollapsed: true, snap: state.Snapshot{UpdatedAt: time.Unix(0, 0)}}
	visible, counts := visibleSessions([]state.SessionView{
		liveSessionView("live", "", "busy", state.AttnActive),
		recentSessionView("history", "", "idle", state.AttnInactive),
	}, true, time.Time{}, 0)

	rendered := m.renderAllSessions(200, visible, counts)
	if strings.Contains(rendered, "history") {
		t.Fatalf("collapsed view must not render recent row title, got %q", rendered)
	}
	if !strings.Contains(rendered, "1 recent") {
		t.Fatalf("collapsed view must still show recent count marker, got %q", rendered)
	}
}

func TestRenderAllSessionsRecentSectionIsUnifiedAtBottom(t *testing.T) {
	m := model{width: 200, snap: state.Snapshot{UpdatedAt: time.Unix(0, 0)}}

	liveA := liveSessionView("liveA-id", "", "busy", state.AttnActive)
	liveA.InstanceID = "instA"
	liveA.InstanceName = "instA"
	liveA.Title = "live-A-title"

	liveB := liveSessionView("liveB-id", "", "busy", state.AttnActive)
	liveB.InstanceID = "instB"
	liveB.InstanceName = "instB"
	liveB.Title = "live-B-title"

	recentA := recentSessionView("recentA-id", "", "idle", state.AttnInactive)
	recentA.InstanceID = "instA"
	recentA.InstanceName = "instA"
	recentA.Title = "recent-A-title"

	recentB := recentSessionView("recentB-id", "", "idle", state.AttnInactive)
	recentB.InstanceID = "instB"
	recentB.InstanceName = "instB"
	recentB.Title = "recent-B-title"

	rendered := m.renderAllSessions(200, []state.SessionView{liveA, liveB, recentA, recentB}, map[string]int{"instA": 1, "instB": 1})

	liveAPos := strings.Index(rendered, "live-A-title")
	liveBPos := strings.Index(rendered, "live-B-title")
	markerPos := strings.Index(rendered, "2 recent")
	recentAPos := strings.Index(rendered, "recent-A-title")
	recentBPos := strings.Index(rendered, "recent-B-title")

	if liveAPos < 0 || liveBPos < 0 || markerPos < 0 || recentAPos < 0 || recentBPos < 0 {
		t.Fatalf("missing fragment in rendered output: %q", rendered)
	}
	if liveAPos > markerPos || liveBPos > markerPos {
		t.Fatalf("expected live rows before recent marker; live-A=%d live-B=%d marker=%d", liveAPos, liveBPos, markerPos)
	}
	if recentAPos < markerPos || recentBPos < markerPos {
		t.Fatalf("expected recent rows after recent marker; recent-A=%d recent-B=%d marker=%d", recentAPos, recentBPos, markerPos)
	}
	if strings.Count(rendered, "▾ 2 recent")+strings.Count(rendered, "▸ 2 recent") != 1 {
		t.Fatalf("expected exactly one unified recent marker line, got rendered=%q", rendered)
	}
}

func TestViewRendersUnreachableFooter(t *testing.T) {
	m := model{
		cfg:   config.Default(),
		width: 120,
		snap: state.Snapshot{
			UpdatedAt: time.Unix(0, 0),
			UnreachableInstances: []state.InstanceFailure{
				{InstanceID: "127.0.0.1:7777", Host: "127.0.0.1", Port: 7777, ConsecutiveFailures: 3},
			},
		},
	}

	rendered := m.View()
	if !strings.Contains(rendered, "1 instance unreachable") {
		t.Fatalf("expected unreachable footer, got %q", rendered)
	}
	if !strings.Contains(rendered, "127.0.0.1:7777 (3 consecutive failures)") {
		t.Fatalf("expected instance details in footer, got %q", rendered)
	}
}

func TestTaskwarriorErrorFooterNilErrReturnsEmpty(t *testing.T) {
	if got := taskwarriorErrorFooter("add", nil); got != "" {
		t.Fatalf("expected empty string for nil error, got %q", got)
	}
}

func TestTaskwarriorErrorFooterRendersOpAndErr(t *testing.T) {
	got := taskwarriorErrorFooter("modify", fmt.Errorf("exit status 1"))
	if !strings.Contains(got, "task modify failed") {
		t.Fatalf("expected op in footer, got %q", got)
	}
	if !strings.Contains(got, "exit status 1") {
		t.Fatalf("expected error message in footer, got %q", got)
	}
}

func TestViewRendersMutationErrorFooter(t *testing.T) {
	m := model{
		cfg:             config.Default(),
		width:           120,
		snap:            state.Snapshot{UpdatedAt: time.Unix(0, 0)},
		lastMutationOp:  "done",
		lastMutationErr: fmt.Errorf("task not found"),
	}

	rendered := m.View()
	if !strings.Contains(rendered, "task done failed") {
		t.Fatalf("expected mutation-error footer with op, got %q", rendered)
	}
	if !strings.Contains(rendered, "task not found") {
		t.Fatalf("expected mutation-error footer with err, got %q", rendered)
	}
}

func TestViewMutationErrorFooterAbsentOnNilErr(t *testing.T) {
	m := model{
		cfg:             config.Default(),
		width:           120,
		snap:            state.Snapshot{UpdatedAt: time.Unix(0, 0)},
		lastMutationOp:  "add",
		lastMutationErr: nil,
	}

	rendered := m.View()
	if strings.Contains(rendered, "task add failed") {
		t.Fatalf("expected no mutation-error footer when err is nil, got %q", rendered)
	}
}

func TestLegendLineIncludesTaskGlyphsOnWideTerminal(t *testing.T) {
	// A very wide terminal should include the task-priority glyphs.
	got := legendLine(300)
	if !strings.Contains(got, "high") {
		t.Fatalf("expected task priority glyphs on wide terminal, got %q", got)
	}
	if !strings.Contains(got, "medium") {
		t.Fatalf("expected 'medium' glyph label on wide terminal, got %q", got)
	}
	if !strings.Contains(got, "low") {
		t.Fatalf("expected 'low' glyph label on wide terminal, got %q", got)
	}
}

func TestLegendLineOmitsTaskGlyphsOnNarrowTerminal(t *testing.T) {
	// A very narrow terminal must not include the task-priority glyphs.
	got := legendLine(40)
	if strings.Contains(got, "high") {
		t.Fatalf("expected task priority glyphs omitted on narrow terminal, got %q", got)
	}
	// Session glyphs must still be present.
	if !strings.Contains(got, "legend:") {
		t.Fatalf("expected session legend prefix on narrow terminal, got %q", got)
	}
}

func TestLegendLineZeroWidthIncludesTaskGlyphs(t *testing.T) {
	// width=0 means "unknown / unconstrained" — task glyphs should be included.
	got := legendLine(0)
	if !strings.Contains(got, "high") {
		t.Fatalf("expected task priority glyphs when width=0 (unconstrained), got %q", got)
	}
}
