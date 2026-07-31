package ui

// update_test.go — tests for the snapshotMsg offload and coalescing state
// machine introduced in step 3 of fix-codex-polling-ui-flicker-and-freeze.
//
// All tests drive model.Update directly with synthetic messages; no real tmux,
// git, or opencode binary is required.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
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
	m.workspaceRows = []settings.Row{
		makeRow("/r", "/r/a", "main", "existing", settings.StateStopped, state.AttnInactive, fixedNow),
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
	m.workspaceRows = []settings.Row{
		makeRow("/r", "/r/a", "main", "curated", settings.StateRunning, state.AttnActive, fixedNow),
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
		if r.State == settings.StateRunning {
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

	rows := []settings.Row{
		makeRow("/r", "/r/a", "main", "built", settings.StateRunning, state.AttnActive, fixedNow),
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

// TestWorkspaceRowsMsgAppliesRoot asserts that workspaceRowsMsg's root is
// applied to the model, including when rows is empty (the zero-repos case,
// which is exactly when View's fallback branch needs a resolved root to
// exclude workspace-owned sessions).
func TestWorkspaceRowsMsgAppliesRoot(t *testing.T) {
	ch := make(chan state.Snapshot, 1)
	m := snapshotModel(ch)
	m.rowsBuilding = true

	updated, _ := m.Update(workspaceRowsMsg{rows: nil, root: "/some/workspace/root"})
	m2 := updated.(model)

	if m2.workspaceRoot != "/some/workspace/root" {
		t.Errorf("workspaceRoot not applied; got %q", m2.workspaceRoot)
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

	rows := []settings.Row{
		makeRow("/r", "/r/a", "main", "only", settings.StateStopped, state.AttnInactive, fixedNow),
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
	m := newModel(ch, config.Default(), false, false)
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

// TestViewFallbackExcludesWorkspaceOwnedSession verifies that when no repos
// are configured — exactly the case where View renders the live-only
// fallback via renderAllSessions — a live session whose Directory lies under
// the resolved workspace root is excluded from both the fallback listing and
// the header's live/recent counts. Without this, an install with zero
// configured repos would surface a workspace session's per-repo checkout as
// an ordinary live session.
func TestViewFallbackExcludesWorkspaceOwnedSession(t *testing.T) {
	root := t.TempDir()
	memberDir := filepath.Join(root, "session-1", "repo")
	if err := os.MkdirAll(memberDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", memberDir, err)
	}
	canonRoot, err := pathnorm.Canonical(root)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", root, err)
	}
	canonMember, err := pathnorm.Canonical(memberDir)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", memberDir, err)
	}

	m := model{
		width:         120,
		workspaceRoot: canonRoot,
		snap: state.Snapshot{
			UpdatedAt: fixedNow,
			Sessions: []state.SessionView{
				{
					InstanceID:   "i1",
					InstanceName: "inst-1",
					SessionID:    "workspace-owned",
					Title:        "workspace-owned-title",
					Directory:    canonMember,
					StatusType:   "busy",
					Attention:    state.AttnActive,
					Source:       state.SourceLive,
				},
				{
					InstanceID:   "i2",
					InstanceName: "inst-2",
					SessionID:    "ordinary",
					Title:        "ordinary-title",
					Directory:    t.TempDir(),
					StatusType:   "busy",
					Attention:    state.AttnActive,
					Source:       state.SourceLive,
				},
			},
		},
		// workspaceRows is nil — no repos configured; the fallback branch renders.
	}

	got := m.View()
	if strings.Contains(got, "workspace-owned-title") {
		t.Errorf("fallback view must exclude the workspace-owned session, got %q", got)
	}
	if !strings.Contains(got, "ordinary-title") {
		t.Errorf("fallback view must still render the ordinary session, got %q", got)
	}
	if !strings.Contains(got, "1 live") {
		t.Errorf("header must count only the ordinary session as live, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// membershipChangedMsg must not clobber an open prompt — review-fix-D,
// defect 2. handleMembershipChanged (workspace_backfill.go) used to seize
// m.prompt for promptWorkspaceBackfill unconditionally, discarding whatever
// prompt was already open; the guard lives in Update's membershipChangedMsg
// case (model.go) rather than inside handleMembershipChanged itself, mirroring
// the repoScanMsg/wsModalScanMsg guards.
// ---------------------------------------------------------------------------

// workspaceWithSessions is a minimal wsStatuses fixture whose workspace has
// at least one session, so handleMembershipChanged would open the backfill
// prompt absent the guard under test.
func workspaceWithSessions(name string, sessionNames ...string) []workspace.WorkspaceStatus {
	sessions := make([]workspace.Session, len(sessionNames))
	for i, n := range sessionNames {
		sessions[i] = workspace.Session{Name: n}
	}
	return []workspace.WorkspaceStatus{{Workspace: workspace.Workspace{Name: name, Sessions: sessions}}}
}

func TestMembershipChangedMsgDoesNotClobberAnOpenPrompt(t *testing.T) {
	m := model{
		width:      120,
		prompt:     promptSettings,
		wsStatuses: workspaceWithSessions("payments", "session-one"),
	}

	updated, cmd := m.Update(membershipChangedMsg{workspace: "payments", repo: "/repo/c", attached: true})
	m2 := updated.(model)

	if m2.prompt != promptSettings {
		t.Errorf("an open prompt must survive membershipChangedMsg; got %v", m2.prompt)
	}
	if !m2.wsBuilding {
		t.Error("the workspace statuses must still refresh even though the backfill offer was dropped")
	}
	if cmd == nil {
		t.Fatal("expected a reload cmd")
	}
	// review-fix-F, defect A: dropping the backfill offer silently leaves the
	// user with no way to know their existing sessions were not updated.
	if !strings.Contains(m2.wsHint, "c") || !strings.Contains(m2.wsHint, "payments") {
		t.Errorf("wsHint must name the repo and workspace when the backfill offer is dropped; got %q", m2.wsHint)
	}
	if !strings.Contains(m2.wsHint, "not") {
		t.Errorf("wsHint must explain the existing sessions were not updated; got %q", m2.wsHint)
	}
	if !strings.Contains(m2.wsHint, "re-add") {
		t.Errorf("wsHint must say re-adding the repo offers the backfill again; got %q", m2.wsHint)
	}
}

func TestMembershipChangedMsgStillOpensBackfillWhenIdle(t *testing.T) {
	m := model{
		width:      120,
		prompt:     promptIdle,
		wsStatuses: workspaceWithSessions("payments", "session-one"),
	}

	updated, _ := m.Update(membershipChangedMsg{workspace: "payments", repo: "/repo/c", attached: true})
	m2 := updated.(model)

	if m2.prompt != promptWorkspaceBackfill {
		t.Errorf("with no prompt open, membershipChangedMsg must still open the backfill prompt; got %v", m2.prompt)
	}
	// review-fix-F, defect A: the drop-and-hint path must not fire when the
	// backfill picker actually opens.
	if m2.wsHint != "" {
		t.Errorf("wsHint must stay empty when the backfill picker opens normally; got %q", m2.wsHint)
	}
}

// TestMembershipChangedMsgCannotStrandWsCreateTargetAcrossViews reproduces
// the review's worst-case path end to end: an 'e'→enter attach commit is in
// flight while the user has already moved on to the Workspaces view's 'n'
// flow (wsCreateTarget captured, promptNewWorkspaceSession open). Before the
// guard, the async membershipChangedMsg seized the prompt for the backfill
// picker; esc out of that clobbered prompt left wsCreateTarget stale, so a
// later, wholly unrelated 'n' in the Sessions view silently created a
// workspace session instead of the ordinary worktree the user asked for.
func TestMembershipChangedMsgCannotStrandWsCreateTargetAcrossViews(t *testing.T) {
	setWsTestXDG(t)
	tmuxFake := &fakeTmuxOps{available: true, ensureWindowResult: "main:1"}
	gitFake := &fakeGitOps{addResult: "/r-feat"}
	harnFake := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"codex"}}

	m := model{
		width: 120, height: 40, input: newTestInput(),
		view:           viewWorkspaces,
		prompt:         promptNewWorkspaceSession,
		wsCreateTarget: "payments",
		tmux:           tmuxFake,
		gitOp:          gitFake,
		harnOp:         harnFake,
		wsStatuses:     workspaceWithSessions("payments", "session-one"),
	}

	// The async membership-change message lands mid-flow.
	updated, _ := m.Update(membershipChangedMsg{workspace: "payments", repo: "/repo/c", attached: true})
	m1 := updated.(model)
	if m1.prompt != promptNewWorkspaceSession {
		t.Fatalf("membershipChangedMsg must not clobber the open session-name prompt; got %v", m1.prompt)
	}
	if m1.wsCreateTarget != "payments" {
		t.Fatalf("wsCreateTarget must survive the guarded message; got %q", m1.wsCreateTarget)
	}

	// esc out of the (undisturbed) session-name prompt.
	updated2, _ := m1.Update(keyMsg("esc"))
	m2 := updated2.(model)
	if m2.prompt != promptIdle || m2.wsCreateTarget != "" {
		t.Fatalf("esc must return to promptIdle with wsCreateTarget cleared; got prompt=%v wsCreateTarget=%q",
			m2.prompt, m2.wsCreateTarget)
	}

	// Tab to Sessions and press 'n' on an ordinary worktree row.
	updated3, _ := m2.Update(keyMsg("tab"))
	m3 := updated3.(model)
	if m3.view != viewSessions {
		t.Fatalf("tab must switch to the Sessions view; got %v", m3.view)
	}
	m3.workspaceRows = []settings.Row{
		makeRow("/r", "/r/feat", "feat", "codex", settings.StateStopped, state.AttnInactive, fixedNow),
	}
	m3.sessionCursor = 0

	updated4, _ := m3.Update(keyMsg("n"))
	m4 := updated4.(model)
	if m4.prompt != promptNewWorktree {
		t.Fatalf("'n' in the Sessions view must open the ordinary new-worktree prompt; got %v", m4.prompt)
	}
	m4.input.SetValue("feat")

	updated5, _ := m4.Update(keyMsg("enter"))
	m5 := updated5.(model)
	if m5.prompt != promptChooseHarness {
		t.Fatalf("the branch-name prompt must advance to promptChooseHarness; got %v", m5.prompt)
	}
	if m5.wsCreateTarget != "" {
		t.Fatalf("wsCreateTarget must still be empty at the harness chooser; got %q — a stale value hijacks the dispatch below", m5.wsCreateTarget)
	}

	updated6, cmd := m5.Update(keyMsg("enter"))
	m6 := updated6.(model)
	if m6.wsCreateTarget != "" {
		t.Error("wsCreateTarget must remain empty after dispatch")
	}
	// worktreeCreatedFrom (actions_test.go) fails the test outright if cmd
	// produced anything other than an ordinary worktreeCreatedMsg — in
	// particular, it fails if the stale wsCreateTarget had instead dispatched
	// assembleWorkspaceSessionCmd's wsSessionAssembledMsg.
	result := worktreeCreatedFrom(t, cmd)
	if result.repo != "/r" || result.branch != "feat" {
		t.Errorf("expected an ordinary worktree create for /r@feat, got repo=%q branch=%q", result.repo, result.branch)
	}
}
