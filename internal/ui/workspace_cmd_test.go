package ui

// workspace_cmd_test.go — tests for step 11's Workspaces-view creation flow:
// 'N' (new, empty workspace) and 'n' (new session — one worktree per member
// repo, all on one new branch), including validation, the optimistic
// animated spinner row, and the fully-persisted end-to-end AssembleSession +
// AddSession round trip.
//
// tmux and harness operations are injected via the existing fakes so no real
// tmux server or opencode binary is required. workspace-store operations use
// fakeStoreOps for validation/dispatch tests and a real *workspace.Store
// (backed by a temp $XDG_CONFIG_HOME, exactly like internal/workspace's own
// store_test.go) for the end-to-end tests, alongside real, local, throwaway
// git repos (mirroring internal/workspace/assemble_test.go) — AssembleSession
// itself shells out to the real git binary with no injectable seam, so a
// true round trip cannot avoid it.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// ---------------------------------------------------------------------------
// fakeStoreOps
// ---------------------------------------------------------------------------

type addSessionCall struct {
	workspaceName string
	session       workspace.Session
}

// fakeStoreOps is a minimal in-memory storeOps for tests that only need to
// verify dispatch (that the right store method was called with the right
// arguments), without touching disk.
type fakeStoreOps struct {
	workspaces []workspace.Workspace

	loadErr         error
	addWorkspaceErr error
	addSessionErr   error

	addWorkspaceCalls []string
	addSessionCalls   []addSessionCall
}

func (f *fakeStoreOps) LoadWorkspaces() ([]workspace.Workspace, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.workspaces, nil
}

func (f *fakeStoreOps) SaveWorkspaces(workspaces []workspace.Workspace) error {
	f.workspaces = workspaces
	return nil
}

func (f *fakeStoreOps) AddWorkspace(name string) (workspace.Workspace, error) {
	f.addWorkspaceCalls = append(f.addWorkspaceCalls, name)
	if f.addWorkspaceErr != nil {
		return workspace.Workspace{}, f.addWorkspaceErr
	}
	ws := workspace.Workspace{Name: name}
	f.workspaces = append(f.workspaces, ws)
	return ws, nil
}

func (f *fakeStoreOps) RemoveWorkspace(name string) error { return nil }

func (f *fakeStoreOps) AddSession(workspaceName string, session workspace.Session) error {
	f.addSessionCalls = append(f.addSessionCalls, addSessionCall{workspaceName, session})
	if f.addSessionErr != nil {
		return f.addSessionErr
	}
	for i, ws := range f.workspaces {
		if ws.Name == workspaceName {
			f.workspaces[i].Sessions = append(f.workspaces[i].Sessions, session)
		}
	}
	return nil
}

func (f *fakeStoreOps) RemoveSession(workspaceName, sessionName string) error { return nil }
func (f *fakeStoreOps) AttachRepo(workspaceName, repoPath string) error       { return nil }
func (f *fakeStoreOps) DetachRepo(workspaceName, repoPath string) error       { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newWsCmdTestRepo creates a temporary git repository with an initial commit
// on "main" and returns its canonical path, mirroring
// internal/workspace/assemble_test.go's newAssembleTestRepo.
func newWsCmdTestRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	cmds := [][]string{
		{"git", "init", "-q", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "-q", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}
	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	return canonical
}

// setWsTestXDG points $XDG_CONFIG_HOME and $XDG_DATA_HOME at fresh temp
// directories for the duration of the test, so settings.LoadConfig,
// workspace.NewStore, and settings.ResolveWorkspaceRoot's default all resolve
// under an isolated sandbox rather than the real user config.
func setWsTestXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// wsSessionAssembledFrom runs cmd — which may be a tea.Batch of the assemble
// Cmd and the spinner ticker — and returns the first wsSessionAssembledMsg
// produced, mirroring worktreeCreatedFrom (actions_test.go).
func wsSessionAssembledFrom(t *testing.T, cmd tea.Cmd) wsSessionAssembledMsg {
	t.Helper()
	msg := runCmd(cmd)
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if wm, ok := c().(wsSessionAssembledMsg); ok {
				return wm
			}
		}
		t.Fatal("no wsSessionAssembledMsg produced by batched cmd")
	}
	if wm, ok := msg.(wsSessionAssembledMsg); ok {
		return wm
	}
	t.Fatalf("expected wsSessionAssembledMsg, got %T", msg)
	return wsSessionAssembledMsg{}
}

// wsMemberWorkspace builds a workspace.WorkspaceStatus with the given member
// repo paths and no sessions, for tests exercising 'n's member-count gate and
// the create flow. Distinct from workspace_view_test.go's makeWsStatus, which
// has no way to set Workspace.Members.
func wsMemberWorkspace(name string, memberPaths ...string) workspace.WorkspaceStatus {
	members := make([]workspace.MemberRepo, 0, len(memberPaths))
	for _, p := range memberPaths {
		members = append(members, workspace.MemberRepo{Path: p})
	}
	return workspace.WorkspaceStatus{Workspace: workspace.Workspace{Name: name, Members: members}}
}

// ---------------------------------------------------------------------------
// 'N' — new, empty workspace
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_NKeyOpensNewWorkspacePrompt(t *testing.T) {
	m := model{width: 120, height: 40, view: viewWorkspaces, input: newTestInput()}

	updated, _ := m.Update(keyMsg("N"))
	m2 := updated.(model)

	if m2.prompt != promptNewWorkspace {
		t.Fatalf("N must open promptNewWorkspace, got %v", m2.prompt)
	}
	if !m2.input.Focused() {
		t.Error("N must focus the input")
	}
}

func TestWorkspaceCreate_NewWorkspacePromptEmptyNameCancelsSilently(t *testing.T) {
	ti := newTestInput()
	ti.SetValue("   ")
	store := &fakeStoreOps{}
	m := model{width: 120, prompt: promptNewWorkspace, input: ti, store: store}

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("empty name must cancel to promptIdle, got %v", m2.prompt)
	}
	if len(store.addWorkspaceCalls) != 0 {
		t.Errorf("empty name must not call AddWorkspace, got %v", store.addWorkspaceCalls)
	}
	_ = cmd
}

func TestWorkspaceCreate_NewWorkspacePromptEscCancels(t *testing.T) {
	m := model{width: 120, prompt: promptNewWorkspace, input: newTestInput()}

	updated, cmd := m.Update(keyMsg("esc"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("esc must cancel to promptIdle, got %v", m2.prompt)
	}
	if cmd != nil {
		t.Error("esc must return nil cmd")
	}
}

func TestWorkspaceCreate_NewWorkspacePromptEnterDispatchesCreateWorkspaceCmd(t *testing.T) {
	ti := newTestInput()
	ti.SetValue("infra")
	store := &fakeStoreOps{}
	m := model{width: 120, prompt: promptNewWorkspace, input: ti, store: store}

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("enter must return to promptIdle, got %v", m2.prompt)
	}
	if cmd == nil {
		t.Fatal("enter with a non-empty name must return a cmd")
	}

	// tea.Batch collapses to the single cmd directly when the other (here,
	// the textinput's own Update cmd for a plain "enter") is nil — it only
	// returns tea.BatchMsg when 2+ cmds are non-nil — so accept either shape.
	msg := runCmd(cmd)
	var created wsWorkspaceCreatedMsg
	found := false
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, c := range m {
			if c == nil {
				continue
			}
			if wc, ok := c().(wsWorkspaceCreatedMsg); ok {
				created, found = wc, true
			}
		}
	case wsWorkspaceCreatedMsg:
		created, found = m, true
	}
	if !found {
		t.Fatalf("no wsWorkspaceCreatedMsg produced, got %T", msg)
	}
	if created.err != nil {
		t.Fatalf("unexpected error: %v", created.err)
	}
	if len(store.addWorkspaceCalls) != 1 || store.addWorkspaceCalls[0] != "infra" {
		t.Errorf("AddWorkspace must be called with %q, got %v", "infra", store.addWorkspaceCalls)
	}

	// Feeding the success back into Update must trigger a reload so the new
	// workspace appears without waiting for the next snapshot tick.
	updated2, reloadCmd := m2.Update(created)
	m3 := updated2.(model)
	if !m3.wsBuilding {
		t.Error("a successful create must dispatch a reload (wsBuilding true)")
	}
	if reloadCmd == nil {
		t.Fatal("a successful create must return a reload cmd")
	}
}

func TestWorkspaceCreate_CreateWorkspaceCmdSurfacesStoreError(t *testing.T) {
	store := &fakeStoreOps{addWorkspaceErr: errors.New("workspace \"infra\" already exists")}
	m := model{width: 120}

	updated, _ := m.Update(createWorkspaceCmd(store, "infra")())
	m2 := updated.(model)

	if m2.wsHint == "" || !strings.Contains(m2.wsHint, "infra") {
		t.Errorf("store error must surface in wsHint naming the workspace, got %q", m2.wsHint)
	}
	if m2.wsBuilding {
		t.Error("a failed create must not dispatch a reload")
	}
}

// ---------------------------------------------------------------------------
// 'n' — new session: member-count gate
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_NKeyWithNoMembersReportsHint(t *testing.T) {
	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		wsStatuses: []workspace.WorkspaceStatus{wsMemberWorkspace("empty-ws")},
	}

	updated, _ := m.Update(keyMsg("n"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("no members must not open the session-name prompt, got prompt=%v", m2.prompt)
	}
	if m2.wsHint == "" || !strings.Contains(m2.wsHint, "empty-ws") {
		t.Errorf("no members must report a hint naming the workspace, got %q", m2.wsHint)
	}
}

func TestWorkspaceCreate_NKeyWithMembersOpensSessionNamePrompt(t *testing.T) {
	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		wsStatuses: []workspace.WorkspaceStatus{wsMemberWorkspace("payments", "/repo/a", "/repo/b")},
	}

	updated, _ := m.Update(keyMsg("n"))
	m2 := updated.(model)

	if m2.prompt != promptNewWorkspaceSession {
		t.Fatalf("members present must open promptNewWorkspaceSession, got %v", m2.prompt)
	}
	if m2.wsCreateTarget != "payments" {
		t.Errorf("wsCreateTarget must capture the workspace under the cursor, got %q", m2.wsCreateTarget)
	}
	if !m2.input.Focused() {
		t.Error("n must focus the input")
	}
}

func TestWorkspaceCreate_NKeyNoWorkspacesIsNoop(t *testing.T) {
	m := model{width: 120, height: 40, view: viewWorkspaces, input: newTestInput()}

	updated, cmd := m.Update(keyMsg("n"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("n with no workspaces must stay idle, got %v", m2.prompt)
	}
	if cmd != nil {
		t.Error("n with no workspaces must return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// Session-name prompt validation
// ---------------------------------------------------------------------------

func sessionNamePromptModel(workspaceName string, existingSessions ...string) model {
	sessions := make([]workspace.Session, 0, len(existingSessions))
	for _, n := range existingSessions {
		sessions = append(sessions, workspace.Session{Name: n})
	}
	return model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		prompt:         promptNewWorkspaceSession,
		wsCreateTarget: workspaceName,
		wsStatuses: []workspace.WorkspaceStatus{{
			Workspace: workspace.Workspace{Name: workspaceName, Members: []workspace.MemberRepo{{Path: "/repo/a"}}, Sessions: sessions},
		}},
	}
}

func TestWorkspaceCreate_SessionNamePromptEmptyNameCancels(t *testing.T) {
	m := sessionNamePromptModel("payments")
	m.input.SetValue("   ")

	updated, _ := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("empty name must cancel to promptIdle, got %v", m2.prompt)
	}
	if m2.wsCreateTarget != "" {
		t.Error("empty name must clear wsCreateTarget")
	}
}

func TestWorkspaceCreate_SessionNamePromptDuplicateNameRefusedAtPrompt(t *testing.T) {
	m := sessionNamePromptModel("payments", "feature-x")
	m.input.SetValue("feature-x")

	updated, _ := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptNewWorkspaceSession {
		t.Errorf("duplicate name must stay at promptNewWorkspaceSession, got %v", m2.prompt)
	}
	if m2.wsHint == "" || !strings.Contains(m2.wsHint, "feature-x") {
		t.Errorf("duplicate name must report a hint naming the session, got %q", m2.wsHint)
	}
}

func TestWorkspaceCreate_SessionNamePromptIllegalNameRefusedAtPrompt(t *testing.T) {
	m := sessionNamePromptModel("payments")
	m.input.SetValue("!!!") // slugifies to empty — no safe characters

	updated, _ := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptNewWorkspaceSession {
		t.Errorf("illegal name must stay at promptNewWorkspaceSession, got %v", m2.prompt)
	}
	if m2.wsHint == "" {
		t.Error("illegal name must set a wsHint explaining why")
	}
}

func TestWorkspaceCreate_SessionNamePromptValidNameOpensHarnessChooser(t *testing.T) {
	ops := &fakeHarnessOpsWithKinds{kinds: []harness.Kind{"codex", "opencode"}}
	m := sessionNamePromptModel("payments")
	m.harnOp = ops
	m.input.SetValue("Feature X")

	updated, _ := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptChooseHarness {
		t.Fatalf("valid name must open promptChooseHarness, got %v", m2.prompt)
	}
	if m2.wsCreateTarget != "payments" {
		t.Errorf("wsCreateTarget must be carried to the chooser, got %q", m2.wsCreateTarget)
	}
	if m2.wsCreateSessionName != "Feature X" {
		t.Errorf("wsCreateSessionName must carry the typed name, got %q", m2.wsCreateSessionName)
	}
}

func TestWorkspaceCreate_SessionNamePromptEscCancelsFlow(t *testing.T) {
	m := sessionNamePromptModel("payments")
	m.input.SetValue("feature-x")

	updated, cmd := m.Update(keyMsg("esc"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("esc must cancel to promptIdle, got %v", m2.prompt)
	}
	if m2.wsCreateTarget != "" || m2.wsCreateSessionName != "" {
		t.Error("esc must clear wsCreateTarget and wsCreateSessionName")
	}
	if cmd != nil {
		t.Error("esc must return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// promptChooseHarness — workspace-session branch (wsCreateTarget set)
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_HarnessChooserEnterDispatchesAssembleAndShowsSpinnerRow(t *testing.T) {
	setWsTestXDG(t)
	store := &fakeStoreOps{loadErr: errors.New("workspace \"payments\" does not exist")}
	tmuxFake := &fakeTmuxOps{}
	m := model{
		width: 120, height: 40, input: newTestInput(),
		prompt:               promptChooseHarness,
		wsCreateTarget:       "payments",
		wsCreateSessionName:  "Feature X",
		harnessChooserKinds:  []harness.Kind{"opencode"},
		harnessChooserCursor: 0,
		// A workspace-session create only ever starts from a workspace already
		// present in wsStatuses (wsUnderCursor reads from it to populate
		// wsCreateTarget in the first place), so the placeholder row has
		// somewhere to attach.
		wsStatuses: []workspace.WorkspaceStatus{wsMemberWorkspace("payments", "/repo/a")},
		store:      store,
		tmux:       tmuxFake,
	}

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)

	if m2.prompt != promptIdle {
		t.Errorf("enter must return to promptIdle, got %v", m2.prompt)
	}
	if m2.wsCreateTarget != "" || m2.wsCreateSessionName != "" {
		t.Error("enter must clear wsCreateTarget and wsCreateSessionName")
	}
	if !m2.spinnerActive {
		t.Error("dispatching an assemble must start the spinner ticker")
	}
	pc, ok := m2.wsPendingSessions[wsSessionKey("payments", "Feature X")]
	if !ok {
		t.Fatal("dispatching an assemble must record a pending workspace session")
	}
	if pc.workspace != "payments" || pc.session != "Feature X" {
		t.Errorf("unexpected pending entry: %+v", pc)
	}

	// The placeholder row's animated glyph must already be baked into
	// m.wsStatuses (rather than deferred to the next render), since
	// formatWsSessionRow has no access to model state.
	found := false
	for _, ws := range m2.wsStatuses {
		if ws.Workspace.Name != "payments" {
			continue
		}
		for _, s := range ws.Sessions {
			if s.Session.Name == "Feature X" && strings.Contains(s.Session.Branch, spinnerFrames[0]) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("placeholder session row with the spinner glyph not found in wsStatuses: %+v", m2.wsStatuses)
	}

	if cmd == nil {
		t.Fatal("enter must return a cmd")
	}
	if len(tmuxFake.ensureWindowCalls) != 0 || len(tmuxFake.selectCalls) != 0 {
		t.Error("creating a session must never touch tmux — that's step 12's 'enter' to launch")
	}

	result := wsSessionAssembledFrom(t, cmd)
	if result.workspaceName != "payments" || result.sessionName != "Feature X" {
		t.Errorf("result must carry workspace+session name, got %+v", result)
	}
	if result.err == nil {
		t.Fatal("expected an error: workspace does not exist in the (fake) store")
	}
	if len(tmuxFake.ensureWindowCalls) != 0 {
		t.Error("assembling a session must never touch tmux")
	}
}

func TestWorkspaceCreate_HarnessChooserEscClearsWsCreateTarget(t *testing.T) {
	m := model{
		width: 120, input: newTestInput(),
		prompt:              promptChooseHarness,
		wsCreateTarget:      "payments",
		wsCreateSessionName: "Feature X",
	}

	updated, _ := m.Update(keyMsg("esc"))
	m2 := updated.(model)

	if m2.wsCreateTarget != "" || m2.wsCreateSessionName != "" {
		t.Error("esc must clear wsCreateTarget and wsCreateSessionName")
	}
}

// ---------------------------------------------------------------------------
// wsSessionAssembledMsg handling
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_AssembledMsgSuccessClearsPendingAndReloads(t *testing.T) {
	m := model{width: 120}
	m.addPendingWsSession("payments", "Feature X")
	m.wsStatuses = injectPendingWsSessions(nil, m.wsPendingSessions, 0)

	updated, cmd := m.Update(wsSessionAssembledMsg{workspaceName: "payments", sessionName: "Feature X"})
	m2 := updated.(model)

	if _, ok := m2.wsPendingSessions[wsSessionKey("payments", "Feature X")]; ok {
		t.Error("success must clear the pending entry")
	}
	if !m2.wsBuilding {
		t.Error("success must dispatch a reload")
	}
	if cmd == nil {
		t.Fatal("success must return a reload cmd")
	}
}

func TestWorkspaceCreate_AssembledMsgFailureSetsHintAndClearsPending(t *testing.T) {
	m := model{width: 120}
	m.addPendingWsSession("payments", "Feature X")

	updated, cmd := m.Update(wsSessionAssembledMsg{
		workspaceName: "payments", sessionName: "Feature X",
		err: fmt.Errorf(`member repo "/repo/b": no such directory`),
	})
	m2 := updated.(model)

	if _, ok := m2.wsPendingSessions[wsSessionKey("payments", "Feature X")]; ok {
		t.Error("failure must clear the pending entry")
	}
	if m2.wsHint == "" || !strings.Contains(m2.wsHint, "/repo/b") {
		t.Errorf("failure must set a wsHint naming the failing repo, got %q", m2.wsHint)
	}
	if m2.wsBuilding {
		t.Error("failure must not dispatch a reload")
	}
	if cmd != nil {
		t.Error("failure must return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// Spinner animation
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_SpinnerTickAnimatesPendingSessionPlaceholder(t *testing.T) {
	m := model{width: 120, spinnerActive: true}
	m.wsStatuses = []workspace.WorkspaceStatus{wsMemberWorkspace("payments", "/repo/a")}
	m.addPendingWsSession("payments", "Feature X")
	m.wsStatuses = injectPendingWsSessions(m.wsStatuses, m.wsPendingSessions, 0)

	branchAt := func(mm model) string {
		for _, ws := range mm.wsStatuses {
			for _, s := range ws.Sessions {
				if s.Session.Name == "Feature X" {
					return s.Session.Branch
				}
			}
		}
		return ""
	}

	first := branchAt(m)
	if !strings.Contains(first, spinnerFrames[0]) {
		t.Fatalf("frame 0 must show the first glyph, got %q", first)
	}

	updated, _ := m.Update(spinnerTickMsg{})
	m2 := updated.(model)
	second := branchAt(m2)
	if second == first {
		t.Errorf("spinner tick must change the placeholder's rendered text; got %q both times", first)
	}
	if !strings.Contains(second, spinnerFrames[1]) {
		t.Errorf("frame 1 must show the second glyph, got %q", second)
	}
}

func TestWorkspaceCreate_SpinnerStopsWhenNoWsPendingSessionsRemain(t *testing.T) {
	m := model{width: 120, spinnerActive: true, pendingCreates: map[string]pendingCreate{}, pulling: map[string]bool{}}

	updated, cmd := m.Update(spinnerTickMsg{})
	m2 := updated.(model)

	if m2.spinnerActive {
		t.Error("spinner must stop when no pending creates, pulls, or ws sessions remain")
	}
	if cmd != nil {
		t.Error("a stopped spinner must not re-arm")
	}
}

// ---------------------------------------------------------------------------
// End-to-end: real git repos + real store
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_EndToEnd_OneWorktreePerMemberOnOneBranch(t *testing.T) {
	setWsTestXDG(t)

	repoA := newWsCmdTestRepo(t, "repo-a")
	repoB := newWsCmdTestRepo(t, "repo-b")

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

	storeOps := realStoreOps{store: store}
	tmuxFake := &fakeTmuxOps{}

	m := model{
		width: 120, height: 40, view: viewWorkspaces, input: newTestInput(),
		wsStatuses: []workspace.WorkspaceStatus{wsMemberWorkspace("payments", repoA, repoB)},
		store:      storeOps,
		tmux:       tmuxFake,
	}

	// 'n' -> session-name prompt.
	updated, _ := m.Update(keyMsg("n"))
	m = updated.(model)
	if m.prompt != promptNewWorkspaceSession {
		t.Fatalf("expected promptNewWorkspaceSession, got %v", m.prompt)
	}
	m.input.SetValue("Feature X")

	// enter -> harness chooser (no default harness configured).
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(model)
	if m.prompt != promptChooseHarness {
		t.Fatalf("expected promptChooseHarness, got %v", m.prompt)
	}

	// enter -> dispatch the real assemble.
	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected a non-nil cmd")
	}

	result := wsSessionAssembledFrom(t, cmd)
	if result.err != nil {
		t.Fatalf("AssembleSession/AddSession: %v", result.err)
	}

	wantBranch, err := workspace.SessionBranch("Feature X")
	if err != nil {
		t.Fatalf("SessionBranch: %v", err)
	}
	if result.session.Branch != wantBranch {
		t.Errorf("session.Branch = %q, want %q", result.session.Branch, wantBranch)
	}
	if len(result.session.Members) != 2 {
		t.Fatalf("expected 2 session members, got %d: %+v", len(result.session.Members), result.session.Members)
	}
	for _, sm := range result.session.Members {
		if _, err := os.Stat(sm.WorktreePath); err != nil {
			t.Errorf("member worktree %q does not exist: %v", sm.WorktreePath, err)
		}
	}

	// Persisted: reload from the real store and find the new session.
	loaded, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	ws, ok := findWorkspaceByName(loaded, "payments")
	if !ok {
		t.Fatal("workspace \"payments\" missing after create")
	}
	if len(ws.Sessions) != 1 || ws.Sessions[0].Name != "Feature X" {
		t.Fatalf("expected 1 persisted session named %q, got %+v", "Feature X", ws.Sessions)
	}

	if len(tmuxFake.ensureWindowCalls) != 0 || len(tmuxFake.selectCalls) != 0 {
		t.Error("creating a session must never touch tmux — no target exists until 'enter' launches it (step 12)")
	}
}

func TestWorkspaceCreate_EndToEnd_FailureNamesRepoAndLeavesNothingBehind(t *testing.T) {
	setWsTestXDG(t)

	repoA := newWsCmdTestRepo(t, "repo-a")
	missingRepo := filepath.Join(t.TempDir(), "does-not-exist")

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("payments"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	// AssembleSession validates member repos by resolving git.RepoRoot, which
	// requires the member to already be attached; attach both members
	// (one real, one missing) so the failure comes from AssembleSession
	// itself, not an earlier "workspace has no members" gate.
	if err := store.AttachRepo("payments", repoA); err != nil {
		t.Fatalf("AttachRepo repoA: %v", err)
	}
	if err := store.AttachRepo("payments", missingRepo); err != nil {
		t.Fatalf("AttachRepo missingRepo: %v", err)
	}

	cmd := assembleWorkspaceSessionCmd(realStoreOps{store: store}, "payments", "Feature X", "opencode")
	msg := runCmd(cmd)
	result, ok := msg.(wsSessionAssembledMsg)
	if !ok {
		t.Fatalf("expected wsSessionAssembledMsg, got %T", msg)
	}
	if result.err == nil {
		t.Fatal("expected an error: one member repo does not exist on disk")
	}
	// Compare basenames rather than full paths: AttachRepo/RepoRoot both
	// canonicalize (resolving ancestor symlinks, e.g. macOS's /var -> /private/var),
	// which can make the stored path differ textually from the raw missingRepo
	// string even though they name the same repo.
	if !strings.Contains(result.err.Error(), filepath.Base(missingRepo)) {
		t.Errorf("error must name the failing repo %q, got %q", missingRepo, result.err.Error())
	}

	loaded, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	ws, ok := findWorkspaceByName(loaded, "payments")
	if !ok {
		t.Fatal("workspace \"payments\" missing")
	}
	if len(ws.Sessions) != 0 {
		t.Errorf("a failed assemble must persist no session, got %+v", ws.Sessions)
	}

	sessionDir, err := workspace.SessionDir(mustResolveTestRoot(t), "payments", "Feature X")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	if _, statErr := os.Stat(sessionDir); statErr == nil {
		t.Errorf("a failed assemble must leave no session directory behind: %q exists", sessionDir)
	}
}

// mustResolveTestRoot resolves the workspace root under the test's XDG
// environment (set by setWsTestXDG in the caller), for asserting that a
// failed assemble left no session directory behind.
func mustResolveTestRoot(t *testing.T) string {
	t.Helper()
	cfg, err := settings.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	root, err := settings.ResolveWorkspaceRoot(cfg)
	if err != nil {
		t.Fatalf("ResolveWorkspaceRoot: %v", err)
	}
	return root
}
