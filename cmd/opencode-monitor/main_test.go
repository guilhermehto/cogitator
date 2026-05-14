package main

import (
	"strings"
	"testing"
	"time"

	"github.com/goliveira/opencode-monitor/internal/state"
)

func makeSessionView(id, parentID, status string, attn state.Attention) state.SessionView {
	return state.SessionView{
		InstanceID:   "inst-1",
		InstanceName: "inst-1",
		SessionID:    id,
		ParentID:     parentID,
		StatusType:   status,
		Attention:    attn,
		Source:       state.SourceLive,
	}
}

func TestVisibleSessionsHidesFinishedSubagents(t *testing.T) {
	rows := []state.SessionView{
		makeSessionView("root", "", "idle", state.AttnInactive),
		makeSessionView("child-idle", "root", "idle", state.AttnInactive),
		makeSessionView("child-busy", "root", "busy", state.AttnActive),
	}

	visible := visibleSessions(rows)
	if len(visible) != 2 {
		t.Fatalf("visible count = %d, want 2", len(visible))
	}

	ids := map[string]state.SessionView{}
	for _, sv := range visible {
		ids[sv.SessionID] = sv
	}
	if _, ok := ids["child-idle"]; ok {
		t.Fatalf("idle subagent should be hidden")
	}
	if got := ids["child-busy"].ParentID; got != "root" {
		t.Fatalf("busy subagent parent = %q, want root", got)
	}
}

func TestVisibleSessionsKeepsUrgentSubagentsVisible(t *testing.T) {
	rows := []state.SessionView{
		makeSessionView("root", "", "idle", state.AttnInactive),
		makeSessionView("child-perm", "root", "idle", state.AttnPermissionPending),
		makeSessionView("child-question", "root", "idle", state.AttnQuestionPending),
		makeSessionView("child-err", "root", "", state.AttnErrored),
	}

	visible := visibleSessions(rows)
	ids := map[string]bool{}
	for _, sv := range visible {
		ids[sv.SessionID] = true
	}
	if !ids["child-perm"] || !ids["child-question"] || !ids["child-err"] {
		t.Fatalf("urgent subagents must stay visible: %+v", ids)
	}
}

func TestVisibleSessionsReparentsAcrossHiddenAncestor(t *testing.T) {
	rows := []state.SessionView{
		makeSessionView("root", "", "idle", state.AttnInactive),
		makeSessionView("mid-idle", "root", "idle", state.AttnInactive),
		makeSessionView("leaf-busy", "mid-idle", "busy", state.AttnActive),
	}

	visible := visibleSessions(rows)
	for _, sv := range visible {
		if sv.SessionID == "leaf-busy" {
			if sv.ParentID != "root" {
				t.Fatalf("leaf parent = %q, want root", sv.ParentID)
			}
			return
		}
	}
	t.Fatalf("leaf-busy row not visible")
}

func TestFormatRowAgentBeforeTitleAndDedupesAgentSuffix(t *testing.T) {
	sv := state.SessionView{
		SessionID:  "s1",
		Title:      "refactor parser (scribe)",
		Agent:      "scribe",
		StatusType: "busy",
		Attention:  state.AttnActive,
		Source:     state.SourceLive,
	}

	row := formatRow(sv, 120, false)
	if !strings.Contains(row, "@scribe refactor parser") {
		t.Fatalf("row = %q, want agent before title", row)
	}
	if strings.Contains(row, "(scribe)") {
		t.Fatalf("row = %q, want duplicate agent suffix removed", row)
	}
}

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

	rendered := m.renderAllSessions(120, rows)
	if strings.Contains(rendered, "Instance 1") || strings.Contains(rendered, "Instance 2") {
		t.Fatalf("rendered = %q, want instance labels removed", rendered)
	}
	if strings.Contains(rendered, "10.0.0.1:1234") || strings.Contains(rendered, "10.0.0.2:5678") {
		t.Fatalf("rendered = %q, want host:port redacted", rendered)
	}
}

func TestViewDoesNotRenderNeedsAttentionPane(t *testing.T) {
	m := model{
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
