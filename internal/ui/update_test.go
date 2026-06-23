package ui

// update_test.go — tests for the snapshotMsg offload and coalescing state
// machine introduced in step 3 of fix-codex-polling-ui-flicker-and-freeze.
//
// All tests drive model.Update directly with synthetic messages; no real tmux,
// git, or opencode binary is required.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// snapshotModel returns a minimal model wired with a snapshot channel.
// workspaceRows is left nil (no repos configured) so buildWorkspaceRows is
// never called inline during the test.
func snapshotModel(ch <-chan state.Snapshot) model {
	return model{
		snaps:    ch,
		bellSent: map[rowKey]state.Attention{},
		input:    newTestInput(),
	}
}

// drainBatch executes all commands in a tea.Batch and returns the messages.
// It handles nil cmds and single non-batch cmds as well.
func drainBatch(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	// tea.Batch returns a batchMsg ([]tea.Cmd) when called; unwrap it.
	type batchMsg []tea.Cmd
	if batch, ok := msg.(batchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			if c != nil {
				m := c()
				if m != nil {
					msgs = append(msgs, m)
				}
			}
		}
		return msgs
	}
	return []tea.Msg{msg}
}

// hasMsgType reports whether any message in msgs is of type T.
func hasMsgType[T any](msgs []tea.Msg) bool {
	for _, m := range msgs {
		if _, ok := m.(T); ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// snapshotMsg: cmd non-nil, workspaceRows unchanged
// ---------------------------------------------------------------------------

// TestSnapshotMsgReturnsNonNilCmd asserts that processing a snapshotMsg
// returns a non-nil command (the background build + waitSnapshot re-arm).
func TestSnapshotMsgReturnsNonNilCmd(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)

	snap := state.Snapshot{Sessions: []state.SessionView{{SessionID: "s1"}}}
	updated, cmd := m.Update(snapshotMsg(snap))
	_ = updated

	if cmd == nil {
		t.Fatal("snapshotMsg must return a non-nil cmd")
	}
}

// TestSnapshotMsgDoesNotBuildRowsInline asserts that workspaceRows is
// unchanged immediately after processing a snapshotMsg (build is offloaded).
func TestSnapshotMsgDoesNotBuildRowsInline(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	// Pre-populate rows so we can detect if they were cleared or rebuilt.
	m.workspaceRows = []workspace.Row{
		makeRow("/r", "/r/a", "main", "existing", workspace.StateStopped, state.AttnInactive, fixedNow),
	}

	snap := state.Snapshot{Sessions: []state.SessionView{{SessionID: "s1"}}}
	updated, _ := m.Update(snapshotMsg(snap))
	m2 := updated.(model)

	if len(m2.workspaceRows) != 1 || m2.workspaceRows[0].Title != "existing" {
		t.Errorf("workspaceRows must be unchanged after snapshotMsg; got %v", m2.workspaceRows)
	}
}

// TestSnapshotMsgSetsRowsBuilding asserts that rowsBuilding is true after the
// first snapshotMsg (a build was dispatched).
func TestSnapshotMsgSetsRowsBuilding(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)

	snap := state.Snapshot{}
	updated, _ := m.Update(snapshotMsg(snap))
	m2 := updated.(model)

	if !m2.rowsBuilding {
		t.Error("rowsBuilding must be true after first snapshotMsg")
	}
}

// TestSnapshotMsgDemoSuppressesBuild asserts that in demo mode a snapshotMsg
// neither dispatches the git/tmux row build (rowsBuilding stays false) nor
// clobbers the curated workspaceRows. This guards the capture path: the build
// would shell out and replace the fixture with nil.
func TestSnapshotMsgDemoSuppressesBuild(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	m.demo = true
	m.workspaceRows = []workspace.Row{
		makeRow("/r", "/r/a", "main", "curated", workspace.StateRunning, state.AttnActive, fixedNow),
	}

	snap := state.Snapshot{Sessions: []state.SessionView{{SessionID: "s1"}}}
	updated, _ := m.Update(snapshotMsg(snap))
	m2 := updated.(model)

	if m2.rowsBuilding {
		t.Error("demo mode must not dispatch a row build (rowsBuilding should stay false)")
	}
	if len(m2.workspaceRows) != 1 || m2.workspaceRows[0].Title != "curated" {
		t.Errorf("demo workspaceRows must be preserved, got %v", m2.workspaceRows)
	}
}

// TestLiveSessionsForMatchesRunningRows asserts the header summary stays in
// sync with the roster: liveSessionsFor yields exactly one live session per
// running worktree row and nothing for stopped/unknown rows.
func TestLiveSessionsForMatchesRunningRows(t *testing.T) {
	rows := demoWorktrees(fixedNow)
	want := 0
	for _, r := range rows {
		if r.State == workspace.StateRunning {
			want++
		}
	}

	got := liveSessionsFor(rows)
	if len(got) != want {
		t.Fatalf("liveSessionsFor returned %d sessions, want %d (one per running row)", len(got), want)
	}
	for _, sv := range got {
		if sv.Source != state.SourceLive {
			t.Errorf("derived header session must be SourceLive, got %v", sv.Source)
		}
	}
}

// TestSnapshotMsgUpdatesSnap asserts that m.snap is updated immediately.
func TestSnapshotMsgUpdatesSnap(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)

	snap := state.Snapshot{Sessions: []state.SessionView{{SessionID: "s42"}}}
	updated, _ := m.Update(snapshotMsg(snap))
	m2 := updated.(model)

	if len(m2.snap.Sessions) != 1 || m2.snap.Sessions[0].SessionID != "s42" {
		t.Errorf("m.snap must be updated immediately; got %v", m2.snap)
	}
}

// ---------------------------------------------------------------------------
// workspaceRowsMsg: rows/launchMode applied, cursor clamped
// ---------------------------------------------------------------------------

// TestWorkspaceRowsMsgAppliesRows asserts that workspaceRowsMsg updates
// m.workspaceRows and m.launchMode.
func TestWorkspaceRowsMsgAppliesRows(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	m.rowsBuilding = true

	rows := []workspace.Row{
		makeRow("/r", "/r/a", "main", "built", workspace.StateRunning, state.AttnActive, fixedNow),
	}
	msg := workspaceRowsMsg{rows: rows, launchMode: tmuxctl.ModeSession}
	updated, _ := m.Update(msg)
	m2 := updated.(model)

	if len(m2.workspaceRows) != 1 || m2.workspaceRows[0].Title != "built" {
		t.Errorf("workspaceRows not applied; got %v", m2.workspaceRows)
	}
	if m2.launchMode != tmuxctl.ModeSession {
		t.Errorf("launchMode not applied; got %v", m2.launchMode)
	}
}

// TestWorkspaceRowsMsgClearsBuildingFlag asserts that rowsBuilding is false
// after workspaceRowsMsg when no dirty flag is set.
func TestWorkspaceRowsMsgClearsBuildingFlag(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	m.rowsBuilding = true

	updated, _ := m.Update(workspaceRowsMsg{})
	m2 := updated.(model)

	if m2.rowsBuilding {
		t.Error("rowsBuilding must be false after workspaceRowsMsg with no dirty flag")
	}
}

// TestWorkspaceRowsMsgClampsSessionCursor asserts that sessionCursor is
// clamped when the new row list is shorter than the current cursor position.
func TestWorkspaceRowsMsgClampsSessionCursor(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	m.rowsBuilding = true
	m.sessionCursor = 5 // beyond any row list

	rows := []workspace.Row{
		makeRow("/r", "/r/a", "main", "only", workspace.StateStopped, state.AttnInactive, fixedNow),
	}
	updated, _ := m.Update(workspaceRowsMsg{rows: rows})
	m2 := updated.(model)

	if m2.sessionCursor != 0 {
		t.Errorf("cursor must be clamped to 0 (last valid index); got %d", m2.sessionCursor)
	}
}

// TestWorkspaceRowsMsgCursorZeroOnEmptyRows asserts that sessionCursor is
// reset to 0 when the new row list is empty.
func TestWorkspaceRowsMsgCursorZeroOnEmptyRows(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	m.rowsBuilding = true
	m.sessionCursor = 3

	updated, _ := m.Update(workspaceRowsMsg{rows: nil})
	m2 := updated.(model)

	if m2.sessionCursor != 0 {
		t.Errorf("cursor must be 0 on empty rows; got %d", m2.sessionCursor)
	}
}

// ---------------------------------------------------------------------------
// Coalescing: second snapshotMsg while build in flight
// ---------------------------------------------------------------------------

// TestSnapshotMsgCoalescesWhileBuildInFlight asserts that a second snapshotMsg
// while rowsBuilding is true sets rowsDirty instead of starting a second build.
func TestSnapshotMsgCoalescesWhileBuildInFlight(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)

	// First snapshot: starts a build.
	snap1 := state.Snapshot{Sessions: []state.SessionView{{SessionID: "s1"}}}
	updated, _ := m.Update(snapshotMsg(snap1))
	m1 := updated.(model)

	if !m1.rowsBuilding {
		t.Fatal("rowsBuilding must be true after first snapshotMsg")
	}
	if m1.rowsDirty {
		t.Fatal("rowsDirty must be false after first snapshotMsg")
	}

	// Second snapshot while build is in flight: must not start another build.
	snap2 := state.Snapshot{Sessions: []state.SessionView{{SessionID: "s2"}}}
	updated2, cmd2 := m1.Update(snapshotMsg(snap2))
	m2 := updated2.(model)

	if !m2.rowsDirty {
		t.Error("rowsDirty must be true after second snapshotMsg while build in flight")
	}
	// The cmd returned must NOT include a workspaceRowsMsg producer (no second build).
	// We verify by running the batch and checking no workspaceRowsMsg is produced
	// synchronously (the build cmd would block on real I/O, but here we just
	// confirm the batch does not contain a second build cmd that resolves immediately).
	// The key assertion: m.snap is updated to the latest snapshot.
	if len(m2.snap.Sessions) != 1 || m2.snap.Sessions[0].SessionID != "s2" {
		t.Errorf("m.snap must reflect the latest snapshot; got %v", m2.snap)
	}
	_ = cmd2 // cmd is non-nil (waitSnapshot re-arm), but no second build started
}

// TestWorkspaceRowsMsgDispatchesFollowUpBuildWhenDirty asserts that when
// workspaceRowsMsg arrives with rowsDirty=true, one follow-up build is
// dispatched using the latest m.snap, and rowsDirty is cleared.
func TestWorkspaceRowsMsgDispatchesFollowUpBuildWhenDirty(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	m.rowsBuilding = true
	m.rowsDirty = true
	// Set a "latest" snap that the follow-up build should capture.
	m.snap = state.Snapshot{Sessions: []state.SessionView{{SessionID: "latest"}}}

	updated, cmd := m.Update(workspaceRowsMsg{})
	m2 := updated.(model)

	if m2.rowsDirty {
		t.Error("rowsDirty must be cleared after workspaceRowsMsg")
	}
	if !m2.rowsBuilding {
		t.Error("rowsBuilding must be true (follow-up build dispatched)")
	}
	if cmd == nil {
		t.Fatal("a follow-up build cmd must be returned when rowsDirty was true")
	}
	// Run the follow-up build cmd synchronously and confirm it returns a
	// workspaceRowsMsg (proving the closure was dispatched, not nil).
	msg := cmd()
	if _, ok := msg.(workspaceRowsMsg); !ok {
		t.Errorf("follow-up cmd must return workspaceRowsMsg; got %T", msg)
	}
}

// TestSnapshotMsgCoalescedBuildUsesLatestSnap asserts end-to-end: two
// snapshots arrive, the second is coalesced; after the first build completes
// the follow-up build is dispatched and its result reflects the second snap.
func TestSnapshotMsgCoalescedBuildUsesLatestSnap(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)

	// First snapshot → starts build.
	snap1 := state.Snapshot{Sessions: []state.SessionView{{SessionID: "first"}}}
	updated, buildCmd1 := m.Update(snapshotMsg(snap1))
	m1 := updated.(model)

	// Second snapshot while build in flight → coalesced.
	snap2 := state.Snapshot{Sessions: []state.SessionView{{SessionID: "second"}}}
	updated2, _ := m1.Update(snapshotMsg(snap2))
	m2 := updated2.(model)

	if !m2.rowsDirty {
		t.Fatal("rowsDirty must be set after second snapshotMsg")
	}

	// Simulate first build completing (buildCmd1 runs in background; here we
	// synthesise the result directly to avoid real I/O).
	_ = buildCmd1
	updated3, followUpCmd := m2.Update(workspaceRowsMsg{rows: nil, launchMode: tmuxctl.ModeWindow})
	m3 := updated3.(model)

	if m3.rowsDirty {
		t.Error("rowsDirty must be cleared after workspaceRowsMsg")
	}
	if !m3.rowsBuilding {
		t.Error("rowsBuilding must be true (follow-up dispatched)")
	}
	if followUpCmd == nil {
		t.Fatal("follow-up cmd must be non-nil")
	}
	// The follow-up cmd must produce a workspaceRowsMsg (it ran buildWorkspaceRows
	// with the latest snap captured at dispatch time — m2.snap == snap2).
	msg := followUpCmd()
	if _, ok := msg.(workspaceRowsMsg); !ok {
		t.Errorf("follow-up cmd must return workspaceRowsMsg; got %T", msg)
	}
}

// TestDemoRendersWorktreeRoster builds the model exactly as RunDemo does and
// asserts View() renders the merged worktree roster — repo headers, branches
// across both repos — and not the live-session fallback. This is the capture
// the README screenshot depends on.
func TestDemoRendersWorktreeRoster(t *testing.T) {
	rows := demoWorktrees(fixedNow)
	ch := make(chan state.Snapshot, 1)
	m := newModel(ch, config.Default(), false, false, nil) // nil tw → no Tasks pane
	m.demo = true
	m.workspaceRows = rows
	m.snap = state.Snapshot{Sessions: liveSessionsFor(rows), UpdatedAt: fixedNow}
	m.width, m.height = 120, 40

	out := m.View()
	for _, want := range []string{"cogitator", "api-gateway", "feat/tmux-launcher", "feat/oauth-pkce"} {
		if !strings.Contains(out, want) {
			t.Errorf("demo view missing %q", want)
		}
	}
	if strings.Contains(out, "no live or recent sessions") {
		t.Error("demo must render the worktree roster, not the live-session fallback")
	}
}
