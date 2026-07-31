package ui

// workspace_modal_test.go — tests for step 14's repo-membership modal ('e' in
// the Workspaces view): the scan/attach/detach Cmds, the combined
// member+candidate list's pure helpers, the 'e' key + wsModalScanMsg/
// wsModalActionErrMsg Update wiring, and the AttachRepo/DetachRepo round trip
// against a real store. membershipChangedMsg is deliberately left unhandled
// (step 15 is its first consumer); this file only asserts it is emitted with
// the right shape and that Update currently ignores it.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// errWorkspaceModalTest is a sentinel error used to drive the modal's error
// paths.
var errWorkspaceModalTest = errors.New("boom")

// ---------------------------------------------------------------------------
// fakeModalStoreOps
// ---------------------------------------------------------------------------

type attachCall struct {
	workspaceName string
	repoPath      string
}

type detachCall struct {
	workspaceName string
	repoPath      string
}

// fakeModalStoreOps is a minimal in-memory storeOps for this file's tests.
// Distinct from workspace_cmd_test.go's fakeStoreOps and
// workspace_delete_test.go's fakeDeleteStoreOps: this file's assertions need
// AttachRepo/DetachRepo call recording and configurable errors, which
// neither of those provides.
type fakeModalStoreOps struct {
	attachErr error
	detachErr error

	attachCalls []attachCall
	detachCalls []detachCall
}

func (f *fakeModalStoreOps) LoadWorkspaces() ([]workspace.Workspace, error) { return nil, nil }
func (f *fakeModalStoreOps) SaveWorkspaces(_ []workspace.Workspace) error   { return nil }
func (f *fakeModalStoreOps) AddWorkspace(_ string) (workspace.Workspace, error) {
	return workspace.Workspace{}, errors.New("fakeModalStoreOps: AddWorkspace not supported")
}
func (f *fakeModalStoreOps) RemoveWorkspace(_ string) error {
	return errors.New("fakeModalStoreOps: RemoveWorkspace not supported")
}
func (f *fakeModalStoreOps) AddSession(_ string, _ workspace.Session) error {
	return errors.New("fakeModalStoreOps: AddSession not supported")
}
func (f *fakeModalStoreOps) RemoveSession(_, _ string) error {
	return errors.New("fakeModalStoreOps: RemoveSession not supported")
}
func (f *fakeModalStoreOps) AttachRepo(workspaceName, repoPath string) error {
	f.attachCalls = append(f.attachCalls, attachCall{workspaceName, repoPath})
	return f.attachErr
}
func (f *fakeModalStoreOps) DetachRepo(workspaceName, repoPath string) error {
	f.detachCalls = append(f.detachCalls, detachCall{workspaceName, repoPath})
	return f.detachErr
}

// ---------------------------------------------------------------------------
// excludeWorkspaceRootSubtree
// ---------------------------------------------------------------------------

func TestWorkspaceModal_ExcludeWorkspaceRootSubtreeDropsPathsUnderRoot(t *testing.T) {
	got := excludeWorkspaceRootSubtree(
		[]string{"/home/me/a", "/home/me/ws/proj/session/app", "/home/me/b"},
		"/home/me/ws",
	)
	want := []string{"/home/me/a", "/home/me/b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("excludeWorkspaceRootSubtree = %v, want %v", got, want)
	}
}

func TestWorkspaceModal_ExcludeWorkspaceRootSubtreeEmptyRootIsNoop(t *testing.T) {
	in := []string{"/a", "/b"}
	got := excludeWorkspaceRootSubtree(in, "")
	if len(got) != 2 {
		t.Errorf("empty root must return all paths unchanged; got %v", got)
	}
}

// ---------------------------------------------------------------------------
// scanWorkspaceModalCmd
// ---------------------------------------------------------------------------

func TestWorkspaceModal_ScanCmdCombinesMembersAndCandidatesExcludingRoot(t *testing.T) {
	root := t.TempDir()

	candidate := filepath.Join(root, "candidate")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate: %v", err)
	}
	initGitRepoAt(t, candidate)
	wantCandidate, err := pathnorm.Canonical(candidate)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}

	member := filepath.Join(root, "member")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatalf("mkdir member: %v", err)
	}
	initGitRepoAt(t, member)
	wantMember, err := pathnorm.Canonical(member)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}

	// A member's own worktree inside the workspace root contains a `.git`
	// *file*, which DiscoverRepos reports as a repo like any other — the scan
	// must exclude it anyway.
	wsRoot := filepath.Join(root, "workspaces")
	sessionWorktree := filepath.Join(wsRoot, "payments", "feature-x", "app")
	if err := os.MkdirAll(sessionWorktree, 0o755); err != nil {
		t.Fatalf("mkdir session worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionWorktree, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}
	canonWsRoot, err := pathnorm.Canonical(wsRoot)
	if err != nil {
		t.Fatalf("canonical wsRoot: %v", err)
	}

	msg := scanWorkspaceModalCmd(root, "payments", []string{wantMember}, canonWsRoot)().(wsModalScanMsg)
	if msg.err != nil {
		t.Fatalf("scan err: %v", msg.err)
	}

	var sawCandidate, sawMember bool
	for _, e := range msg.entries {
		if settings.PathUnderRoot(canonWsRoot, e.path) {
			t.Errorf("scan must exclude anything under the workspace root; got %+v", e)
		}
		switch e.path {
		case wantCandidate:
			sawCandidate = true
			if e.member {
				t.Errorf("candidate must not be flagged as a member: %+v", e)
			}
		case wantMember:
			sawMember = true
			if !e.member {
				t.Errorf("existing member must be flagged as a member: %+v", e)
			}
		}
	}
	if !sawCandidate {
		t.Errorf("scan must include the new candidate repo; got %+v", msg.entries)
	}
	if !sawMember {
		t.Errorf("scan must include the existing member (offered for removal); got %+v", msg.entries)
	}
}

// ---------------------------------------------------------------------------
// attachWorkspaceRepoCmd / detachWorkspaceRepoCmd
// ---------------------------------------------------------------------------

func TestWorkspaceModal_AttachRepoCmdSuccess(t *testing.T) {
	repo := initGitRepoForUI(t)
	want, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	store := &fakeModalStoreOps{}

	msg := attachWorkspaceRepoCmd(store, "payments", repo, nil)()
	changed, ok := msg.(membershipChangedMsg)
	if !ok {
		t.Fatalf("expected membershipChangedMsg, got %T (%+v)", msg, msg)
	}
	if changed.workspace != "payments" || changed.repo != want || !changed.attached {
		t.Fatalf("unexpected result %+v (want workspace=payments repo=%q attached=true)", changed, want)
	}
	if len(store.attachCalls) != 1 || store.attachCalls[0] != (attachCall{"payments", want}) {
		t.Errorf("AttachRepo must be called with (payments, %q); got %v", want, store.attachCalls)
	}
}

func TestWorkspaceModal_AttachRepoCmdNotGitRepoErrors(t *testing.T) {
	store := &fakeModalStoreOps{}
	msg := attachWorkspaceRepoCmd(store, "payments", t.TempDir(), nil)()
	errMsg, ok := msg.(wsModalActionErrMsg)
	if !ok {
		t.Fatalf("expected wsModalActionErrMsg, got %T", msg)
	}
	if errMsg.err == nil {
		t.Error("expected a non-nil error for a non-git directory")
	}
	if len(store.attachCalls) != 0 {
		t.Error("a validation failure must never reach AttachRepo")
	}
}

func TestWorkspaceModal_AttachRepoCmdHiddenBasenameRefused(t *testing.T) {
	hidden := filepath.Join(t.TempDir(), ".hidden")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepoAt(t, hidden)

	store := &fakeModalStoreOps{}
	msg := attachWorkspaceRepoCmd(store, "payments", hidden, nil)()
	errMsg, ok := msg.(wsModalActionErrMsg)
	if !ok {
		t.Fatalf("expected wsModalActionErrMsg, got %T", msg)
	}
	if !strings.Contains(errMsg.err.Error(), "hidden") {
		t.Errorf("error must name the hidden-basename conflict; got %q", errMsg.err.Error())
	}
	if len(store.attachCalls) != 0 {
		t.Error("a validation failure must never reach AttachRepo")
	}
}

func TestWorkspaceModal_AttachRepoCmdCollisionRefused(t *testing.T) {
	repoA := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(repoA, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepoAt(t, repoA)
	canonA, err := pathnorm.Canonical(repoA)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}

	repoB := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(repoB, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepoAt(t, repoB)

	store := &fakeModalStoreOps{}
	msg := attachWorkspaceRepoCmd(store, "payments", repoB, []string{canonA})()
	errMsg, ok := msg.(wsModalActionErrMsg)
	if !ok {
		t.Fatalf("expected wsModalActionErrMsg, got %T", msg)
	}
	if !strings.Contains(errMsg.err.Error(), "app") {
		t.Errorf("error must name the colliding basename; got %q", errMsg.err.Error())
	}
	if len(store.attachCalls) != 0 {
		t.Error("a validation failure must never reach AttachRepo")
	}
}

func TestWorkspaceModal_AttachRepoCmdStoreNilErrors(t *testing.T) {
	repo := initGitRepoForUI(t)
	msg := attachWorkspaceRepoCmd(nil, "payments", repo, nil)()
	if _, ok := msg.(wsModalActionErrMsg); !ok {
		t.Fatalf("expected wsModalActionErrMsg, got %T", msg)
	}
}

func TestWorkspaceModal_DetachRepoCmdSuccess(t *testing.T) {
	store := &fakeModalStoreOps{}
	msg := detachWorkspaceRepoCmd(store, "payments", "/repo/a")()
	changed, ok := msg.(membershipChangedMsg)
	if !ok {
		t.Fatalf("expected membershipChangedMsg, got %T (%+v)", msg, msg)
	}
	if changed.workspace != "payments" || changed.repo != "/repo/a" || changed.attached {
		t.Fatalf("unexpected result %+v", changed)
	}
	if len(store.detachCalls) != 1 || store.detachCalls[0] != (detachCall{"payments", "/repo/a"}) {
		t.Errorf("DetachRepo must be called with (payments, /repo/a); got %v", store.detachCalls)
	}
}

func TestWorkspaceModal_DetachRepoCmdStoreErrorSurfaces(t *testing.T) {
	store := &fakeModalStoreOps{detachErr: errWorkspaceModalTest}
	msg := detachWorkspaceRepoCmd(store, "payments", "/repo/a")()
	errMsg, ok := msg.(wsModalActionErrMsg)
	if !ok {
		t.Fatalf("expected wsModalActionErrMsg, got %T", msg)
	}
	if !errors.Is(errMsg.err, errWorkspaceModalTest) {
		t.Errorf("error must propagate the store's error; got %v", errMsg.err)
	}
}

func TestWorkspaceModal_DetachRepoCmdStoreNilErrors(t *testing.T) {
	msg := detachWorkspaceRepoCmd(nil, "payments", "/repo/a")()
	if _, ok := msg.(wsModalActionErrMsg); !ok {
		t.Fatalf("expected wsModalActionErrMsg, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// 'e' key + modal open
// ---------------------------------------------------------------------------

func TestWorkspaceModal_EKeyOpensModalAndScans(t *testing.T) {
	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		wsStatuses: []workspace.WorkspaceStatus{wsMemberWorkspace("payments", "/repo/a")},
	}

	updated, cmd := m.Update(keyMsg("e"))
	m2 := updated.(model)

	if m2.prompt != promptWorkspaceModal {
		t.Fatalf("'e' must open promptWorkspaceModal, got %v", m2.prompt)
	}
	if m2.wsModalWorkspace != "payments" {
		t.Errorf("wsModalWorkspace must capture the workspace under the cursor, got %q", m2.wsModalWorkspace)
	}
	if !m2.wsModalScanning {
		t.Error("'e' must mark the modal as scanning")
	}
	if !m2.input.Focused() {
		t.Error("'e' must focus the input")
	}
	if cmd == nil {
		t.Error("'e' must return a cmd (focus + scan batch)")
	}
}

func TestWorkspaceModal_EKeyNoWorkspaceIsNoop(t *testing.T) {
	m := model{width: 120, height: 40, view: viewWorkspaces, input: newTestInput()}

	updated, cmd := m.Update(keyMsg("e"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("'e' with no workspaces must stay idle, got %v", m2.prompt)
	}
	if cmd != nil {
		t.Error("'e' with no workspaces must return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// wsModalScanMsg handling
// ---------------------------------------------------------------------------

func TestWorkspaceModal_ScanMsgPopulatesEntriesAndMatches(t *testing.T) {
	m := model{
		width: 120, input: newTestInput(),
		prompt: promptWorkspaceModal, wsModalWorkspace: "payments", wsModalScanning: true,
	}

	entries := []wsModalEntry{{path: "/repo/a", member: true}, {path: "/repo/b"}}
	updated, _ := m.Update(wsModalScanMsg{workspace: "payments", entries: entries})
	m2 := updated.(model)

	if m2.wsModalScanning {
		t.Error("scan result must clear scanning flag")
	}
	if len(m2.wsModalMatches) != 2 {
		t.Errorf("expected 2 matches (empty query), got %v", m2.wsModalMatches)
	}
}

func TestWorkspaceModal_ScanMsgIgnoredWhenModalClosed(t *testing.T) {
	m := model{width: 120, prompt: promptIdle}
	updated, _ := m.Update(wsModalScanMsg{workspace: "payments", entries: []wsModalEntry{{path: "/x"}}})
	if got := updated.(model).wsModalEntries; got != nil {
		t.Errorf("stale scan result must be ignored when modal is closed; got %v", got)
	}
}

func TestWorkspaceModal_ScanMsgIgnoredForDifferentWorkspace(t *testing.T) {
	m := model{width: 120, prompt: promptWorkspaceModal, wsModalWorkspace: "payments", wsModalScanning: true}
	updated, _ := m.Update(wsModalScanMsg{workspace: "other", entries: []wsModalEntry{{path: "/x"}}})
	m2 := updated.(model)
	if !m2.wsModalScanning {
		t.Error("a scan result for a different workspace must be ignored (scanning flag left set)")
	}
	if m2.wsModalEntries != nil {
		t.Errorf("stale scan result for a different workspace must be ignored; got %v", m2.wsModalEntries)
	}
}

func TestWorkspaceModal_ScanMsgErrorSetsModalErr(t *testing.T) {
	m := model{
		width: 120, input: newTestInput(),
		prompt: promptWorkspaceModal, wsModalWorkspace: "payments", wsModalScanning: true,
	}
	updated, _ := m.Update(wsModalScanMsg{workspace: "payments", err: errWorkspaceModalTest})
	m2 := updated.(model)
	if !strings.Contains(m2.wsModalErr, "scan failed") {
		t.Errorf("expected scan-failed error, got %q", m2.wsModalErr)
	}
}

// ---------------------------------------------------------------------------
// modal key handling: filter, navigate, select, cancel
// ---------------------------------------------------------------------------

func TestWorkspaceModal_TypingFiltersMatches(t *testing.T) {
	m := model{width: 120, prompt: promptWorkspaceModal, input: newTestInput()}
	m.input.Focus()
	m.wsModalEntries = []wsModalEntry{{path: "/home/me/cogitator", member: true}, {path: "/home/me/notes"}}
	m.wsModalMatches = fuzzyMatchIndices("", wsModalEntryPaths(m.wsModalEntries))

	updated, _ := m.Update(keyMsg("c"))
	m2 := updated.(model)
	if len(m2.wsModalMatches) != 1 || !strings.Contains(m2.wsModalEntries[m2.wsModalMatches[0]].path, "cogitator") {
		t.Errorf("typing 'c' should filter to cogitator; got %v", m2.wsModalMatches)
	}
}

func TestWorkspaceModal_NavigationClamps(t *testing.T) {
	m := model{width: 120, prompt: promptWorkspaceModal, input: newTestInput()}
	m.wsModalMatches = []int{0, 1}
	m.wsModalCursor = 0

	up, _ := m.Update(keyMsg("up"))
	if c := up.(model).wsModalCursor; c != 0 {
		t.Errorf("up at top: cursor = %d, want 0", c)
	}
	down, _ := m.Update(keyMsg("down"))
	m = down.(model)
	if m.wsModalCursor != 1 {
		t.Fatalf("down: cursor = %d, want 1", m.wsModalCursor)
	}
	down2, _ := m.Update(keyMsg("down"))
	if c := down2.(model).wsModalCursor; c != 1 {
		t.Errorf("down past end: cursor = %d, want 1 (clamped)", c)
	}
}

func TestWorkspaceModal_EscClosesWithNoChange(t *testing.T) {
	m := model{width: 120, prompt: promptWorkspaceModal, input: newTestInput()}
	m.wsModalEntries = []wsModalEntry{{path: "/a"}}
	m.wsModalMatches = []int{0}

	updated, cmd := m.Update(keyMsg("esc"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("esc must close the modal; prompt = %v", m2.prompt)
	}
	if m2.wsModalEntries != nil || m2.wsModalMatches != nil {
		t.Errorf("esc must reset modal state; entries=%v matches=%v", m2.wsModalEntries, m2.wsModalMatches)
	}
	if cmd != nil {
		t.Error("esc must not dispatch a cmd")
	}
}

func TestWorkspaceModal_EnterWithNoMatchesNoop(t *testing.T) {
	m := model{width: 120, prompt: promptWorkspaceModal, input: newTestInput()}
	m.wsModalMatches = nil
	updated, cmd := m.Update(keyMsg("enter"))
	if cmd != nil {
		t.Error("enter with no matches must not dispatch a cmd")
	}
	if updated.(model).prompt != promptWorkspaceModal {
		t.Error("enter with no matches must keep the modal open")
	}
}

func TestWorkspaceModal_EnterOnCandidateDispatchesAttachAndCloses(t *testing.T) {
	repo := initGitRepoForUI(t)
	store := &fakeModalStoreOps{}
	m := model{
		width: 120, prompt: promptWorkspaceModal, input: newTestInput(),
		wsModalWorkspace: "payments", store: store,
	}
	m.wsModalEntries = []wsModalEntry{{path: repo}}
	m.wsModalMatches = []int{0}

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("enter must close the modal; prompt = %v", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("enter on a candidate must dispatch a cmd")
	}
	runCmd(cmd)
	if len(store.attachCalls) != 1 {
		t.Errorf("enter on a candidate must call AttachRepo; got attach=%v detach=%v", store.attachCalls, store.detachCalls)
	}
	if len(store.detachCalls) != 0 {
		t.Error("enter on a candidate must never call DetachRepo")
	}
}

func TestWorkspaceModal_EnterOnMemberDispatchesDetachAndCloses(t *testing.T) {
	store := &fakeModalStoreOps{}
	m := model{
		width: 120, prompt: promptWorkspaceModal, input: newTestInput(),
		wsModalWorkspace: "payments", store: store,
	}
	m.wsModalEntries = []wsModalEntry{{path: "/repo/a", member: true}}
	m.wsModalMatches = []int{0}

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("enter must close the modal; prompt = %v", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("enter on a member must dispatch a cmd")
	}
	runCmd(cmd)
	if len(store.detachCalls) != 1 || store.detachCalls[0].repoPath != "/repo/a" {
		t.Errorf("enter on a member must call DetachRepo with its path; got %v", store.detachCalls)
	}
	if len(store.attachCalls) != 0 {
		t.Error("enter on a member must never call AttachRepo")
	}
}

// ---------------------------------------------------------------------------
// wsModalActionErrMsg / membershipChangedMsg handling
// ---------------------------------------------------------------------------

func TestWorkspaceModal_ActionErrMsgSetsHint(t *testing.T) {
	m := model{width: 120}
	updated, _ := m.Update(wsModalActionErrMsg{err: errWorkspaceModalTest})
	if got := updated.(model).wsHint; !strings.Contains(got, "membership change failed") {
		t.Errorf("hint: got %q, want membership-change-failed", got)
	}
}

func TestWorkspaceModal_MembershipChangedMsgUnhandled(t *testing.T) {
	m := model{width: 120, wsHint: "unchanged"}
	updated, cmd := m.Update(membershipChangedMsg{workspace: "payments", repo: "/repo/a", attached: true})
	m2 := updated.(model)
	if m2.wsHint != "unchanged" {
		t.Errorf("membershipChangedMsg must not be consumed yet; wsHint changed to %q", m2.wsHint)
	}
	if cmd != nil {
		t.Error("membershipChangedMsg must not dispatch a cmd (nothing consumes it yet)")
	}
}

// ---------------------------------------------------------------------------
// Rendering smoke test
// ---------------------------------------------------------------------------

func TestWorkspaceModal_RenderDoesNotPanic(t *testing.T) {
	cases := []model{
		{width: 120, height: 40, input: newTestInput(), wsModalWorkspace: "payments", wsModalScanning: true},
		{width: 120, height: 40, input: newTestInput(), wsModalWorkspace: "payments", wsModalErr: "scan failed: boom"},
		{width: 120, height: 40, input: newTestInput(), wsModalWorkspace: "payments"},
		{
			width: 120, height: 40, input: newTestInput(), wsModalWorkspace: "payments",
			wsModalEntries: []wsModalEntry{{path: "/repo/a", member: true}, {path: "/repo/b"}},
			wsModalMatches: []int{0, 1},
		},
	}
	for i, m := range cases {
		if got := m.renderWorkspaceModal(80, 20); got == "" {
			t.Errorf("case %d: renderWorkspaceModal returned empty string", i)
		}
	}
}

// ---------------------------------------------------------------------------
// End-to-end: real store, restart persistence
// ---------------------------------------------------------------------------

func TestWorkspaceModal_EndToEndAttachPersistsAcrossReload(t *testing.T) {
	setWsTestXDG(t)
	repo := newWsCmdTestRepo(t, "repo-a")

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("payments"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}

	msg := attachWorkspaceRepoCmd(realStoreOps{store: store}, "payments", repo, nil)()
	changed, ok := msg.(membershipChangedMsg)
	if !ok || !changed.attached {
		t.Fatalf("expected a successful attach membershipChangedMsg, got %+v (%T)", msg, msg)
	}

	// Simulate a restart: a fresh Store instance reloads from disk.
	reloaded, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore (reload): %v", err)
	}
	workspaces, err := reloaded.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	ws, ok := findWorkspaceByName(workspaces, "payments")
	if !ok {
		t.Fatal("workspace \"payments\" missing after reload")
	}
	if len(ws.Members) != 1 || ws.Members[0].Path != changed.repo {
		t.Errorf("member must persist across reload; got %+v", ws.Members)
	}
}

func TestWorkspaceModal_EndToEndDetachPersistsAndTouchesNothingOnDisk(t *testing.T) {
	setWsTestXDG(t)
	repo := newWsCmdTestRepo(t, "repo-a")

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("payments"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := store.AttachRepo("payments", repo); err != nil {
		t.Fatalf("AttachRepo: %v", err)
	}

	msg := detachWorkspaceRepoCmd(realStoreOps{store: store}, "payments", repo)()
	changed, ok := msg.(membershipChangedMsg)
	if !ok || changed.attached {
		t.Fatalf("expected a successful detach membershipChangedMsg, got %+v (%T)", msg, msg)
	}

	reloaded, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore (reload): %v", err)
	}
	workspaces, err := reloaded.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	ws, ok := findWorkspaceByName(workspaces, "payments")
	if !ok {
		t.Fatal("workspace \"payments\" missing after reload")
	}
	if len(ws.Members) != 0 {
		t.Errorf("member must be gone after detach + reload; got %+v", ws.Members)
	}
	if _, statErr := os.Stat(repo); statErr != nil {
		t.Errorf("detach must never touch the repo on disk; stat error: %v", statErr)
	}
}
