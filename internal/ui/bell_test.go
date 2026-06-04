package ui

import (
	"testing"

	"github.com/guilhermehto/cogitator/internal/state"
)

func TestProcessBellTransitionsFiresOnceWhileAttentionStable(t *testing.T) {
	bellSent := map[rowKey]state.Attention{}
	row := state.SessionView{
		InstanceID: "inst-1",
		SessionID:  "s1",
		Attention:  state.AttnPermissionPending,
	}

	first := processBellTransitions([]state.SessionView{row}, bellSent)
	if len(first) != 1 {
		t.Fatalf("first snapshot fired %d bells, want 1", len(first))
	}
	second := processBellTransitions([]state.SessionView{row}, bellSent)
	if len(second) != 0 {
		t.Fatalf("second snapshot in same attention fired %d bells, want 0", len(second))
	}
	third := processBellTransitions([]state.SessionView{row}, bellSent)
	if len(third) != 0 {
		t.Fatalf("third snapshot in same attention fired %d bells, want 0", len(third))
	}
}

func TestProcessBellTransitionsFiresAgainAfterLeavingAttention(t *testing.T) {
	bellSent := map[rowKey]state.Attention{}
	pending := state.SessionView{
		InstanceID: "inst-1",
		SessionID:  "s1",
		Attention:  state.AttnPermissionPending,
	}
	calm := pending
	calm.Attention = state.AttnInactive

	if got := processBellTransitions([]state.SessionView{pending}, bellSent); len(got) != 1 {
		t.Fatalf("entry into attention fired %d bells, want 1", len(got))
	}
	if got := processBellTransitions([]state.SessionView{calm}, bellSent); len(got) != 0 {
		t.Fatalf("leaving attention fired %d bells, want 0", len(got))
	}
	if _, ok := bellSent[rowKey{instanceID: "inst-1", sessionID: "s1"}]; ok {
		t.Fatalf("bellSent must clear once a session leaves attention")
	}
	if got := processBellTransitions([]state.SessionView{pending}, bellSent); len(got) != 1 {
		t.Fatalf("re-entry into attention fired %d bells, want 1", len(got))
	}
}

func TestProcessBellTransitionsFiresOnFinished(t *testing.T) {
	bellSent := map[rowKey]state.Attention{}
	row := state.SessionView{
		InstanceID: "inst-1",
		SessionID:  "s1",
		Attention:  state.AttnFinished,
	}
	if got := processBellTransitions([]state.SessionView{row}, bellSent); len(got) != 1 {
		t.Fatalf("finished fired %d bells, want 1", len(got))
	}
	if got := processBellTransitions([]state.SessionView{row}, bellSent); len(got) != 0 {
		t.Fatalf("stable finished fired %d bells, want 0", len(got))
	}
}

func TestProcessBellTransitionsFiresOnAttentionTypeChange(t *testing.T) {
	bellSent := map[rowKey]state.Attention{}
	perm := state.SessionView{
		InstanceID: "inst-1",
		SessionID:  "s1",
		Attention:  state.AttnPermissionPending,
	}
	errored := perm
	errored.Attention = state.AttnErrored

	if got := processBellTransitions([]state.SessionView{perm}, bellSent); len(got) != 1 {
		t.Fatalf("first attention fired %d bells, want 1", len(got))
	}
	if got := processBellTransitions([]state.SessionView{errored}, bellSent); len(got) != 1 {
		t.Fatalf("attention type change fired %d bells, want 1", len(got))
	}
}

func TestProcessBellTransitionsPrunesDisappearedSessions(t *testing.T) {
	bellSent := map[rowKey]state.Attention{}
	row := state.SessionView{
		InstanceID: "inst-1",
		SessionID:  "s1",
		Attention:  state.AttnPermissionPending,
	}
	processBellTransitions([]state.SessionView{row}, bellSent)
	if len(bellSent) != 1 {
		t.Fatalf("bellSent len = %d, want 1", len(bellSent))
	}
	processBellTransitions(nil, bellSent)
	if len(bellSent) != 0 {
		t.Fatalf("disappeared session should be pruned, bellSent = %+v", bellSent)
	}
}
