package workspace_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// workspacesPath returns the path Store writes to under the current
// $XDG_CONFIG_HOME, mirroring how Store derives it from settings.ConfigPath.
func workspacesPath(t *testing.T) string {
	t.Helper()
	configPath, err := settings.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	return filepath.Join(filepath.Dir(configPath), "workspaces.json")
}

// TestLoadWorkspaces_NoFile verifies that LoadWorkspaces returns an empty set
// and no error when workspaces.json does not exist yet.
func TestLoadWorkspaces_NoFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	workspaces, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces with no file: %v", err)
	}
	if len(workspaces) != 0 {
		t.Errorf("expected 0 workspaces, got %d", len(workspaces))
	}
}

// TestLoadWorkspaces_TwoWorkspacesWithSessions verifies that a hand-edited
// workspaces.json listing two workspaces loads both, with member repo paths
// canonicalized, and their sessions attached.
func TestLoadWorkspaces_TwoWorkspacesWithSessions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	repoA := filepath.Join(tmp, "repo-a")
	repoB := filepath.Join(tmp, "repo-b")
	for _, d := range []string{repoA, repoB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	canonicalA, err := pathnorm.Canonical(repoA)
	if err != nil {
		t.Fatalf("Canonical(repoA): %v", err)
	}
	canonicalB, err := pathnorm.Canonical(repoB)
	if err != nil {
		t.Fatalf("Canonical(repoB): %v", err)
	}

	// Use a dirty (non-canonical) form for one member path to prove Load
	// canonicalizes it rather than passing it through verbatim.
	dirtyA := repoA + string(filepath.Separator) + "." + string(filepath.Separator)

	cfgDir := filepath.Join(tmp, "cogitator")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	raw := fmt.Sprintf(`{
		"workspaces": [
			{
				"name": "Alpha",
				"members": [{"path": %q}],
				"sessions": [{"name": "s1", "dir": "/tmp/alpha-s1", "branch": "s1", "harness": "opencode", "members": []}]
			},
			{
				"name": "Beta",
				"members": [{"path": %q}],
				"sessions": []
			}
		]
	}`, dirtyA, repoB)
	if err := os.WriteFile(filepath.Join(cfgDir, "workspaces.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write workspaces.json: %v", err)
	}

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	workspaces, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(workspaces))
	}

	alpha, beta := workspaces[0], workspaces[1]
	if alpha.Name != "Alpha" || beta.Name != "Beta" {
		t.Fatalf("unexpected workspace names: %q, %q", alpha.Name, beta.Name)
	}
	if len(alpha.Sessions) != 1 || alpha.Sessions[0].Name != "s1" {
		t.Errorf("expected Alpha to have session %q, got %+v", "s1", alpha.Sessions)
	}
	if len(beta.Sessions) != 0 {
		t.Errorf("expected Beta to have 0 sessions, got %d", len(beta.Sessions))
	}
	if alpha.Members[0].Path != canonicalA {
		t.Errorf("Alpha member path = %q, want canonical %q", alpha.Members[0].Path, canonicalA)
	}
	if beta.Members[0].Path != canonicalB {
		t.Errorf("Beta member path = %q, want canonical %q", beta.Members[0].Path, canonicalB)
	}
	if alpha.Members[0].Missing || beta.Members[0].Missing {
		t.Error("member repos that exist on disk should not be flagged Missing")
	}
}

// TestLoadWorkspaces_FlagsMissingMember verifies that a member repo absent
// from disk is flagged Missing rather than failing the load, and the
// workspace is still returned.
func TestLoadWorkspaces_FlagsMissingMember(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	absentPath := filepath.Join(tmp, "does-not-exist")

	cfgDir := filepath.Join(tmp, "cogitator")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	raw := fmt.Sprintf(`{"workspaces": [{"name": "Alpha", "members": [{"path": %q}], "sessions": []}]}`, absentPath)
	if err := os.WriteFile(filepath.Join(cfgDir, "workspaces.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write workspaces.json: %v", err)
	}

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	workspaces, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces with absent member: %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(workspaces))
	}
	if len(workspaces[0].Members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(workspaces[0].Members))
	}
	if !workspaces[0].Members[0].Missing {
		t.Error("expected absent member repo to be flagged Missing")
	}
}

// TestStore_WorkspacesFileBesideConfig verifies that SaveWorkspaces writes
// workspaces.json in the same directory as config.json.
func TestStore_WorkspacesFileBesideConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SaveWorkspaces([]workspace.Workspace{{Name: "demo"}}); err != nil {
		t.Fatalf("SaveWorkspaces: %v", err)
	}

	configPath, err := settings.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	want := filepath.Join(filepath.Dir(configPath), "workspaces.json")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected workspaces.json at %s: %v", want, err)
	}
}

// TestAddWorkspace_RejectsDuplicateName verifies that adding a workspace whose
// name already exists is rejected with an error naming the conflict.
func TestAddWorkspace_RejectsDuplicateName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("demo"); err != nil {
		t.Fatalf("AddWorkspace(first): %v", err)
	}

	_, err = store.AddWorkspace("demo")
	if err == nil {
		t.Fatal("expected an error adding a duplicate workspace name")
	}
	if !strings.Contains(err.Error(), "demo") {
		t.Errorf("error %q does not name the conflicting workspace", err.Error())
	}
}

// TestAddSession_RejectsDuplicateNameWithinWorkspace verifies that adding a
// session whose name already exists in the workspace is rejected with an
// error naming the conflict.
func TestAddSession_RejectsDuplicateNameWithinWorkspace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("demo"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := store.AddSession("demo", workspace.Session{Name: "feature"}); err != nil {
		t.Fatalf("AddSession(first): %v", err)
	}

	err = store.AddSession("demo", workspace.Session{Name: "feature"})
	if err == nil {
		t.Fatal("expected an error adding a duplicate session name")
	}
	if !strings.Contains(err.Error(), "feature") {
		t.Errorf("error %q does not name the conflicting session", err.Error())
	}
}

// TestStore_ConcurrentAddSession_BothPersist verifies that two goroutines
// each adding a different session to the same workspace concurrently both
// land, and the resulting file still parses. Run with -race.
func TestStore_ConcurrentAddSession_BothPersist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("demo"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	sessions := []string{"s1", "s2"}
	for _, name := range sessions {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			errs <- store.AddSession("demo", workspace.Session{Name: name, Branch: name})
		}(name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("AddSession: %v", err)
		}
	}

	workspaces, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(workspaces))
	}
	if len(workspaces[0].Sessions) != 2 {
		t.Fatalf("expected 2 sessions after concurrent adds, got %d", len(workspaces[0].Sessions))
	}

	data, err := os.ReadFile(workspacesPath(t))
	if err != nil {
		t.Fatalf("read workspaces.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Errorf("workspaces.json does not parse after concurrent writes: %v", err)
	}
}

// TestSaveWorkspaces_FailedWriteLeavesPreviousFileValid verifies that when a
// save cannot complete (simulating an interrupted write), the previously
// saved workspaces.json is untouched and still parses. The temp-file+rename
// pattern in Store.save guarantees this: a save that cannot create or write
// its temp file never reaches the rename step.
func TestSaveWorkspaces_FailedWriteLeavesPreviousFileValid(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("demo"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}

	path := workspacesPath(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workspaces.json before failed save: %v", err)
	}

	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if err := store.SaveWorkspaces([]workspace.Workspace{{Name: "should-not-land"}}); err == nil {
		t.Fatal("expected SaveWorkspaces to fail against a read-only directory")
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("restore dir permissions: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workspaces.json after failed save: %v", err)
	}
	if string(after) != string(before) {
		t.Error("previous workspaces.json was modified by the failed save")
	}
	var parsed map[string]any
	if err := json.Unmarshal(after, &parsed); err != nil {
		t.Errorf("previous workspaces.json no longer parses: %v", err)
	}
}

// TestAttachRepo_AddsCanonicalMemberAndRejectsDuplicate verifies AttachRepo
// canonicalizes the given path, appends it once, and rejects a repeat.
func TestAttachRepo_AddsCanonicalMemberAndRejectsDuplicate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	canonical, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("Canonical(repo): %v", err)
	}

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("demo"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := store.AttachRepo("demo", repo); err != nil {
		t.Fatalf("AttachRepo: %v", err)
	}

	workspaces, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	if len(workspaces[0].Members) != 1 || workspaces[0].Members[0].Path != canonical {
		t.Fatalf("expected 1 member %q, got %+v", canonical, workspaces[0].Members)
	}

	if err := store.AttachRepo("demo", repo); err == nil {
		t.Error("expected an error attaching an already-attached repo")
	}
}

// TestDetachRepo_RemovesMemberAndErrorsWhenNotMember verifies DetachRepo
// removes an existing member and errors on a repo that is not a member.
func TestDetachRepo_RemovesMemberAndErrorsWhenNotMember(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("demo"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := store.AttachRepo("demo", repo); err != nil {
		t.Fatalf("AttachRepo: %v", err)
	}
	if err := store.DetachRepo("demo", repo); err != nil {
		t.Fatalf("DetachRepo: %v", err)
	}

	workspaces, err := store.LoadWorkspaces()
	if err != nil {
		t.Fatalf("LoadWorkspaces: %v", err)
	}
	if len(workspaces[0].Members) != 0 {
		t.Fatalf("expected 0 members after DetachRepo, got %d", len(workspaces[0].Members))
	}

	if err := store.DetachRepo("demo", repo); err == nil {
		t.Error("expected an error detaching a repo that is not a member")
	}
}

// TestRemoveWorkspace_ErrorsWhenMissing verifies RemoveWorkspace reports an
// error for a workspace name that does not exist.
func TestRemoveWorkspace_ErrorsWhenMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.RemoveWorkspace("ghost"); err == nil {
		t.Error("expected an error removing a nonexistent workspace")
	}
}

// TestRemoveSession_ErrorsWhenMissing verifies RemoveSession reports an error
// for a session name that does not exist in an existing workspace.
func TestRemoveSession_ErrorsWhenMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	store, err := workspace.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AddWorkspace("demo"); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := store.RemoveSession("demo", "ghost"); err == nil {
		t.Error("expected an error removing a nonexistent session")
	}
}
