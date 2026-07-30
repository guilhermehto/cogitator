package ui

// workspace_view_test.go — tests for the Workspaces view introduced in step 10
// of add-multi-repo-workspaces-with-per-session-worktree-bundles: the Tab
// swap, listing/rendering, scrolling, empty states, the demo/--status store
// gate, and the loadWorkspaceStatusCmd coalescing state machine.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeSessionStatus builds a workspace.SessionStatus for testing.
func makeSessionStatus(name, branch string, st settings.RowState, memberRepos ...string) workspace.SessionStatus {
	members := make([]workspace.SessionMember, 0, len(memberRepos))
	for _, r := range memberRepos {
		members = append(members, workspace.SessionMember{RepoPath: r, WorktreePath: r + "/" + name})
	}
	return workspace.SessionStatus{
		Session: workspace.Session{Name: name, Branch: branch, Members: members},
		State:   st,
	}
}

// makeWsStatus builds a workspace.WorkspaceStatus for testing.
func makeWsStatus(name string, sessions ...workspace.SessionStatus) workspace.WorkspaceStatus {
	ws := workspace.Workspace{Name: name}
	for _, s := range sessions {
		ws.Sessions = append(ws.Sessions, s.Session)
	}
	return workspace.WorkspaceStatus{Workspace: ws, Sessions: sessions}
}

// ---------------------------------------------------------------------------
// Tab swap
// ---------------------------------------------------------------------------

func TestWorkspaceViewTabTogglesViewAndBackAgain(t *testing.T) {
	m := model{width: 120, height: 40, input: newTestInput()}

	updated, _ := m.Update(keyMsg("tab"))
	m1 := updated.(model)
	if m1.view != viewWorkspaces {
		t.Fatalf("Tab must switch to viewWorkspaces; got %v", m1.view)
	}

	updated2, _ := m1.Update(keyMsg("tab"))
	m2 := updated2.(model)
	if m2.view != viewSessions {
		t.Fatalf("second Tab must switch back to viewSessions; got %v", m2.view)
	}
}

func TestWorkspaceViewTabPreservesSessionCursor(t *testing.T) {
	m := model{
		width: 120, height: 40, input: newTestInput(),
		sessionCursor: 3,
		workspaceRows: []settings.Row{
			makeRow("/r", "/r/a", "a", "t", settings.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/b", "b", "t", settings.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/c", "c", "t", settings.StateStopped, state.AttnInactive, fixedNow),
			makeRow("/r", "/r/d", "d", "t", settings.StateStopped, state.AttnInactive, fixedNow),
		},
	}

	updated, _ := m.Update(keyMsg("tab"))
	m1 := updated.(model)
	// Navigate the Workspaces view; this must never touch sessionCursor.
	updated2, _ := m1.Update(keyMsg("j"))
	m2 := updated2.(model)
	updated3, _ := m2.Update(keyMsg("tab"))
	m3 := updated3.(model)

	if m3.view != viewSessions {
		t.Fatalf("second Tab must return to viewSessions; got %v", m3.view)
	}
	if m3.sessionCursor != 3 {
		t.Errorf("sessionCursor must survive a round trip through the Workspaces view; got %d, want 3", m3.sessionCursor)
	}
}

// ---------------------------------------------------------------------------
// Listing / rendering
// ---------------------------------------------------------------------------

func TestWorkspaceViewListsWorkspacesWithSessionCountAndDetails(t *testing.T) {
	m := model{
		width: 120, height: 40,
		view: viewWorkspaces,
		wsStatuses: []workspace.WorkspaceStatus{
			makeWsStatus("payments",
				makeSessionStatus("feature-x", "feature-x", settings.StateRunning, "/home/me/api", "/home/me/web"),
			),
			makeWsStatus("infra",
				makeSessionStatus("upgrade", "upgrade", settings.StateStopped, "/home/me/infra"),
			),
		},
	}

	out := m.View()
	for _, want := range []string{"payments", "infra", "feature-x", "upgrade", "api", "web", "1 sessions"} {
		if !strings.Contains(out, want) {
			t.Errorf("Workspaces view missing %q; got:\n%s", want, out)
		}
	}
}

func TestWorkspaceViewEmptyWorkspaceShowsCreateHint(t *testing.T) {
	m := model{
		width: 120, height: 40,
		view:       viewWorkspaces,
		wsStatuses: []workspace.WorkspaceStatus{makeWsStatus("empty-ws")},
	}

	out := m.View()
	if !strings.Contains(out, "press n") {
		t.Errorf("empty workspace must hint that n creates a session; got:\n%s", out)
	}
}

func TestWorkspaceViewEmptyStateWhenNoWorkspacesConfigured(t *testing.T) {
	m := model{width: 120, height: 40, view: viewWorkspaces}

	out := m.View()
	if !strings.Contains(out, "no workspaces configured") {
		t.Errorf("no-workspaces state must render an empty-state hint, not a blank pane; got:\n%s", out)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("no-workspaces state must not render a blank pane")
	}
}

// ---------------------------------------------------------------------------
// Scrolling
// ---------------------------------------------------------------------------

// manyWsStatuses returns n single-session workspaces, each individually
// identifiable by its session branch, so a test can assert on which one is
// visible after scrolling.
func manyWsStatuses(n int) []workspace.WorkspaceStatus {
	out := make([]workspace.WorkspaceStatus, 0, n)
	for i := 0; i < n; i++ {
		branch := "branch-" + string(rune('a'+i))
		out = append(out, makeWsStatus("ws-"+string(rune('a'+i)), makeSessionStatus("sess", branch, settings.StateStopped, "/home/me/r")))
	}
	return out
}

func TestWorkspaceViewScrollKeepsCursorVisibleOnJ(t *testing.T) {
	m := model{width: 120, height: 12, view: viewWorkspaces, wsStatuses: manyWsStatuses(20)}

	// Each workspace contributes 2 entries (header + 1 session); move the
	// cursor past the last entry so it clamps there, well past the first
	// screenful.
	for i := 0; i < 50; i++ {
		updated, _ := m.Update(keyMsg("j"))
		m = updated.(model)
	}

	total := wsEntryCount(m.wsStatuses)
	if m.wsCursor != total-1 {
		t.Fatalf("cursor should clamp at the last entry (%d); got %d", total-1, m.wsCursor)
	}
	out := m.View()
	if !strings.Contains(out, "branch-t") {
		t.Errorf("scrolled view must keep the selected last session (branch-t) visible; got:\n%s", out)
	}
}

func TestWorkspaceViewGGJumpsToTop(t *testing.T) {
	m := model{width: 120, height: 12, view: viewWorkspaces, wsStatuses: manyWsStatuses(20)}
	m.wsCursor = wsEntryCount(m.wsStatuses) - 1
	m.syncWsScroll()

	updated, _ := m.Update(keyMsg("g"))
	m1 := updated.(model)
	if !m1.wsPendingG {
		t.Fatal("first g must arm wsPendingG")
	}
	updated2, _ := m1.Update(keyMsg("g"))
	m2 := updated2.(model)

	if m2.wsCursor != 0 {
		t.Fatalf("gg must jump the cursor to 0; got %d", m2.wsCursor)
	}
	out := m2.View()
	if !strings.Contains(out, "ws-a") {
		t.Errorf("after gg the first workspace (ws-a) must be visible; got:\n%s", out)
	}
}

func TestWorkspaceViewGJumpsToBottom(t *testing.T) {
	m := model{width: 120, height: 12, view: viewWorkspaces, wsStatuses: manyWsStatuses(20)}

	updated, _ := m.Update(keyMsg("G"))
	m1 := updated.(model)

	want := wsEntryCount(m1.wsStatuses) - 1
	if m1.wsCursor != want {
		t.Fatalf("G must jump the cursor to the last entry (%d); got %d", want, m1.wsCursor)
	}
	out := m1.View()
	if !strings.Contains(out, "branch-t") {
		t.Errorf("after G the last session (branch-t) must be visible; got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// --demo / --status gate
// ---------------------------------------------------------------------------

// batchLen reports how many cmds a tea.Batch-produced message carries,
// without invoking any of them (tea.Batch's own returned func — when more
// than one cmd is non-nil — returns tea.BatchMsg(cmds) directly; none of the
// batched cmds run until bubbletea's runtime schedules them). This lets a
// test assert on which cmds Init batched (e.g. whether the workspace-status
// load was included) without risking a call into waitSnapshot/tickCmd, both
// of which block for real.
func batchLen(msg tea.Msg) int {
	if bm, ok := msg.(tea.BatchMsg); ok {
		return len(bm)
	}
	if msg == nil {
		return 0
	}
	return 1
}

func TestWorkspaceViewInitSkipsStoreLoadUnderDemo(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := newModel(ch, nil, false, false)
	m.demo = true

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init must return a non-nil cmd (waitSnapshot + tick)")
	}
	// Under demo the workspace-status cmd must be nil, so the batch has
	// exactly 2 elements (waitSnapshot, tick) rather than 3.
	if n := batchLen(cmd()); n != 2 {
		t.Errorf("Init under demo must batch exactly 2 cmds (no workspace-status load); got %d", n)
	}
}

func TestWorkspaceViewInitDispatchesStoreLoadWhenNotDemo(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := newModel(ch, nil, false, false)

	cmd := m.Init()
	if n := batchLen(cmd()); n != 3 {
		t.Errorf("Init outside demo must batch 3 cmds (waitSnapshot, tick, workspace-status load); got %d", n)
	}
}

func TestWorkspaceViewLoadCmdNilStoreIsSafe(t *testing.T) {
	cmd := loadWorkspaceStatusCmd(nil, state.Snapshot{})
	msg := cmd()
	got, ok := msg.(wsStatusMsg)
	if !ok {
		t.Fatalf("expected wsStatusMsg, got %T", msg)
	}
	if len(got.statuses) != 0 {
		t.Errorf("nil store must yield an empty result; got %v", got.statuses)
	}
}

// ---------------------------------------------------------------------------
// Coalesced rebuild — mirrors TestSnapshotMsgCoalescesWhileBuildInFlight for
// buildWorkspaceRowsCmd/workspaceRowsMsg.
// ---------------------------------------------------------------------------

func TestWorkspaceViewStatusLoadSetsBuildingOnFirstSnapshot(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)

	updated, _ := m.Update(snapshotMsg(state.Snapshot{}))
	m2 := updated.(model)

	if !m2.wsBuilding {
		t.Error("wsBuilding must be true after the first snapshotMsg")
	}
}

func TestWorkspaceViewStatusLoadDemoSuppressesLoad(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	m.demo = true

	updated, _ := m.Update(snapshotMsg(state.Snapshot{}))
	m2 := updated.(model)

	if m2.wsBuilding {
		t.Error("demo mode must not dispatch a workspace-status load (wsBuilding should stay false)")
	}
}

func TestWorkspaceViewStatusLoadCoalescesBurstOfSnapshots(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)

	updated, _ := m.Update(snapshotMsg(state.Snapshot{}))
	m1 := updated.(model)
	if !m1.wsBuilding {
		t.Fatal("wsBuilding must be true after the first snapshotMsg")
	}
	if m1.wsDirty {
		t.Fatal("wsDirty must be false after the first snapshotMsg")
	}

	updated2, _ := m1.Update(snapshotMsg(state.Snapshot{}))
	m2 := updated2.(model)
	if !m2.wsDirty {
		t.Error("a second snapshotMsg while a load is in flight must set wsDirty rather than starting another load")
	}
}

func TestWorkspaceViewStatusMsgAppliesResultAndClampsCursor(t *testing.T) {
	m := model{wsBuilding: true, wsCursor: 5}

	statuses := []workspace.WorkspaceStatus{makeWsStatus("solo", makeSessionStatus("s", "s", settings.StateStopped, "/r"))}
	updated, _ := m.Update(wsStatusMsg{statuses: statuses})
	m2 := updated.(model)

	if len(m2.wsStatuses) != 1 {
		t.Fatalf("wsStatuses must be applied; got %v", m2.wsStatuses)
	}
	if m2.wsBuilding {
		t.Error("wsBuilding must be false after wsStatusMsg with no dirty flag")
	}
	want := wsEntryCount(statuses) - 1
	if m2.wsCursor != want {
		t.Errorf("wsCursor must clamp to the last valid entry (%d); got %d", want, m2.wsCursor)
	}
}

func TestWorkspaceViewStatusMsgDispatchesFollowUpWhenDirty(t *testing.T) {
	m := model{wsBuilding: true, wsDirty: true}

	updated, cmd := m.Update(wsStatusMsg{})
	m2 := updated.(model)

	if m2.wsDirty {
		t.Error("wsDirty must be cleared after wsStatusMsg")
	}
	if !m2.wsBuilding {
		t.Error("wsBuilding must be true (follow-up load dispatched)")
	}
	if cmd == nil {
		t.Fatal("a follow-up load cmd must be returned when wsDirty was true")
	}
	msg := cmd()
	if _, ok := msg.(wsStatusMsg); !ok {
		t.Errorf("follow-up cmd must return wsStatusMsg; got %T", msg)
	}
}
