package ui

// workspace_backfill_test.go — tests for step 15's membership-backfill flow:
// the membershipChangedMsg handler that opens the multi-select prompt (or
// skips straight to a reload for a workspace with no sessions), the prompt's
// own key handling, and the fully-persisted per-session
// AssembleMember/TeardownMember + SaveWorkspaces round trip against a real
// store and real, local, throwaway git repos — mirroring
// workspace_cmd_test.go's own end-to-end section, since AssembleMember and
// TeardownMember shell out to the real git binary with no injectable seam.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/workspace"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// backfillAppliedFrom runs cmd and returns the first wsBackfillAppliedMsg
// produced, mirroring wsSessionAssembledFrom (workspace_cmd_test.go).
func backfillAppliedFrom(t *testing.T, cmd tea.Cmd) wsBackfillAppliedMsg {
	t.Helper()
	msg := runCmd(cmd)
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if am, ok := c().(wsBackfillAppliedMsg); ok {
				return am
			}
		}
		t.Fatal("no wsBackfillAppliedMsg produced by batched cmd")
	}
	if am, ok := msg.(wsBackfillAppliedMsg); ok {
		return am
	}
	t.Fatalf("expected wsBackfillAppliedMsg, got %T", msg)
	return wsBackfillAppliedMsg{}
}

// runGitInDir runs a git subcommand in dir, failing the test on error.
func runGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// ---------------------------------------------------------------------------
// handleMembershipChanged / membershipChangedMsg wiring
// ---------------------------------------------------------------------------

func TestWorkspaceBackfill_MembershipChangedWithSessionsOpensPrompt(t *testing.T) {
	m := model{
		width: 120, height: 40,
		wsStatuses: []workspace.WorkspaceStatus{{
			Workspace: workspace.Workspace{
				Name: "payments",
				Sessions: []workspace.Session{
					{Name: "session-one"},
					{Name: "session-two"},
				},
			},
		}},
	}

	updated, cmd := m.Update(membershipChangedMsg{workspace: "payments", repo: "/repo/c", attached: true})
	m2 := updated.(model)

	if m2.prompt != promptWorkspaceBackfill {
		t.Fatalf("a workspace with sessions must open promptWorkspaceBackfill, got %v", m2.prompt)
	}
	if m2.wsBackfillWorkspace != "payments" || m2.wsBackfillRepo != "/repo/c" || !m2.wsBackfillAttached {
		t.Errorf("prompt state must capture the change, got %+v", m2)
	}
	if len(m2.wsBackfillSessions) != 2 || m2.wsBackfillSessions[0] != "session-one" || m2.wsBackfillSessions[1] != "session-two" {
		t.Errorf("prompt must offer every session in the workspace, got %v", m2.wsBackfillSessions)
	}
	if cmd != nil {
		t.Error("opening the prompt must not dispatch a cmd")
	}
}

func TestWorkspaceBackfill_MembershipChangedWithNoSessionsReloads(t *testing.T) {
	m := model{
		width: 120,
		wsStatuses: []workspace.WorkspaceStatus{{
			Workspace: workspace.Workspace{Name: "payments"},
		}},
	}

	updated, cmd := m.Update(membershipChangedMsg{workspace: "payments", repo: "/repo/c", attached: false})
	m2 := updated.(model)

	if m2.prompt == promptWorkspaceBackfill {
		t.Error("a workspace with no sessions must not open the backfill prompt")
	}
	if !m2.wsBuilding {
		t.Error("a workspace with no sessions must still dispatch a reload")
	}
	if cmd == nil {
		t.Fatal("expected a reload cmd")
	}
}

func TestWorkspaceBackfill_MembershipChangedForUnknownWorkspaceReloads(t *testing.T) {
	m := model{width: 120}

	updated, cmd := m.Update(membershipChangedMsg{workspace: "ghost", repo: "/repo/c", attached: true})
	m2 := updated.(model)

	if m2.prompt == promptWorkspaceBackfill {
		t.Error("an unknown workspace must not open the backfill prompt")
	}
	if cmd == nil {
		t.Fatal("expected a reload cmd even when the workspace is not in wsStatuses")
	}
}

// ---------------------------------------------------------------------------
// updateWorkspaceBackfillActive
// ---------------------------------------------------------------------------

func backfillPromptModel(sessions ...string) model {
	return model{
		width: 120, height: 40,
		prompt:              promptWorkspaceBackfill,
		wsBackfillWorkspace: "payments",
		wsBackfillRepo:      "/repo/c",
		wsBackfillAttached:  true,
		wsBackfillSessions:  sessions,
		wsBackfillSelected:  map[string]bool{},
	}
}

func TestWorkspaceBackfill_CursorMovementClamped(t *testing.T) {
	m := backfillPromptModel("a", "b")

	updated, _ := m.Update(keyMsg("up"))
	m2 := updated.(model)
	if m2.wsBackfillCursor != 0 {
		t.Errorf("up at the top must clamp to 0, got %d", m2.wsBackfillCursor)
	}

	updated, _ = m2.Update(keyMsg("down"))
	m3 := updated.(model)
	if m3.wsBackfillCursor != 1 {
		t.Errorf("down must advance to 1, got %d", m3.wsBackfillCursor)
	}

	updated, _ = m3.Update(keyMsg("down"))
	m4 := updated.(model)
	if m4.wsBackfillCursor != 1 {
		t.Errorf("down at the bottom must clamp to 1, got %d", m4.wsBackfillCursor)
	}
}

func TestWorkspaceBackfill_SpaceTogglesHighlightedSession(t *testing.T) {
	m := backfillPromptModel("session-one", "session-two")
	m.wsBackfillCursor = 1

	updated, _ := m.Update(keyMsg(" "))
	m2 := updated.(model)
	if !m2.wsBackfillSelected["session-two"] {
		t.Error("space must check the highlighted session")
	}
	if m2.wsBackfillSelected["session-one"] {
		t.Error("space must not affect the other session")
	}

	updated, _ = m2.Update(keyMsg(" "))
	m3 := updated.(model)
	if m3.wsBackfillSelected["session-two"] {
		t.Error("space again must uncheck it")
	}
}

func TestWorkspaceBackfill_EscSkipsWithNoBackfillAndReloads(t *testing.T) {
	m := backfillPromptModel("session-one")
	m.wsBackfillSelected["session-one"] = true

	updated, cmd := m.Update(keyMsg("esc"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("esc must return to promptIdle, got %v", m2.prompt)
	}
	if m2.wsBackfillWorkspace != "" || len(m2.wsBackfillSessions) != 0 {
		t.Error("esc must clear the backfill prompt state")
	}
	if !m2.wsBuilding {
		t.Error("esc must still dispatch a reload")
	}
	if cmd == nil {
		t.Fatal("expected a reload cmd")
	}
	msg := runCmd(cmd)
	if _, ok := msg.(wsBackfillAppliedMsg); ok {
		t.Error("esc must never dispatch backfillMembershipCmd")
	}
}

func TestWorkspaceBackfill_EnterWithNoneSelectedAppliesNothingAndReloads(t *testing.T) {
	m := backfillPromptModel("session-one", "session-two")

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("enter must return to promptIdle, got %v", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("expected a reload cmd")
	}
	msg := runCmd(cmd)
	if _, ok := msg.(wsBackfillAppliedMsg); ok {
		t.Error("enter with nothing selected must not dispatch backfillMembershipCmd")
	}
}

func TestWorkspaceBackfill_EnterWithSelectionDispatchesBackfillForChosenOnly(t *testing.T) {
	store := &fakeStoreOps{workspaces: []workspace.Workspace{{
		Name: "payments",
		Sessions: []workspace.Session{
			{Name: "session-one"},
			{Name: "session-two"},
		},
	}}}
	m := backfillPromptModel("session-one", "session-two")
	m.wsBackfillAttached = true
	m.wsBackfillRepo = filepath.Join(t.TempDir(), "does-not-exist")
	m.wsBackfillSelected["session-one"] = true
	m.store = store

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("enter must return to promptIdle, got %v", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("expected a cmd")
	}

	result := backfillAppliedFrom(t, cmd)
	if result.workspaceName != "payments" || result.repo != m.wsBackfillRepo || !result.attached {
		t.Errorf("result must carry workspace/repo/attached, got %+v", result)
	}
	// The repo path is deliberately not a git repository, so AssembleMember
	// fails — but only for the one session that was actually chosen.
	if len(result.failures) != 1 || result.failures[0].session != "session-one" {
		t.Errorf("expected exactly one failure for session-one, got %+v", result.failures)
	}
}

// ---------------------------------------------------------------------------
// backfillOneSession — not-found edges
// ---------------------------------------------------------------------------

func TestWorkspaceBackfill_UnknownWorkspaceFails(t *testing.T) {
	store := &fakeStoreOps{}
	err := backfillOneSession(store, "ghost", "/repo/c", true, "session-one")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected an error naming the workspace, got %v", err)
	}
}

func TestWorkspaceBackfill_UnknownSessionFails(t *testing.T) {
	store := &fakeStoreOps{workspaces: []workspace.Workspace{{Name: "payments"}}}
	err := backfillOneSession(store, "payments", "/repo/c", true, "ghost-session")
	if err == nil || !strings.Contains(err.Error(), "ghost-session") {
		t.Errorf("expected an error naming the session, got %v", err)
	}
}

func TestWorkspaceBackfill_DetachOfAbsentMemberIsNoop(t *testing.T) {
	store := &fakeStoreOps{workspaces: []workspace.Workspace{{
		Name: "payments",
		Sessions: []workspace.Session{{
			Name:    "session-one",
			Members: []workspace.SessionMember{{RepoPath: "/repo/a"}},
		}},
	}}}
	err := backfillOneSession(store, "payments", "/repo/never-was-a-member", false, "session-one")
	if err != nil {
		t.Errorf("detaching a member the session never had must be a no-op success, got %v", err)
	}
	if len(store.workspaces[0].Sessions[0].Members) != 1 {
		t.Errorf("the session's existing members must be untouched, got %+v", store.workspaces[0].Sessions[0].Members)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: real git repos + real store
// ---------------------------------------------------------------------------

func TestWorkspaceBackfill_EndToEnd_AttachAddsWorktreeToChosenSessionOnly(t *testing.T) {
	setWsTestXDG(t)

	repoA := newWsCmdTestRepo(t, "repo-a")
	repoB := newWsCmdTestRepo(t, "repo-b")
	repoC := newWsCmdTestRepo(t, "repo-c")

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("payments"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := store.AttachRepo("payments", repoA); err != nil {
		t.Fatalf("AttachRepo repoA: %v", err)
	}
	if err := store.AttachRepo("payments", repoB); err != nil {
		t.Fatalf("AttachRepo repoB: %v", err)
	}

	ws, ok := findWorkspaceByName(mustLoad(t, store), "payments")
	if !ok {
		t.Fatal("workspace payments missing")
	}
	root := mustResolveTestRoot(t)

	sessionOne, err := workspace.AssembleSession(ws, root, "Session One", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession Session One: %v", err)
	}
	if err := store.AddSession("payments", sessionOne); err != nil {
		t.Fatalf("AddSession Session One: %v", err)
	}
	sessionTwo, err := workspace.AssembleSession(ws, root, "Session Two", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession Session Two: %v", err)
	}
	if err := store.AddSession("payments", sessionTwo); err != nil {
		t.Fatalf("AddSession Session Two: %v", err)
	}

	// Commit the membership change (mirrors what the modal already did).
	if err := store.AttachRepo("payments", repoC); err != nil {
		t.Fatalf("AttachRepo repoC: %v", err)
	}
	// Deliberately break the backfill for Session Two only: pre-create its
	// branch in repoC so AssembleMember's branch-free pre-flight fails there.
	runGitInDir(t, repoC, "branch", sessionTwo.Branch)

	storeOps := realStoreOps{store: store}
	cmd := backfillMembershipCmd(storeOps, "payments", repoC, true, []string{"Session One", "Session Two"})
	result := backfillAppliedFrom(t, cmd)

	if len(result.failures) != 1 || result.failures[0].session != "Session Two" {
		t.Fatalf("expected exactly one failure for Session Two, got %+v", result.failures)
	}
	if !strings.Contains(result.failures[0].err.Error(), sessionTwo.Branch) {
		t.Errorf("failure must name the branch/repo, got %v", result.failures[0].err)
	}

	loaded := mustLoad(t, store)
	loadedWs, ok := findWorkspaceByName(loaded, "payments")
	if !ok {
		t.Fatal("workspace payments missing after backfill")
	}
	if !hasMember(loadedWs.Members, repoC) {
		t.Error("the membership change itself must stand regardless of backfill outcome")
	}

	one, ok := findSessionByName(loadedWs.Sessions, "Session One")
	if !ok {
		t.Fatal("Session One missing after backfill")
	}
	if !hasSessionMember(one.Members, repoC) {
		t.Errorf("Session One (chosen, succeeded) must gain a member for repoC, got %+v", one.Members)
	}
	oneRepoC, _ := findSessionMember(one.Members, repoC)
	if _, statErr := os.Stat(oneRepoC.WorktreePath); statErr != nil {
		t.Errorf("Session One's new worktree must exist on disk: %v", statErr)
	}

	two, ok := findSessionByName(loadedWs.Sessions, "Session Two")
	if !ok {
		t.Fatal("Session Two missing after backfill")
	}
	if hasSessionMember(two.Members, repoC) {
		t.Errorf("Session Two (chosen, but failed) must not gain a member for repoC, got %+v", two.Members)
	}
}

func TestWorkspaceBackfill_EndToEnd_SkippedSessionOmitsRepoAfterReload(t *testing.T) {
	setWsTestXDG(t)

	repoA := newWsCmdTestRepo(t, "repo-a")
	repoC := newWsCmdTestRepo(t, "repo-c")

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("payments"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := store.AttachRepo("payments", repoA); err != nil {
		t.Fatalf("AttachRepo repoA: %v", err)
	}

	ws, _ := findWorkspaceByName(mustLoad(t, store), "payments")
	root := mustResolveTestRoot(t)
	skipped, err := workspace.AssembleSession(ws, root, "Skipped Session", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}
	if err := store.AddSession("payments", skipped); err != nil {
		t.Fatalf("AddSession: %v", err)
	}

	if err := store.AttachRepo("payments", repoC); err != nil {
		t.Fatalf("AttachRepo repoC: %v", err)
	}

	// The user chose not to backfill "Skipped Session" at all — simulate by
	// dispatching the backfill Cmd with an empty chosen list, exactly what
	// the prompt's enter-with-nothing-selected path produces.
	storeOps := realStoreOps{store: store}
	cmd := backfillMembershipCmd(storeOps, "payments", repoC, true, nil)
	result := backfillAppliedFrom(t, cmd)
	if len(result.failures) != 0 {
		t.Fatalf("an empty chosen list must produce no failures, got %+v", result.failures)
	}

	loaded := mustLoad(t, store)
	loadedWs, _ := findWorkspaceByName(loaded, "payments")
	sess, ok := findSessionByName(loadedWs.Sessions, "Skipped Session")
	if !ok {
		t.Fatal("Skipped Session missing after reload")
	}
	if hasSessionMember(sess.Members, repoC) {
		t.Errorf("a skipped session's member list must still omit the new repo, got %+v", sess.Members)
	}
}

func TestWorkspaceBackfill_EndToEnd_DetachRemovesWorktreeAndLeavesOthers(t *testing.T) {
	setWsTestXDG(t)

	repoA := newWsCmdTestRepo(t, "repo-a")
	repoB := newWsCmdTestRepo(t, "repo-b")
	repoC := newWsCmdTestRepo(t, "repo-c")

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("payments"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	for _, r := range []string{repoA, repoB, repoC} {
		if err := store.AttachRepo("payments", r); err != nil {
			t.Fatalf("AttachRepo %s: %v", r, err)
		}
	}

	ws, _ := findWorkspaceByName(mustLoad(t, store), "payments")
	root := mustResolveTestRoot(t)
	sess, err := workspace.AssembleSession(ws, root, "Feature X", "opencode")
	if err != nil {
		t.Fatalf("AssembleSession: %v", err)
	}
	if err := store.AddSession("payments", sess); err != nil {
		t.Fatalf("AddSession: %v", err)
	}

	if err := store.DetachRepo("payments", repoC); err != nil {
		t.Fatalf("DetachRepo repoC: %v", err)
	}

	storeOps := realStoreOps{store: store}
	cmd := backfillMembershipCmd(storeOps, "payments", repoC, false, []string{"Feature X"})
	result := backfillAppliedFrom(t, cmd)
	if len(result.failures) != 0 {
		t.Fatalf("expected no failures, got %+v", result.failures)
	}

	removedMember, ok := findSessionMember(sess.Members, repoC)
	if !ok {
		t.Fatal("test setup: session must have had a member for repoC")
	}
	if _, statErr := os.Stat(removedMember.WorktreePath); statErr == nil {
		t.Errorf("repoC's worktree must be removed, but %q still exists", removedMember.WorktreePath)
	}

	loaded := mustLoad(t, store)
	loadedWs, _ := findWorkspaceByName(loaded, "payments")
	loadedSess, ok := findSessionByName(loadedWs.Sessions, "Feature X")
	if !ok {
		t.Fatal("Feature X missing after backfill")
	}
	if hasSessionMember(loadedSess.Members, repoC) {
		t.Errorf("repoC must be gone from the session's members, got %+v", loadedSess.Members)
	}
	if !hasSessionMember(loadedSess.Members, repoA) || !hasSessionMember(loadedSess.Members, repoB) {
		t.Fatalf("the other members must be untouched, got %+v", loadedSess.Members)
	}
	for _, path := range []string{repoA, repoB} {
		mem, _ := findSessionMember(loadedSess.Members, path)
		if _, statErr := os.Stat(mem.WorktreePath); statErr != nil {
			t.Errorf("member %s's worktree must still exist: %v", path, statErr)
		}
	}
}

// ---------------------------------------------------------------------------
// Small helpers for the end-to-end assertions above
// ---------------------------------------------------------------------------

func mustLoad(t *testing.T, store *workspace.Store) []workspace.Workspace {
	t.Helper()
	loaded, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	return loaded
}

func hasMember(members []workspace.MemberRepo, repoPath string) bool {
	for _, m := range members {
		if m.Path == repoPath {
			return true
		}
	}
	return false
}

func hasSessionMember(members []workspace.SessionMember, repoPath string) bool {
	_, ok := findSessionMember(members, repoPath)
	return ok
}

func findSessionMember(members []workspace.SessionMember, repoPath string) (workspace.SessionMember, bool) {
	for _, m := range members {
		if m.RepoPath == repoPath {
			return m, true
		}
	}
	return workspace.SessionMember{}, false
}
