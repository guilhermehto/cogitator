package agents

import (
	"reflect"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/mux"
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

func liveViews(dirs ...string) []state.SessionView {
	views := make([]state.SessionView, len(dirs))
	for i, dir := range dirs {
		views[i] = state.SessionView{SessionID: "s-" + dir, Directory: dir, Source: state.SourceLive, LastActivity: now}
	}

	return views
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

	live := liveViews("/w/idle", "/w/working", "/w/done", "/w/errored", "/w/perm-new", "/w/perm-old")
	got := ids(build(Input{Rows: rows, Live: live}))
	want := []string{
		"/w/perm-old", "/w/perm-new", "/w/errored",
		"/w/done", "/w/working", "/w/idle", "/w/unknown", "/w/stopped",
	}
	if !equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestBuildHoldsWorkingAgentPlaceUntilItStops(t *testing.T) {
	live := liveViews("/w/a", "/w/b")
	first := build(Input{Live: live, Rows: []settings.Row{
		row("/w/a", settings.StateRunning, state.AttnActive, 10*time.Minute),
		row("/w/b", settings.StateRunning, state.AttnActive, 5*time.Minute),
	}})

	busy := build(Input{Live: live, Previous: first, Rows: []settings.Row{
		row("/w/a", settings.StateRunning, state.AttnActive, 0),
		row("/w/b", settings.StateRunning, state.AttnActive, 5*time.Minute),
	}})
	if got, want := ids(busy), []string{"/w/a", "/w/b"}; !equal(got, want) {
		t.Fatalf("order after new activity = %v, want %v", got, want)
	}
	if !busy[0].OrderedAt.Equal(ago(10 * time.Minute)) {
		t.Fatalf("working agent OrderedAt = %v, want held %v", busy[0].OrderedAt, ago(10*time.Minute))
	}

	finished := build(Input{Live: live, Previous: busy, Rows: []settings.Row{
		row("/w/a", settings.StateRunning, state.AttnFinished, 0),
	}})
	if !finished[0].OrderedAt.Equal(now) {
		t.Fatalf("finished agent OrderedAt = %v, want its last activity %v", finished[0].OrderedAt, now)
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
			got := ids(build(Input{Rows: rows, Live: liveViews("/w/live")}))
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

func TestBuildLivenessOfRecentOnlySessions(t *testing.T) {
	recent := func(dir string) []state.SessionView {
		views := liveViews(dir)
		views[0].Source = state.SourceRecent

		return views
	}
	windowAt := func(dir string) []mux.Pane {
		return []mux.Pane{{PaneID: "%1", WindowID: "@1", SessionID: "$1", CogDir: dir}}
	}
	rowAt := func(attn state.Attention, age time.Duration) []settings.Row {
		return []settings.Row{row("/w/a", settings.StateRunning, attn, age)}
	}
	workspaceAt := func(attn state.Attention, age time.Duration) []workspace.WorkspaceStatus {
		return []workspace.WorkspaceStatus{{
			Workspace: workspace.Workspace{Name: "ws"},
			Sessions: []workspace.SessionStatus{{
				Session: workspace.Session{Name: "feat", Dir: "/w/a"},
				State:   settings.StateRunning, Attention: attn, LastActivity: ago(age),
			}},
		}}
	}

	tests := []struct {
		name  string
		in    Input
		want  []string
		group Group
	}{
		{"drops unseen finished row without a window past the window", Input{Rows: rowAt(state.AttnFinished, 30*time.Hour), Live: recent("/w/a")}, nil, 0},
		{"lists recently finished row without a window as stopped", Input{Rows: rowAt(state.AttnFinished, 10*time.Minute), Live: recent("/w/a")}, []string{"/w/a"}, GroupStopped},
		{"keeps unseen finished row with a tmux window done past the window", Input{Rows: rowAt(state.AttnFinished, 30*time.Hour), Live: recent("/w/a"), Panes: windowAt("/w/a")}, []string{"/w/a"}, GroupDone},
		{"keeps pending permission row needing you past the window", Input{Rows: rowAt(state.AttnPermissionPending, 2*time.Hour), Live: recent("/w/a")}, []string{"/w/a"}, GroupNeedsYou},
		{"drops unseen finished workspace session without a window past the window", Input{Workspaces: workspaceAt(state.AttnFinished, 2*time.Hour), Live: recent("/w/a")}, nil, 0},
		{"lists seen row with a tmux window as idle", Input{Rows: rowAt(state.AttnInactive, 2*time.Hour), Live: recent("/w/a"), Panes: windowAt("/w/a")}, []string{"/w/a"}, GroupIdle},
		{"lists busy row with a tmux window as working", Input{Rows: rowAt(state.AttnActive, 40*time.Minute), Live: recent("/w/a"), Panes: windowAt("/w/a")}, []string{"/w/a"}, GroupWorking},
		{"lists seen row without a window as stopped inside the window", Input{Rows: rowAt(state.AttnInactive, 10*time.Minute), Live: recent("/w/a")}, []string{"/w/a"}, GroupStopped},
		{"drops seen row without a window past the window", Input{Rows: rowAt(state.AttnInactive, 2*time.Hour), Live: recent("/w/a")}, nil, 0},
		{"drops stale busy row without a window past the window", Input{Rows: rowAt(state.AttnActive, 2*time.Hour), Live: recent("/w/a")}, nil, 0},
		{"drops seen workspace session without a window past the window", Input{Workspaces: workspaceAt(state.AttnInactive, 2*time.Hour), Live: recent("/w/a")}, nil, 0},
		{"lists seen row backed by a live view as idle", Input{Rows: rowAt(state.AttnInactive, 2*time.Hour), Live: liveViews("/w/a")}, []string{"/w/a"}, GroupIdle},
		{"lists busy row backed by a live view as working", Input{Rows: rowAt(state.AttnActive, time.Minute), Live: liveViews("/w/a")}, []string{"/w/a"}, GroupWorking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := build(tt.in)
			if !equal(ids(got), tt.want) {
				t.Fatalf("agents = %v, want %v", ids(got), tt.want)
			}
			if len(got) == 1 && got[0].Group != tt.group {
				t.Fatalf("group = %v, want %v", got[0].Group, tt.group)
			}
		})
	}
}

func TestBuildHidesDismissedAgentUntilItRunsAgain(t *testing.T) {
	dismissedAt := now.Add(-time.Minute)
	windowAt := []mux.Pane{{PaneID: "%1", WindowID: "@1", SessionID: "$1", CogDir: "/w/a"}}
	rowsActiveAgo := func(age time.Duration) []settings.Row {
		return []settings.Row{row("/w/a", settings.StateStopped, state.AttnFinished, age)}
	}
	dismissedA := map[string]time.Time{"/w/a": dismissedAt}

	tests := []struct {
		name string
		in   Input
		want []string
	}{
		{"hides dismissed agent", Input{Rows: rowsActiveAgo(5 * time.Minute), Dismissed: dismissedA}, nil},
		{"shows dismissed agent with new activity", Input{Rows: rowsActiveAgo(30 * time.Second), Dismissed: dismissedA}, []string{"/w/a"}},
		{"shows dismissed agent that has a tmux window again", Input{Rows: rowsActiveAgo(5 * time.Minute), Panes: windowAt, Dismissed: dismissedA}, []string{"/w/a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ids(build(tt.in)); !equal(got, tt.want) {
				t.Fatalf("agents = %v, want %v", got, tt.want)
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

	got := build(Input{Rows: rows, Workspaces: workspaces, Live: liveViews("/ws/feat")})
	if len(got) != 1 || got[0].ID != "/ws/feat" || got[0].Kind != KindWorkspace || got[0].Group != GroupNeedsYou {
		t.Fatalf("agents = %+v, want only the workspace session needing attention", got)
	}
	if got[0].Workspace != "ws" || got[0].SessionName != "feat" {
		t.Fatalf("workspace identity not carried: %+v", got[0])
	}
}

func TestBuildManagedAgentLocatesWindowByCogDir(t *testing.T) {
	rows := []settings.Row{row("/w/a", settings.StateRunning, state.AttnFinished, time.Minute)}
	panes := []mux.Pane{
		{PaneID: "%3", WindowID: "@3", SessionID: "$1", Cwd: "/elsewhere", CogDir: "/w/a"},
	}

	got := build(Input{Rows: rows, Panes: panes, Live: liveViews("/w/a")})
	if got[0].WindowID != "@3" || got[0].PaneID != "%3" || got[0].MuxSessionID != "$1" || !got[0].SessionOwned {
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
	pane := mux.Pane{PaneID: "%7", WindowID: "@7", SessionID: "$2", Cwd: "/stray"}

	tests := []struct {
		name  string
		live  []state.SessionView
		panes []mux.Pane
		root  string
		want  []string
	}{
		{"includes live session matching a pane cwd", []state.SessionView{live("/stray", state.SourceLive)}, []mux.Pane{pane}, "", []string{"/stray"}},
		{"drops stray without a matching pane", []state.SessionView{live("/stray", state.SourceLive)}, nil, "", nil},
		{"drops recent-history sessions", []state.SessionView{live("/stray", state.SourceRecent)}, []mux.Pane{pane}, "", nil},
		{"drops sessions under the workspace root", []state.SessionView{live("/stray", state.SourceLive)}, []mux.Pane{pane}, "/", nil},
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
	panes := []mux.Pane{{PaneID: "%1", WindowID: "@1", SessionID: "$1", Cwd: "/w/a"}}

	if got := build(Input{Rows: rows, Live: live, Panes: panes}); len(got) != 0 {
		t.Fatalf("managed dir leaked as stray: %+v", got)
	}
}

func TestBuildStrayPrefersPaneRunningTheHarness(t *testing.T) {
	live := []state.SessionView{{SessionID: "x", Directory: "/stray", Source: state.SourceLive, Provider: "opencode", LastActivity: ago(time.Minute)}}
	panes := []mux.Pane{
		{PaneID: "%1", WindowID: "@1", SessionID: "$1", Cwd: "/stray", Command: "zsh"},
		{PaneID: "%2", WindowID: "@2", SessionID: "$1", Cwd: "/stray", Command: "opencode"},
	}

	got := build(Input{Live: live, Panes: panes})
	if got[0].PaneID != "%2" {
		t.Fatalf("pane = %s, want %%2", got[0].PaneID)
	}
}

// The panes are zellijctl.ListPanes output for a zellij session with a shell
// and a claude pane in the stray dir; the claude-code harness launches "claude".
func TestBuildStrayPrefersClaudePaneOverShellInZellij(t *testing.T) {
	live := []state.SessionView{{SessionID: "x", Directory: "/stray", Source: state.SourceLive, Provider: "claude-code", LastActivity: ago(time.Minute)}}
	for _, command := range []string{"claude --resume abc", "sleep 100000 claude"} {
		panes := []mux.Pane{
			{PaneID: "cogtest-agent:terminal_0", WindowID: "cogtest-agent:0", SessionID: "cogtest-agent", Cwd: "/stray", Command: "/bin/zsh", CogDir: "/stray"},
			{PaneID: "cogtest-agent:terminal_1", WindowID: "cogtest-agent:0", SessionID: "cogtest-agent", Cwd: "/stray", Command: command, CogDir: "/stray"},
		}

		got := build(Input{Live: live, Panes: panes})
		if got[0].PaneID != "cogtest-agent:terminal_1" {
			t.Errorf("command %q: pane = %s, want cogtest-agent:terminal_1", command, got[0].PaneID)
		}
	}
}

func TestBuildStrayFallsBackToLowestPaneID(t *testing.T) {
	live := []state.SessionView{{SessionID: "x", Directory: "/stray", Source: state.SourceLive, LastActivity: ago(time.Minute)}}
	panes := []mux.Pane{
		{PaneID: "%10", WindowID: "@10", SessionID: "$1", Cwd: "/stray", Command: "zsh"},
		{PaneID: "%9", WindowID: "@9", SessionID: "$1", Cwd: "/stray", Command: "vim"},
	}

	got := build(Input{Live: live, Panes: panes})
	if got[0].PaneID != "%9" {
		t.Fatalf("pane = %s, want %%9", got[0].PaneID)
	}
}

// The zellij session "dev" has two omp tabs in the worktree /w/a.
func siblingPanes() []mux.Pane {
	return []mux.Pane{
		{PaneID: "dev:terminal_1", WindowID: "dev:0", SessionID: "dev", Cwd: "/w/a", Command: "omp", CogDir: "/w/a"},
		{PaneID: "dev:terminal_2", WindowID: "dev:1", SessionID: "dev", Cwd: "/w/a", Command: "omp", CogDir: "/w/a"},
	}
}

func siblingView(id string, created time.Duration, attn state.Attention, paneID string) state.SessionView {
	return state.SessionView{
		SessionID: id, Directory: "/w/a", Source: state.SourceLive, Provider: "omp",
		Attention: attn, Created: ago(created), LastActivity: ago(time.Minute), PaneID: paneID,
	}
}

func TestBuildSplitsSessionsSharingAManagedDirOntoTheirHookPanes(t *testing.T) {
	rows := []settings.Row{row("/w/a", settings.StateRunning, state.AttnActive, time.Minute)}
	live := []state.SessionView{
		siblingView("old", 2*time.Hour, state.AttnQuestionPending, "dev:terminal_2"),
		siblingView("new", time.Hour, state.AttnActive, "dev:terminal_1"),
	}

	got := build(Input{Rows: rows, Live: live, Panes: siblingPanes()})
	byID := map[string]Agent{}
	for _, a := range got {
		byID[a.ID] = a
	}
	old, okOld := byID["/w/a#old"]
	fresh, okNew := byID["/w/a#new"]
	if len(got) != 2 || !okOld || !okNew {
		t.Fatalf("agents = %v, want /w/a#old and /w/a#new", ids(got))
	}
	if old.WindowID != "dev:1" || old.Group != GroupNeedsYou || old.Kind != KindWorktree || old.Dir != "/w/a" {
		t.Errorf("old = %+v, want its question on tab dev:1", old)
	}
	if fresh.WindowID != "dev:0" || fresh.Group != GroupWorking || !fresh.Sibling() {
		t.Errorf("new = %+v, want working on tab dev:0", fresh)
	}
}

func TestBuildGivesUnreportedSiblingsDistinctPanes(t *testing.T) {
	live := []state.SessionView{
		siblingView("a", 2*time.Hour, state.AttnActive, ""),
		siblingView("b", time.Hour, state.AttnActive, ""),
	}

	got := build(Input{Live: live, Panes: siblingPanes()})
	if len(got) != 2 || got[0].PaneID == got[1].PaneID || got[0].Kind != KindStray {
		t.Fatalf("agents = %+v, want two strays on different panes", got)
	}
}

func TestBuildDropsAnUnreportedSiblingWhenNoPaneIsFree(t *testing.T) {
	live := []state.SessionView{
		siblingView("reported", 2*time.Hour, state.AttnActive, "dev:terminal_1"),
		siblingView("unreported", time.Hour, state.AttnActive, ""),
	}

	got := build(Input{Live: live, Panes: siblingPanes()[:1]})
	if !equal(ids(got), []string{"/w/a#reported"}) || got[0].WindowID != "dev:0" {
		t.Fatalf("agents = %+v, want only /w/a#reported on tab dev:0", got)
	}
}

// A tmux pane id such as "%4" is never in a zellij listing, and a closed pane
// drops out of any listing.
func TestBuildLeavesOutSessionsReportedOnUnlistedPanes(t *testing.T) {
	inTmux := siblingView("tmux", time.Hour, state.AttnActive, "%4")
	closed := siblingView("closed", time.Hour, state.AttnActive, "dev:terminal_9")
	here := siblingView("here", 2*time.Hour, state.AttnActive, "dev:terminal_1")
	managed := row("/w/a", settings.StateRunning, state.AttnActive, time.Minute)
	managed.SessionID = "tmux"

	cases := map[string]struct {
		rows []settings.Row
		live []state.SessionView
		want []string
	}{
		"stray in another multiplexer":              {nil, []state.SessionView{inTmux}, nil},
		"managed session in another multiplexer":    {[]settings.Row{managed}, []state.SessionView{inTmux}, nil},
		"session sharing its dir with a closed one": {nil, []state.SessionView{here, closed}, []string{"/w/a"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := build(Input{Rows: tc.rows, Live: tc.live, Panes: siblingPanes()})
			if !equal(ids(got), tc.want) {
				t.Fatalf("agents = %v, want %v", ids(got), tc.want)
			}
			if len(got) == 1 && got[0].WindowID != "dev:0" {
				t.Fatalf("agent = %+v, want it on its reported tab dev:0", got[0])
			}
		})
	}
}

func TestBuildDescribesTheManagedSessionRunningHere(t *testing.T) {
	r := row("/w/a", settings.StateRunning, state.AttnQuestionPending, time.Minute)
	r.SessionID = "tmux"
	here := siblingView("here", time.Hour, state.AttnActive, "dev:terminal_1")
	here.Title = "Fix login"
	live := []state.SessionView{siblingView("tmux", 2*time.Hour, state.AttnQuestionPending, "%4"), here}

	got := build(Input{Rows: []settings.Row{r}, Live: live, Panes: siblingPanes()})
	if len(got) != 1 || got[0].SessionID != "here" || got[0].Title != "Fix login" || got[0].Group != GroupWorking || got[0].WindowID != "dev:0" {
		t.Fatalf("agents = %+v, want the working session on tab dev:0", got)
	}
}

func TestBuildKeepsOneAgentPerDirForASingleSession(t *testing.T) {
	live := []state.SessionView{siblingView("only", time.Hour, state.AttnActive, "dev:terminal_2")}

	got := build(Input{Live: live, Panes: siblingPanes()})
	if !equal(ids(got), []string{"/w/a"}) || got[0].Sibling() {
		t.Fatalf("agents = %+v, want the directory agent /w/a", got)
	}
}

// omp started from a shell in ~ runs in /tmp; zellij reports the shell's cwd.
func TestBuildPlacesALoneSessionOnTheHookPaneWhateverItsShellCwd(t *testing.T) {
	panes := []mux.Pane{{PaneID: "bb:terminal_2", WindowID: "bb:1", SessionID: "bb", Cwd: "/home/me", Command: "omp"}}
	reported := state.SessionView{SessionID: "s", Directory: "/tmp", Source: state.SourceLive, Provider: "omp", LastActivity: ago(time.Minute), PaneID: "bb:terminal_2"}
	gone := reported
	gone.PaneID = "bb:terminal_9"

	got := build(Input{Live: []state.SessionView{reported}, Panes: panes})
	if len(got) != 1 || got[0].WindowID != "bb:1" || got[0].Kind != KindStray {
		t.Fatalf("agents = %+v, want the /tmp stray on tab bb:1", got)
	}

	if got := build(Input{Live: []state.SessionView{gone}, Panes: panes}); len(got) != 0 {
		t.Fatalf("agents = %+v, want none once the reported pane is gone and no pane sits in /tmp", got)
	}
}

func TestBuildLabelsSiblingsByTabNameThenTitleThenTabNumber(t *testing.T) {
	panes := []mux.Pane{
		{PaneID: "dev:terminal_1", WindowID: "dev:0", SessionID: "dev", Cwd: "/w/a", Command: "omp", TabName: "LEX-123", TabNumber: 1},
		{PaneID: "dev:terminal_2", WindowID: "dev:1", SessionID: "dev", Cwd: "/w/a", Command: "omp", TabNumber: 2},
		{PaneID: "dev:terminal_3", WindowID: "dev:2", SessionID: "dev", Cwd: "/w/a", Command: "omp", TabNumber: 3},
	}
	named := siblingView("named", 3*time.Hour, state.AttnActive, "dev:terminal_1")
	named.Title = "Fix login"
	titled := siblingView("titled", 2*time.Hour, state.AttnActive, "dev:terminal_2")
	titled.Title = "Tidy sidebar"
	fresh := siblingView("fresh", time.Hour, state.AttnActive, "dev:terminal_3")

	got := build(Input{Live: []state.SessionView{named, titled, fresh}, Panes: panes})
	tabs := map[string]string{}
	for _, a := range got {
		tabs[a.ID] = a.Tab
	}
	want := map[string]string{"/w/a#named": "LEX-123", "/w/a#titled": "Tidy sidebar", "/w/a#fresh": "tab 3"}
	if !reflect.DeepEqual(tabs, want) {
		t.Errorf("tabs = %v, want %v", tabs, want)
	}
}

func TestBuildShowsTabNamesOnlyWhenTheyAddSomething(t *testing.T) {
	cases := map[string]struct {
		tabName string
		want    string
	}{
		"name the user chose":              {"LEX-9", "LEX-9"},
		"name cogitator gave the tab":      {"repo/main", ""},
		"unnamed tab of a lone agent":      {"", ""},
		"tab named after the worktree dir": {"a", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := row("/w/a", settings.StateRunning, state.AttnActive, time.Minute)
			r.Branch = "main"
			panes := []mux.Pane{{PaneID: "%1", WindowID: "@1", SessionID: "$1", Cwd: "/w/a", Command: "opencode", CogDir: "/w/a", TabName: tc.tabName, TabNumber: 2}}

			got := build(Input{Rows: []settings.Row{r}, Panes: panes})
			if len(got) != 1 || got[0].Tab != tc.want {
				t.Fatalf("agents = %+v, want tab %q", got, tc.want)
			}
		})
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
	owned := []Agent{{ID: "A", WindowID: "@1", MuxSessionID: "$1", SessionOwned: true}}
	shared := []Agent{
		{ID: "A", WindowID: "@1", MuxSessionID: "$1"},
		{ID: "B", WindowID: "@2", MuxSessionID: "$1"},
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
	panes := []mux.Pane{
		{PaneID: "%1", WindowID: "@1", SessionID: "$1", CogDir: "/w/a"},
		{PaneID: "%2", WindowID: "@2", SessionID: "$1", CogDir: "/w/b"},
	}

	for _, a := range build(Input{Rows: rows, Panes: panes, Live: liveViews("/w/a", "/w/b")}) {
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
