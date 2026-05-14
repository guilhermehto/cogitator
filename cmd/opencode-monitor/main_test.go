package main

import (
	"testing"

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
		makeSessionView("child-err", "root", "", state.AttnErrored),
	}

	visible := visibleSessions(rows)
	ids := map[string]bool{}
	for _, sv := range visible {
		ids[sv.SessionID] = true
	}
	if !ids["child-perm"] || !ids["child-err"] {
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
