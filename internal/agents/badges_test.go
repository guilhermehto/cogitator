package agents

import (
	"reflect"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

func agentOn(id, windowID string, group Group, attn state.Attention, age int) Agent {
	return Agent{
		ID: id, WindowID: windowID, TmuxSessionID: "$1", Group: group,
		Attention: attn, LastActivity: now.Add(-durationSecs(age)),
	}
}

func durationSecs(n int) time.Duration { return time.Duration(n) * time.Second }

func winSet(target, name, value string) tmuxctl.OptionWrite {
	return tmuxctl.OptionWrite{Target: target, Name: name, Value: value}
}

func winUnset(target, name string) tmuxctl.OptionWrite {
	return tmuxctl.OptionWrite{Target: target, Name: name, Unset: true}
}

func globalSet(name, value string) tmuxctl.OptionWrite {
	return tmuxctl.OptionWrite{Global: true, Name: name, Value: value}
}

func globalUnset(name string) tmuxctl.OptionWrite {
	return tmuxctl.OptionWrite{Global: true, Name: name, Unset: true}
}

func TestDiffOptionsIsEmptyWhenTmuxHoldsDesiredState(t *testing.T) {
	a := agentOn("/a", "@1", GroupNeedsYou, state.AttnPermissionPending, 10)
	since := "1767268790"
	panes := []Pane{{WindowID: "@1", CogStatus: "needs-you", CogReason: "permission", CogSince: since}}
	globals := map[string]string{OptNeeds: "1"}

	if got := DiffOptions([]Agent{a}, panes, globals); len(got) != 0 {
		t.Fatalf("want no writes, got %v", got)
	}
}

func TestDiffOptionsMovesPermissionToWorking(t *testing.T) {
	a := agentOn("/a", "@1", GroupWorking, state.AttnActive, 5)
	panes := []Pane{{WindowID: "@1", CogStatus: "needs-you", CogReason: "permission", CogSince: "100"}}
	globals := map[string]string{OptNeeds: "1"}

	got := DiffOptions([]Agent{a}, panes, globals)

	want := []tmuxctl.OptionWrite{
		winSet("@1", OptStatus, "working"),
		winUnset("@1", OptReason),
		winSet("@1", OptSince, "1767268795"),
		globalUnset(OptNeeds),
		globalSet(OptWorking, "1"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestDiffOptionsIgnoresActivityWithoutStatusChange(t *testing.T) {
	a := agentOn("/a", "@1", GroupWorking, state.AttnActive, 1)
	panes := []Pane{{WindowID: "@1", CogStatus: "working", CogSince: "100"}}
	globals := map[string]string{OptWorking: "1"}

	if got := DiffOptions([]Agent{a}, panes, globals); len(got) != 0 {
		t.Fatalf("want no writes, got %v", got)
	}
}

func TestDiffOptionsUnsetsOrphanedWindowOptions(t *testing.T) {
	panes := []Pane{{WindowID: "@2", CogStatus: "done", CogReason: "finished", CogSince: "100"}}

	got := DiffOptions(nil, panes, nil)

	want := []tmuxctl.OptionWrite{
		winUnset("@2", OptStatus), winUnset("@2", OptReason), winUnset("@2", OptSince),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestDiffOptionsSkipsWindowsMissingFromPanes(t *testing.T) {
	a := agentOn("/a", "@9", GroupNeedsYou, state.AttnQuestionPending, 5)

	if got := DiffOptions([]Agent{a}, []Pane{{WindowID: "@1"}}, nil); len(got) != 0 {
		t.Fatalf("want no writes, got %v", got)
	}
}

func TestDiffOptionsShowsNeedsYouOverStrayInSharedWindow(t *testing.T) {
	stray := agentOn("/a-stray", "@1", GroupWorking, state.AttnActive, 5)
	needs := agentOn("/z-managed", "@1", GroupNeedsYou, state.AttnErrored, 5)

	got := DiffOptions([]Agent{stray, needs}, []Pane{{WindowID: "@1"}}, nil)

	if len(got) == 0 || got[0] != winSet("@1", OptStatus, "needs-you") {
		t.Fatalf("want needs-you first, got %v", got)
	}
}

func TestDiffOptionsUnsetsStoppedAgentWindow(t *testing.T) {
	a := agentOn("/a", "@1", GroupStopped, state.AttnInactive, 5)
	panes := []Pane{{WindowID: "@1", CogStatus: "idle", CogSince: "100"}}

	got := DiffOptions([]Agent{a}, panes, nil)

	want := []tmuxctl.OptionWrite{winUnset("@1", OptStatus), winUnset("@1", OptSince)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestClearAllUnsetsPresentWindowOptionsAndGlobals(t *testing.T) {
	panes := []Pane{
		{WindowID: "@1", CogStatus: "done", CogReason: "finished", CogSince: "1"},
		{WindowID: "@2"},
	}

	got := ClearAll(panes, map[string]string{OptDone: "1"})

	want := []tmuxctl.OptionWrite{
		winUnset("@1", OptStatus), winUnset("@1", OptReason), winUnset("@1", OptSince),
		globalUnset(OptNeeds), globalUnset(OptDone), globalUnset(OptWorking),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestSeenOnFocus(t *testing.T) {
	done := agentOn("/done", "@1", GroupDone, state.AttnFinished, 60)
	needs := agentOn("/needs", "@2", GroupNeedsYou, state.AttnPermissionPending, 60)
	list := []Agent{needs, done}

	cases := []struct {
		name   string
		client tmuxctl.ClientFocus
		want   []string
	}{
		{"returns done agent on focused window after activity",
			tmuxctl.ClientFocus{SessionID: "$1", WindowID: "@1", ActivityUnixSecs: now.Unix()}, []string{"/done"}},
		{"ignores client idle since before the agent finished",
			tmuxctl.ClientFocus{SessionID: "$1", WindowID: "@1", ActivityUnixSecs: now.Unix() - 120}, nil},
		{"never returns needs-you agents",
			tmuxctl.ClientFocus{SessionID: "$1", WindowID: "@2", ActivityUnixSecs: now.Unix()}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(SeenOnFocus(list, []tmuxctl.ClientFocus{tc.client}))
			if !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSeenOnFocusHonoursSessionOwnership(t *testing.T) {
	owned := agentOn("/owned", "@1", GroupDone, state.AttnFinished, 60)
	owned.SessionOwned = true
	client := tmuxctl.ClientFocus{SessionID: "$1", WindowID: "@7", ActivityUnixSecs: now.Unix()}

	got := ids(SeenOnFocus([]Agent{owned}, []tmuxctl.ClientFocus{client}))

	if !reflect.DeepEqual(got, []string{"/owned"}) {
		t.Fatalf("got %v", got)
	}
}
