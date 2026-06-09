package ui

import (
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

func TestRosterToRestored_EmptyRosterYieldsNil(t *testing.T) {
	got := rosterToRestored(nil)
	if got != nil {
		t.Fatalf("nil roster: expected nil, got %v", got)
	}
	got = rosterToRestored(map[string]workspace.RosterEntry{})
	if got != nil {
		t.Fatalf("empty roster: expected nil, got %v", got)
	}
}

func TestRosterToRestored_DropsEmptySessionID(t *testing.T) {
	roster := map[string]workspace.RosterEntry{
		"/repo/a": {Dir: "/repo/a", Harness: "opencode", SessionID: "", Attention: "finished", LastActivity: time.Now()},
	}
	got := rosterToRestored(roster)
	if len(got) != 0 {
		t.Fatalf("empty SessionID: expected 0 results, got %d", len(got))
	}
}

func TestRosterToRestored_DropsNonStickyAttention(t *testing.T) {
	now := time.Now()
	roster := map[string]workspace.RosterEntry{
		"/repo/active":   {Dir: "/repo/active", Harness: "opencode", SessionID: "s-active", Attention: "active", LastActivity: now},
		"/repo/inactive": {Dir: "/repo/inactive", Harness: "opencode", SessionID: "s-inactive", Attention: "inactive", LastActivity: now},
		"/repo/empty":    {Dir: "/repo/empty", Harness: "opencode", SessionID: "s-empty", Attention: "", LastActivity: now},
	}
	got := rosterToRestored(roster)
	if len(got) != 0 {
		t.Fatalf("non-sticky attention: expected 0 results, got %d: %v", len(got), got)
	}
}

func TestRosterToRestored_KeepsStickyAttention(t *testing.T) {
	now := time.Now()
	roster := map[string]workspace.RosterEntry{
		"/repo/fin":  {Dir: "/repo/fin", Harness: "opencode", SessionID: "s-fin", Attention: "finished", LastActivity: now},
		"/repo/err":  {Dir: "/repo/err", Harness: "opencode", SessionID: "s-err", Attention: "errored", LastActivity: now},
		"/repo/perm": {Dir: "/repo/perm", Harness: "opencode", SessionID: "s-perm", Attention: "permission", LastActivity: now},
		"/repo/q":    {Dir: "/repo/q", Harness: "opencode", SessionID: "s-q", Attention: "question", LastActivity: now},
	}
	got := rosterToRestored(roster)
	if len(got) != 4 {
		t.Fatalf("sticky attention: expected 4 results, got %d: %v", len(got), got)
	}
	byID := make(map[string]state.RestoredSession, len(got))
	for _, r := range got {
		byID[r.SessionID] = r
	}
	for _, want := range []struct {
		id   string
		attn state.Attention
	}{
		{"s-fin", state.AttnFinished},
		{"s-err", state.AttnErrored},
		{"s-perm", state.AttnPermissionPending},
		{"s-q", state.AttnQuestionPending},
	} {
		r, ok := byID[want.id]
		if !ok {
			t.Errorf("missing session %q in output", want.id)
			continue
		}
		if r.Attention != want.attn {
			t.Errorf("session %q: attention = %q, want %q", want.id, r.Attention, want.attn)
		}
	}
}

func TestRosterToRestored_HarnessToProviderMapping(t *testing.T) {
	now := time.Now()
	cases := []struct {
		harness      string
		wantProvider harness.Kind
	}{
		{"opencode", harness.Kind("opencode")},
		{"codex", harness.Kind("codex")},
		{"", harness.Kind("opencode")}, // empty defaults to opencode
	}
	for _, tc := range cases {
		roster := map[string]workspace.RosterEntry{
			"/repo/x": {Dir: "/repo/x", Harness: tc.harness, SessionID: "s1", Attention: "finished", LastActivity: now},
		}
		got := rosterToRestored(roster)
		if len(got) != 1 {
			t.Errorf("harness=%q: expected 1 result, got %d", tc.harness, len(got))
			continue
		}
		if got[0].Provider != tc.wantProvider {
			t.Errorf("harness=%q: provider = %q, want %q", tc.harness, got[0].Provider, tc.wantProvider)
		}
	}
}
