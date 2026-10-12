package agents

import (
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

const window = 30 * time.Minute

func ago(d time.Duration) time.Time { return now.Add(-d) }

func row(dir string, st settings.RowState, attn state.Attention, age time.Duration) settings.Row {
	return settings.Row{
		Repo: "/repo", Worktree: dir, Harness: "opencode", SessionID: "s-" + dir,
		State: st, Attention: attn, LastActivity: ago(age),
	}
}

func build(in Input) []Agent {
	in.Now = now
	if in.RecentWindow == 0 {
		in.RecentWindow = window
	}
	return Build(in)
}

func ids(list []Agent) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildOrdersByUrgencyThenOldestFirst(t *testing.T) {
	rows := []settings.Row{
		row("/w/stopped", settings.StateStopped, "", 5*time.Minute),
		row("/w/idle", settings.StateRunning, state.AttnInactive, 5*time.Minute),
		row("/w/working", settings.StateRunning, state.AttnActive, 5*time.Minute),
		row("/w/done", settings.StateRunning, state.AttnFinished, 5*time.Minute),
		row("/w/errored", settings.StateRunning, state.AttnErrored, 20*time.Minute),
		row("/w/perm-new", settings.StateRunning, state.AttnPermissionPending, 1*time.Minute),
		row("/w/perm-old", settings.StateRunning, state.AttnPermissionPending, 10*time.Minute),
		row("/w/unknown", settings.StateUnknown, "", 1*time.Minute),
	}

	got := ids(build(Input{Rows: rows}))
	want := []string{
		"/w/perm-old", "/w/perm-new", "/w/errored",
		"/w/done", "/w/working", "/w/idle", "/w/unknown", "/w/stopped",
	}
	if !equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestBuildStoppedAgentsFollowRecentWindow(t *testing.T) {
	tests := []struct {
		name    string
		age     time.Duration
		present bool
	}{
		{"drops stopped agent past the window", 31 * time.Minute, false},
		{"keeps stopped agent inside the window", 29 * time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := []settings.Row{
				row("/w/live", settings.StateRunning, state.AttnActive, time.Minute),
				row("/w/stopped", settings.StateStopped, "", tt.age),
			}
			got := ids(build(Input{Rows: rows}))
			want := []string{"/w/live"}
			if tt.present {
				want = append(want, "/w/stopped")
			}
			if !equal(got, want) {
				t.Fatalf("agents = %v, want %v", got, want)
			}
		})
	}
}

func TestBuildSkipsPlaceholdersMissingAndPendingSessions(t *testing.T) {
	rows := []settings.Row{
		{Repo: "/repo", Worktree: "/repo", State: settings.StateStopped, IsRoot: true},
		row("/w/missing", settings.StateMissing, "", time.Minute),
	}
	workspaces := []workspace.WorkspaceStatus{{
		Workspace: workspace.Workspace{Name: "ws"},
		Sessions: []workspace.SessionStatus{
			{Session: workspace.Session{Name: "pending"}},
			{
				Session:   workspace.Session{Name: "feat", Dir: "/ws/feat", Branch: "feat", Harness: "opencode"},
				State:     settings.StateRunning,
				Attention: state.AttnQuestionPending, LastActivity: ago(time.Minute),
			},
		},
	}}

	got := build(Input{Rows: rows, Workspaces: workspaces})
	if len(got) != 1 || got[0].ID != "/ws/feat" || got[0].Kind != KindWorkspace || got[0].Group != GroupNeedsYou {
		t.Fatalf("agents = %+v, want only the workspace session needing attention", got)
	}
	if got[0].Workspace != "ws" || got[0].SessionName != "feat" {
		t.Fatalf("workspace identity not carried: %+v", got[0])
	}
}

func TestBuildManagedAgentLocatesWindowByCogDir(t *testing.T) {
	rows := []settings.Row{row("/w/a", settings.StateRunning, state.AttnFinished, time.Minute)}
	panes := []Pane{
		{PaneID: "%3", WindowID: "@3", SessionID: "$1", Cwd: "/elsewhere", CogDir: "/w/a"},
	}

	got := build(Input{Rows: rows, Panes: panes})
	if got[0].WindowID != "@3" || got[0].PaneID != "%3" || got[0].TmuxSessionID != "$1" || !got[0].SessionOwned {
		t.Fatalf("tmux location not joined: %+v", got[0])
	}
}

func TestBuildStrays(t *testing.T) {
	live := func(dir string, src state.Source) state.SessionView {
		return state.SessionView{
			SessionID: "x", Directory: dir, Source: src, Provider: "opencode",
			Attention: state.AttnPermissionPending, LastActivity: ago(time.Minute),
		}
	}
	pane := Pane{PaneID: "%7", WindowID: "@7", SessionID: "$2", Cwd: "/stray"}

	tests := []struct {
		name  string
		live  []state.SessionView
		panes []Pane
		root  string
		want  []string
	}{
		{"includes live session matching a pane cwd", []state.SessionView{live("/stray", state.SourceLive)}, []Pane{pane}, "", []string{"/stray"}},
		{"drops stray without a matching pane", []state.SessionView{live("/stray", state.SourceLive)}, nil, "", nil},
		{"drops recent-history sessions", []state.SessionView{live("/stray", state.SourceRecent)}, []Pane{pane}, "", nil},
		{"drops sessions under the workspace root", []state.SessionView{live("/stray", state.SourceLive)}, []Pane{pane}, "/", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := build(Input{Live: tt.live, Panes: tt.panes, WorkspaceRoot: tt.root})
			if !equal(ids(got), tt.want) {
				t.Fatalf("agents = %v, want %v", ids(got), tt.want)
			}
			if len(got) == 1 && (got[0].PaneID != "%7" || got[0].WindowID != "@7" || got[0].Kind != KindStray) {
				t.Fatalf("stray not joined to pane: %+v", got[0])
			}
		})
	}
}

func TestBuildStrayExcludesManagedDirs(t *testing.T) {
	rows := []settings.Row{row("/w/a", settings.StateStopped, "", 90*time.Minute)}
	live := []state.SessionView{{SessionID: "x", Directory: "/w/a", Source: state.SourceLive, LastActivity: ago(time.Minute)}}
	panes := []Pane{{PaneID: "%1", WindowID: "@1", SessionID: "$1", Cwd: "/w/a"}}

	if got := build(Input{Rows: rows, Live: live, Panes: panes}); len(got) != 0 {
		t.Fatalf("managed dir leaked as stray: %+v", got)
	}
}

func TestBuildStrayPrefersPaneRunningTheHarness(t *testing.T) {
	live := []state.SessionView{{SessionID: "x", Directory: "/stray", Source: state.SourceLive, Provider: "opencode", LastActivity: ago(time.Minute)}}
	panes := []Pane{
		{PaneID: "%1", WindowID: "@1", SessionID: "$1", Cwd: "/stray", Command: "zsh"},
		{PaneID: "%2", WindowID: "@2", SessionID: "$1", Cwd: "/stray", Command: "opencode"},
	}

	got := build(Input{Live: live, Panes: panes})
	if got[0].PaneID != "%2" {
		t.Fatalf("pane = %s, want %%2", got[0].PaneID)
	}
}

func TestBuildStrayFallsBackToLowestPaneID(t *testing.T) {
	live := []state.SessionView{{SessionID: "x", Directory: "/stray", Source: state.SourceLive, LastActivity: ago(time.Minute)}}
	panes := []Pane{
		{PaneID: "%10", WindowID: "@10", SessionID: "$1", Cwd: "/stray", Command: "zsh"},
		{PaneID: "%9", WindowID: "@9", SessionID: "$1", Cwd: "/stray", Command: "vim"},
	}

	got := build(Input{Live: live, Panes: panes})
	if got[0].PaneID != "%9" {
		t.Fatalf("pane = %s, want %%9", got[0].PaneID)
	}
}

func TestNext(t *testing.T) {
	list := []Agent{
		{ID: "A", Group: GroupNeedsYou},
		{ID: "B", Group: GroupNeedsYou},
		{ID: "C", Group: GroupWorking},
	}
	tests := []struct {
		name    string
		list    []Agent
		current string
		want    string
	}{
		{"advances to the next actionable agent", list, "A", "B"},
		{"wraps past non-actionable agents", list, "B", "A"},
		{"starts at the first actionable when nothing is current", list, "", "A"},
		{"starts at the first actionable when current is not actionable", list, "C", "A"},
		{"starts at the first actionable when current is unknown", list, "gone", "A"},
		{"returns done agents", []Agent{{ID: "D", Group: GroupDone}}, "", "D"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Next(tt.list, tt.current)
			if !ok || got.ID != tt.want {
				t.Fatalf("Next = %q, %v; want %q", got.ID, ok, tt.want)
			}
		})
	}
}

func TestNextReturnsFalseWithoutOtherActionableAgents(t *testing.T) {
	tests := []struct {
		name    string
		list    []Agent
		current string
	}{
		{"only working agents", []Agent{{ID: "W", Group: GroupWorking}, {ID: "I", Group: GroupIdle}, {ID: "S", Group: GroupStopped}}, ""},
		{"current is the only actionable agent", []Agent{{ID: "A", Group: GroupNeedsYou}}, "A"},
		{"empty list", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := Next(tt.list, tt.current); ok {
				t.Fatalf("Next = %+v, want none", got)
			}
		})
	}
}

func TestAgentAt(t *testing.T) {
	owned := []Agent{{ID: "A", WindowID: "@1", TmuxSessionID: "$1", SessionOwned: true}}
	shared := []Agent{
		{ID: "A", WindowID: "@1", TmuxSessionID: "$1"},
		{ID: "B", WindowID: "@2", TmuxSessionID: "$1"},
	}
	tests := []struct {
		name      string
		list      []Agent
		window    string
		session   string
		wantID    string
		wantFound bool
	}{
		{"matches the agent's own window", owned, "@1", "$1", "A", true},
		{"matches a sibling window of a session-owned agent", owned, "@9", "$1", "A", true},
		{"ignores sibling windows when the session holds two agents", shared, "@9", "$1", "", false},
		{"ignores other sessions", owned, "@9", "$2", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := AgentAt(tt.list, tt.window, tt.session)
			if id != tt.wantID || ok != tt.wantFound {
				t.Fatalf("AgentAt = %q, %v; want %q, %v", id, ok, tt.wantID, tt.wantFound)
			}
		})
	}
}

func TestBuildSessionOwnedFalseWhenSessionHoldsTwoTaggedWindows(t *testing.T) {
	rows := []settings.Row{
		row("/w/a", settings.StateRunning, state.AttnFinished, time.Minute),
		row("/w/b", settings.StateRunning, state.AttnFinished, 2*time.Minute),
	}
	panes := []Pane{
		{PaneID: "%1", WindowID: "@1", SessionID: "$1", CogDir: "/w/a"},
		{PaneID: "%2", WindowID: "@2", SessionID: "$1", CogDir: "/w/b"},
	}

	for _, a := range build(Input{Rows: rows, Panes: panes}) {
		if a.SessionOwned {
			t.Fatalf("%s reported SessionOwned in a shared session", a.ID)
		}
	}
}

func TestCounts(t *testing.T) {
	list := []Agent{
		{Group: GroupNeedsYou, Attention: state.AttnPermissionPending},
		{Group: GroupNeedsYou, Attention: state.AttnPermissionPending},
		{Group: GroupNeedsYou, Attention: state.AttnErrored},
		{Group: GroupDone, Attention: state.AttnFinished},
		{Group: GroupWorking, Attention: state.AttnActive},
	}

	c := Counts(list)
	if c.ByGroup[GroupNeedsYou] != 3 || c.ByGroup[GroupDone] != 1 || c.ByGroup[GroupWorking] != 1 {
		t.Fatalf("group counts = %v", c.ByGroup)
	}
	if c.NeedsYou[state.AttnPermissionPending] != 2 || c.NeedsYou[state.AttnErrored] != 1 || c.NeedsYou[state.AttnFinished] != 0 {
		t.Fatalf("needs-you counts = %v", c.NeedsYou)
	}
}
