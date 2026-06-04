package ui

// repofinder_test.go — unit tests for the embedded (in-TUI) add-repo finder:
// the pure helpers (filterConfigured, clampIndex), the scan/add Cmds, and the
// 'A' key + repoScanMsg/repoAddMsg Update wiring. No external process is ever
// launched; the finder runs entirely inside the Bubble Tea event loop.

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// errScanTest is a sentinel error used to drive the finder's error paths.
var errScanTest = errors.New("boom")

// initGitRepoForUI creates a temporary git repository and returns its path.
func initGitRepoForUI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// filterConfigured
// ---------------------------------------------------------------------------

func TestFilterConfigured_DropsAlreadyConfigured(t *testing.T) {
	got := filterConfigured(
		[]string{"/a", "/b", "/c"},
		[]workspace.RepoConfig{{Path: "/b"}},
	)
	want := []string{"/a", "/c"}
	if len(got) != len(want) || got[0] != "/a" || got[1] != "/c" {
		t.Errorf("filterConfigured = %v, want %v", got, want)
	}
}

func TestFilterConfigured_EmptyConfigReturnsAll(t *testing.T) {
	in := []string{"/a", "/b"}
	got := filterConfigured(in, nil)
	if len(got) != 2 {
		t.Errorf("empty config should return all; got %v", got)
	}
}

// ---------------------------------------------------------------------------
// clampIndex
// ---------------------------------------------------------------------------

func TestClampIndex(t *testing.T) {
	cases := []struct{ i, n, want int }{
		{0, 0, 0},
		{5, 0, 0},  // empty list pins to 0
		{-1, 3, 0}, // below range
		{1, 3, 1},  // in range
		{3, 3, 2},  // at/above range clamps to last
		{99, 3, 2},
	}
	for _, c := range cases {
		if got := clampIndex(c.i, c.n); got != c.want {
			t.Errorf("clampIndex(%d, %d) = %d, want %d", c.i, c.n, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// scanReposCmd / addSelectedRepoCmd
// ---------------------------------------------------------------------------

func TestScanReposCmd_FindsRepoAndFiltersConfigured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	if err := exec.Command("mkdir", "-p", filepath.Join(repo, ".git")).Run(); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	want, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}

	msg := scanReposCmd(root)().(repoScanMsg)
	if msg.err != nil {
		t.Fatalf("scan err: %v", msg.err)
	}
	if !containsStr(msg.repos, want) {
		t.Errorf("scan should find %q; got %v", want, msg.repos)
	}

	// With the repo already configured, the scan must filter it out.
	if _, err := workspace.AddRepo(want); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	msg2 := scanReposCmd(root)().(repoScanMsg)
	if containsStr(msg2.repos, want) {
		t.Errorf("configured repo must be filtered from scan; got %v", msg2.repos)
	}
}

func TestAddSelectedRepoCmd_AddsThenDedups(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := initGitRepoForUI(t)
	want, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}

	msg := addSelectedRepoCmd(repo)().(repoAddMsg)
	if !msg.added || msg.addErr != nil || msg.repoPath != want {
		t.Fatalf("first add: unexpected %+v (want added, path %q)", msg, want)
	}

	msg2 := addSelectedRepoCmd(repo)().(repoAddMsg)
	if msg2.added || msg2.addErr != nil {
		t.Errorf("second add should be a dedup no-op; got %+v", msg2)
	}
}

func TestAddSelectedRepoCmd_NotRepoErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	msg := addSelectedRepoCmd(t.TempDir())().(repoAddMsg)
	if msg.addErr == nil {
		t.Fatalf("expected addErr for non-git dir; got %+v", msg)
	}
	if msg.added {
		t.Errorf("non-repo must not be added")
	}
}

// ---------------------------------------------------------------------------
// 'A' key + finder open
// ---------------------------------------------------------------------------

func TestAddRepoKey_OpensFinderAndScans(t *testing.T) {
	m := model{width: 120, input: textinput.New()}
	updated, cmd := m.Update(keyMsg("A"))
	m2 := updated.(model)
	if m2.prompt != promptAddRepo {
		t.Errorf("'A' must open the repo finder (prompt=promptAddRepo); got %v", m2.prompt)
	}
	if !m2.repoFinderScanning {
		t.Errorf("'A' must mark the finder as scanning")
	}
	if cmd == nil {
		t.Errorf("'A' must return a cmd (focus + scan batch)")
	}
}

// 'a' must remain the recent toggle, not the repo finder.
func TestAddRepoLowercaseStillTogglesRecent(t *testing.T) {
	m := model{width: 120, recentCollapsed: true}
	updated, _ := m.Update(keyMsg("a"))
	if updated.(model).recentCollapsed {
		t.Errorf("'a' must still toggle recentCollapsed to false")
	}
}

// ---------------------------------------------------------------------------
// repoScanMsg handling
// ---------------------------------------------------------------------------

func TestRepoScanMsg_PopulatesMatchesWhenFinderOpen(t *testing.T) {
	m := model{width: 120, input: textinput.New(), prompt: promptAddRepo, repoFinderScanning: true}
	updated, _ := m.Update(repoScanMsg{repos: []string{"/home/me/a", "/home/me/b"}})
	m2 := updated.(model)
	if m2.repoFinderScanning {
		t.Errorf("scan result must clear scanning flag")
	}
	if len(m2.repoFinderMatches) != 2 {
		t.Errorf("expected 2 matches (empty query), got %v", m2.repoFinderMatches)
	}
}

func TestRepoScanMsg_IgnoredWhenFinderClosed(t *testing.T) {
	m := model{width: 120, prompt: promptIdle}
	updated, _ := m.Update(repoScanMsg{repos: []string{"/x"}})
	if got := updated.(model).repoFinderMatches; got != nil {
		t.Errorf("stale scan result must be ignored when finder is closed; got %v", got)
	}
}

func TestRepoScanMsg_ErrorSetsFinderErr(t *testing.T) {
	m := model{width: 120, input: textinput.New(), prompt: promptAddRepo, repoFinderScanning: true}
	updated, _ := m.Update(repoScanMsg{err: errScanTest})
	m2 := updated.(model)
	if !strings.Contains(m2.repoFinderErr, "scan failed") {
		t.Errorf("expected scan-failed error, got %q", m2.repoFinderErr)
	}
}

// ---------------------------------------------------------------------------
// finder key handling: filter, navigate, select, cancel
// ---------------------------------------------------------------------------

func TestFinder_TypingFiltersMatches(t *testing.T) {
	m := model{width: 120, prompt: promptAddRepo, input: textinput.New()}
	m.input.Focus()
	m.repoFinderAll = []string{"/home/me/cogitator", "/home/me/notes"}
	m.repoFinderMatches = m.repoFinderAll

	updated, _ := m.Update(keyMsg("c"))
	m2 := updated.(model)
	if len(m2.repoFinderMatches) != 1 || !strings.Contains(m2.repoFinderMatches[0], "cogitator") {
		t.Errorf("typing 'c' should filter to cogitator; got %v", m2.repoFinderMatches)
	}
}

func TestFinder_NavigationClamps(t *testing.T) {
	m := model{width: 120, prompt: promptAddRepo, input: textinput.New()}
	m.repoFinderMatches = []string{"/a", "/b"}
	m.repoFinderCursor = 0

	// Up at the top stays at 0.
	up, _ := m.Update(keyMsg("up"))
	if c := up.(model).repoFinderCursor; c != 0 {
		t.Errorf("up at top: cursor = %d, want 0", c)
	}
	// Down moves to 1; a second down clamps at the last index.
	down, _ := m.Update(keyMsg("down"))
	m = down.(model)
	if m.repoFinderCursor != 1 {
		t.Fatalf("down: cursor = %d, want 1", m.repoFinderCursor)
	}
	down2, _ := m.Update(keyMsg("down"))
	if c := down2.(model).repoFinderCursor; c != 1 {
		t.Errorf("down past end: cursor = %d, want 1 (clamped)", c)
	}
}

func TestFinder_EnterSelectsAndCloses(t *testing.T) {
	m := model{width: 120, prompt: promptAddRepo, input: textinput.New()}
	m.repoFinderMatches = []string{"/home/me/proj"}
	m.repoFinderCursor = 0

	updated, cmd := m.Update(keyMsg("enter"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("enter must close the finder; prompt = %v", m2.prompt)
	}
	if cmd == nil {
		t.Errorf("enter must dispatch the add cmd")
	}
}

func TestFinder_EnterWithNoMatchesNoop(t *testing.T) {
	m := model{width: 120, prompt: promptAddRepo, input: textinput.New()}
	m.repoFinderMatches = nil
	updated, cmd := m.Update(keyMsg("enter"))
	if cmd != nil {
		t.Errorf("enter with no matches must not dispatch a cmd")
	}
	if updated.(model).prompt != promptAddRepo {
		t.Errorf("enter with no matches must keep the finder open")
	}
}

func TestFinder_EscCloses(t *testing.T) {
	m := model{width: 120, prompt: promptAddRepo, input: textinput.New()}
	m.repoFinderAll = []string{"/a"}
	m.repoFinderMatches = []string{"/a"}

	updated, _ := m.Update(keyMsg("esc"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("esc must close the finder; prompt = %v", m2.prompt)
	}
	if m2.repoFinderAll != nil || m2.repoFinderMatches != nil {
		t.Errorf("esc must reset finder state; all=%v matches=%v", m2.repoFinderAll, m2.repoFinderMatches)
	}
}

// ---------------------------------------------------------------------------
// repoAddMsg handling
// ---------------------------------------------------------------------------

func TestRepoAddMsg_AddedSetsHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := model{width: 120}
	updated, _ := m.Update(repoAddMsg{added: true, repoPath: "/home/me/myrepo"})
	if got := updated.(model).tmuxHint; got != "added repo: myrepo" {
		t.Errorf("hint: got %q, want %q", got, "added repo: myrepo")
	}
}

func TestRepoAddMsg_DuplicateSetsHint(t *testing.T) {
	m := model{width: 120}
	updated, _ := m.Update(repoAddMsg{added: false, repoPath: "/home/me/myrepo"})
	if got := updated.(model).tmuxHint; got != "repo already configured: myrepo" {
		t.Errorf("hint: got %q, want %q", got, "repo already configured: myrepo")
	}
}

func TestRepoAddMsg_ErrorSetsHint(t *testing.T) {
	m := model{width: 120}
	updated, _ := m.Update(repoAddMsg{addErr: errScanTest, repoPath: "/home/me/x"})
	if got := updated.(model).tmuxHint; !strings.Contains(got, "add repo failed") {
		t.Errorf("hint: got %q, want add-repo-failed", got)
	}
}

// ---------------------------------------------------------------------------
// removeRepoCmd
// ---------------------------------------------------------------------------

func TestRemoveRepoCmd_RemovesThenNoop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := initGitRepoForUI(t)
	want, err := pathnorm.Canonical(repo)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if _, err := workspace.AddRepo(repo); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	msg := removeRepoCmd(want)().(repoRemoveMsg)
	if !msg.removed || msg.removeErr != nil || msg.repoPath != want {
		t.Fatalf("first remove: unexpected %+v (want removed, path %q)", msg, want)
	}

	// Second remove of the now-untracked path is a no-op.
	msg2 := removeRepoCmd(want)().(repoRemoveMsg)
	if msg2.removed || msg2.removeErr != nil {
		t.Errorf("second remove should be a no-op; got %+v", msg2)
	}
}

// ---------------------------------------------------------------------------
// 'R' key + confirmation flow
// ---------------------------------------------------------------------------

func TestRemoveRepoKey_OpensConfirm(t *testing.T) {
	m := model{
		width:         120,
		workspaceRows: []workspace.Row{{Repo: "/home/me/myrepo", Worktree: "/home/me/myrepo"}},
	}
	updated, cmd := m.Update(keyMsg("R"))
	m2 := updated.(model)
	if m2.prompt != promptConfirmRemoveRepo {
		t.Errorf("'R' must open the remove confirmation; got prompt %v", m2.prompt)
	}
	if m2.removeRepoTarget != "/home/me/myrepo" {
		t.Errorf("removeRepoTarget: got %q, want %q", m2.removeRepoTarget, "/home/me/myrepo")
	}
	if cmd != nil {
		t.Errorf("'R' must not dispatch a cmd until confirmed")
	}
}

func TestRemoveRepoKey_NoRowsNoop(t *testing.T) {
	m := model{width: 120}
	updated, _ := m.Update(keyMsg("R"))
	if updated.(model).prompt != promptIdle {
		t.Errorf("'R' with no rows must stay idle")
	}
}

func TestRemoveRepoConfirm_YDispatches(t *testing.T) {
	m := model{width: 120, prompt: promptConfirmRemoveRepo, removeRepoTarget: "/home/me/myrepo"}
	updated, cmd := m.Update(keyMsg("y"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("'y' must close the confirmation; got prompt %v", m2.prompt)
	}
	if m2.removeRepoTarget != "" {
		t.Errorf("'y' must clear removeRepoTarget; got %q", m2.removeRepoTarget)
	}
	if cmd == nil {
		t.Errorf("'y' must dispatch the remove cmd")
	}
}

func TestRemoveRepoConfirm_OtherKeyCancels(t *testing.T) {
	m := model{width: 120, prompt: promptConfirmRemoveRepo, removeRepoTarget: "/home/me/myrepo"}
	updated, cmd := m.Update(keyMsg("n"))
	m2 := updated.(model)
	if m2.prompt != promptIdle {
		t.Errorf("any other key must cancel; got prompt %v", m2.prompt)
	}
	if m2.removeRepoTarget != "" {
		t.Errorf("cancel must clear removeRepoTarget; got %q", m2.removeRepoTarget)
	}
	if cmd != nil {
		t.Errorf("cancel must not dispatch a cmd")
	}
}

// ---------------------------------------------------------------------------
// repoRemoveMsg handling
// ---------------------------------------------------------------------------

func TestRepoRemoveMsg_RemovedSetsHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := model{width: 120}
	updated, _ := m.Update(repoRemoveMsg{removed: true, repoPath: "/home/me/myrepo"})
	if got := updated.(model).tmuxHint; got != "removed repo: myrepo" {
		t.Errorf("hint: got %q, want %q", got, "removed repo: myrepo")
	}
}

func TestRepoRemoveMsg_NotTrackedSetsHint(t *testing.T) {
	m := model{width: 120}
	updated, _ := m.Update(repoRemoveMsg{removed: false, repoPath: "/home/me/myrepo"})
	if got := updated.(model).tmuxHint; got != "repo not tracked: myrepo" {
		t.Errorf("hint: got %q, want %q", got, "repo not tracked: myrepo")
	}
}

func TestRepoRemoveMsg_ErrorSetsHint(t *testing.T) {
	m := model{width: 120}
	updated, _ := m.Update(repoRemoveMsg{removeErr: errScanTest, repoPath: "/home/me/x"})
	if got := updated.(model).tmuxHint; !strings.Contains(got, "remove repo failed") {
		t.Errorf("hint: got %q, want remove-repo-failed", got)
	}
}
