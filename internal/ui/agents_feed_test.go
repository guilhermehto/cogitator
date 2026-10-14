package ui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
)

type recordingViewMarker struct {
	viewed []string
}

func (r *recordingViewMarker) MarkViewed(_ harness.Kind, _, sessionID string) {
	r.viewed = append(r.viewed, sessionID)
}

func feedTestModel(tmux *fakeMux, rows []settings.Row) model {
	m := makeTestModel(tmux, &fakeGitOps{}, &fakeHarnessOps{}, rows)
	m.cfg = config.Default()
	m.feed = &agentfeed.Holder{}

	return m
}

func worktreeRow(dir string, rowState settings.RowState, attn state.Attention) settings.Row {
	return settings.Row{
		Repo:         "/r/repo",
		Worktree:     dir,
		Branch:       "feat",
		Harness:      "opencode",
		Provider:     "opencode",
		SessionID:    "ses_1",
		State:        rowState,
		Attention:    attn,
		LastActivity: time.Now(),
	}
}

// liveViewsForRunningRows backs each running row with a live session view;
// agents only count as in flight when a live view exists for their dir.
func liveViewsForRunningRows(rows []settings.Row) []state.SessionView {
	var views []state.SessionView
	for _, row := range rows {
		if row.State != settings.StateRunning {
			continue
		}
		views = append(views, state.SessionView{
			SessionID: row.SessionID, Directory: row.Worktree, Source: state.SourceLive,
			Provider: harness.Kind(row.Provider), Attention: row.Attention, LastActivity: row.LastActivity,
		})
	}

	return views
}

// rebuildAgents feeds rows through the model; like a store snapshot, a row's
// live view replaces the earlier view of the same session.
func rebuildAgents(t *testing.T, m model, rows []settings.Row) model {
	t.Helper()

	fresh := liveViewsForRunningRows(rows)
	sessions := slices.DeleteFunc(slices.Clone(m.snap.Sessions), func(sv state.SessionView) bool {
		return slices.ContainsFunc(fresh, func(f state.SessionView) bool { return f.SessionID == sv.SessionID })
	})
	m.snap.Sessions = append(sessions, fresh...)
	updated, _ := m.Update(workspaceRowsMsg{rows: rows})

	return updated.(model)
}

func TestFeedGroupsFinishedWorktreeAgentAsDone(t *testing.T) {
	rows := []settings.Row{worktreeRow(t.TempDir(), settings.StateRunning, state.AttnFinished)}
	m := rebuildAgents(t, feedTestModel(&fakeMux{available: true}, nil), rows)

	if len(m.agents) != 1 || m.agents[0].Group != agents.GroupDone {
		t.Fatalf("agents = %+v, want one agent in Done", m.agents)
	}
}

// zellij has no badges, so nothing else re-reads its panes between session
// events.
func TestFocusTickPicksUpRenamedTabWithoutBadges(t *testing.T) {
	dir := t.TempDir()
	rows := []settings.Row{worktreeRow(dir, settings.StateRunning, state.AttnActive)}
	tabName := ""
	m := feedTestModel(&fakeMux{available: true}, nil)
	m.listPanes = func() ([]mux.Pane, error) {
		return []mux.Pane{{PaneID: "s:terminal_1", WindowID: "s:1", SessionID: "s", Cwd: dir, Command: "opencode", CogDir: dir, TabName: tabName}}, nil
	}
	m = rebuildAgents(t, m, rows)

	tabName = "LEX-456"
	updated, cmd := m.Update(focusTickMsg{})
	m = updated.(model)
	for _, msg := range runBatchWithTimeout(cmd) {
		if panes, ok := msg.(panesMsg); ok {
			updated, _ = m.Update(panes)
			m = updated.(model)
		}
	}

	if len(m.agents) != 1 || m.agents[0].Tab != "LEX-456" {
		t.Fatalf("agents = %+v, want the renamed tab LEX-456", m.agents)
	}
}

func TestFeedFocusSelectsRunningAgentWindowForClientAndClearsBadge(t *testing.T) {
	dir := t.TempDir()
	rows := []settings.Row{worktreeRow(dir, settings.StateRunning, state.AttnFinished)}
	tmux := &fakeMux{available: true, findWindowResult: "s:1", processAlive: true}
	marker := &recordingViewMarker{}
	m := feedTestModel(tmux, nil)
	m.viewMarker = marker
	m = rebuildAgents(t, m, rows)

	cmd := m.handleFeedAction(agentfeed.Action{Kind: agentfeed.ActionFocus, AgentID: m.agents[0].ID, Client: "/dev/ttys009"})
	updated, _ := m.Update(cmd())
	m = updated.(model)

	want := clientSelectCall{target: "s:1", client: "/dev/ttys009"}
	if len(tmux.clientSelectCalls) != 1 || tmux.clientSelectCalls[0] != want {
		t.Fatalf("client selects = %+v, want [%+v]", tmux.clientSelectCalls, want)
	}
	if len(marker.viewed) != 1 || marker.viewed[0] != "ses_1" {
		t.Fatalf("viewed = %v, want [ses_1]", marker.viewed)
	}
	if !m.hasSwitchRecord(dir) {
		t.Fatal("focus not recorded for MRU ordering")
	}
}

func TestFeedFocusResumesStoppedAgentInSessionModeForClient(t *testing.T) {
	rows := []settings.Row{worktreeRow(t.TempDir(), settings.StateStopped, state.AttnInactive)}
	tmux := &fakeMux{available: true, findWindowResult: "s:1", processAlive: false}
	m := feedTestModel(tmux, nil)
	m = rebuildAgents(t, m, rows)
	m.launchMode = mux.ModeSession

	cmd := m.handleFeedAction(agentfeed.Action{Kind: agentfeed.ActionFocus, AgentID: m.agents[0].ID, Client: "/dev/ttys009"})
	cmd()

	if len(tmux.relaunchCalls) != 1 {
		t.Fatalf("relaunch calls = %d, want 1", len(tmux.relaunchCalls))
	}
	want := clientSelectCall{target: "s:1", session: true, client: "/dev/ttys009"}
	if len(tmux.clientSelectCalls) != 1 || tmux.clientSelectCalls[0] != want {
		t.Fatalf("client selects = %+v, want [%+v]", tmux.clientSelectCalls, want)
	}
}

func TestFeedFocusSelectsStrayPaneForClient(t *testing.T) {
	dir := t.TempDir()
	tmux := &fakeMux{available: true}
	m := feedTestModel(tmux, nil)
	m.snap = state.Snapshot{Sessions: []state.SessionView{{
		Source: state.SourceLive, Directory: dir, SessionID: "ses_stray", LastActivity: time.Now(),
	}}}
	m.panes = []mux.Pane{{PaneID: "%7", WindowID: "@1", SessionID: "$1", Cwd: dir}}
	m.refreshAgents()

	if len(m.agents) != 1 || m.agents[0].Kind != agents.KindStray {
		t.Fatalf("agents = %+v, want one stray", m.agents)
	}

	cmd := m.handleFeedAction(agentfeed.Action{Kind: agentfeed.ActionFocus, AgentID: m.agents[0].ID, Client: "/dev/ttys009"})
	cmd()

	want := clientSelectCall{paneID: "%7", client: "/dev/ttys009"}
	if len(tmux.clientSelectCalls) != 1 || tmux.clientSelectCalls[0] != want {
		t.Fatalf("client selects = %+v, want [%+v]", tmux.clientSelectCalls, want)
	}
}

func TestFeedSeenMarksAgentViewed(t *testing.T) {
	rows := []settings.Row{worktreeRow(t.TempDir(), settings.StateRunning, state.AttnFinished)}
	marker := &recordingViewMarker{}
	m := feedTestModel(&fakeMux{available: true}, nil)
	m.viewMarker = marker
	m = rebuildAgents(t, m, rows)

	m.handleFeedAction(agentfeed.Action{Kind: agentfeed.ActionSeen, AgentID: m.agents[0].ID})

	if len(marker.viewed) != 1 || marker.viewed[0] != "ses_1" {
		t.Fatalf("viewed = %v, want [ses_1]", marker.viewed)
	}
}

func TestFeedJumpFailureIsShownOnRequestingClient(t *testing.T) {
	tmux := &fakeMux{available: true}
	m := feedTestModel(tmux, nil)

	updated, cmd := m.Update(launchResultMsg{dir: "/r/a", client: "/dev/ttys009", err: errors.New("boom")})
	m = updated.(model)
	cmd()

	if len(tmux.displayMessageCalls) != 1 || tmux.displayMessageCalls[0].client != "/dev/ttys009" {
		t.Fatalf("display messages = %+v, want one for /dev/ttys009", tmux.displayMessageCalls)
	}
	if m.tmuxHint == "" {
		t.Fatal("tmuxHint not set")
	}
}

func TestFeedBindFailureIsNonFatal(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	missingDir := filepath.Join(t.TempDir(), "missing", "feed.sock")

	holder, actions, stop := startAgentFeed(context.Background(), missingDir, logger)
	defer stop()

	if holder != nil || actions != nil {
		t.Fatalf("holder = %v, actions = %v, want nil on bind failure", holder, actions)
	}
}
