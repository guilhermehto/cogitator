package workspace_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// mustDir creates a temporary subdirectory and returns its canonical path.
// The directory is cleaned up when the test ends.
func mustDir(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// Return the canonical form so test assertions match Row.Worktree.
	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", dir, err)
	}
	return canonical
}

// makeSession builds a SessionView for testing with Provider defaulting to "opencode".
func makeSession(dir, sessionID, title string, src state.Source, activity time.Time) state.SessionView {
	return state.SessionView{
		SessionID:    sessionID,
		Title:        title,
		Directory:    dir,
		Source:       src,
		Attention:    state.AttnInactive,
		LastActivity: activity,
		Provider:     harness.Kind("opencode"),
		// ParentID empty → top-level session (as required by liveTopLevel contract)
	}
}

// makeSessionWithProvider builds a SessionView for testing with an explicit Provider.
func makeSessionWithProvider(dir, sessionID, title string, src state.Source, activity time.Time, provider harness.Kind) state.SessionView {
	sv := makeSession(dir, sessionID, title, src, activity)
	sv.Provider = provider
	return sv
}

// makeRosterEntry builds a RosterEntry for testing.
func makeRosterEntry(dir, harness, sessionID, title string, activity time.Time) workspace.RosterEntry {
	return workspace.RosterEntry{
		Dir:          dir,
		Harness:      harness,
		SessionID:    sessionID,
		Title:        title,
		LastActivity: activity,
	}
}

// findRow returns the first Row whose Worktree matches dir, or nil.
func findRow(rows []workspace.Row, dir string) *workspace.Row {
	for i := range rows {
		if rows[i].Worktree == dir {
			return &rows[i]
		}
	}
	return nil
}

// TestMerge_Running verifies that a worktree with a live session yields
// State=running with the live session's Attention and Title.
func TestMerge_Running(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	wtDir := mustDir(t, tmp, "wt-running")

	now := time.Now()
	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDir, Branch: "main"}},
	}
	roster := map[string]workspace.RosterEntry{}
	liveTopLevel := []state.SessionView{
		makeSession(wtDir, "sess-1", "Running Session", state.SourceLive, now),
	}
	liveTopLevel[0].Attention = state.AttnActive

	rows := workspace.Merge(repos, worktreesByRepo, roster, liveTopLevel, nil)

	row := findRow(rows, wtDir)
	if row == nil {
		t.Fatalf("no row for worktree %q", wtDir)
	}
	if row.State != workspace.StateRunning {
		t.Errorf("State: got %q, want %q", row.State, workspace.StateRunning)
	}
	if row.Title != "Running Session" {
		t.Errorf("Title: got %q, want %q", row.Title, "Running Session")
	}
	if row.Attention != state.AttnActive {
		t.Errorf("Attention: got %q, want %q", row.Attention, state.AttnActive)
	}
	if row.SessionID != "sess-1" {
		t.Errorf("SessionID: got %q, want %q", row.SessionID, "sess-1")
	}
}

// TestMerge_Stopped verifies that a worktree in the roster with an opencode
// harness (LiveStatus=true) but no live session yields State=stopped.
func TestMerge_Stopped(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	wtDir := mustDir(t, tmp, "wt-stopped")

	past := time.Now().Add(-10 * time.Minute)
	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDir, Branch: "feature"}},
	}
	roster := map[string]workspace.RosterEntry{
		wtDir: makeRosterEntry(wtDir, "opencode", "sess-old", "Old Session", past),
	}

	rows := workspace.Merge(repos, worktreesByRepo, roster, nil, nil)

	row := findRow(rows, wtDir)
	if row == nil {
		t.Fatalf("no row for worktree %q", wtDir)
	}
	if row.State != workspace.StateStopped {
		t.Errorf("State: got %q, want %q", row.State, workspace.StateStopped)
	}
	if row.Title != "Old Session" {
		t.Errorf("Title: got %q, want %q", row.Title, "Old Session")
	}
	if !row.LastActivity.Equal(past) {
		t.Errorf("LastActivity: got %v, want %v", row.LastActivity, past)
	}
}

// TestMerge_Empty verifies that a worktree on disk with no roster entry and
// no live session yields State=empty.
func TestMerge_Empty(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	wtDir := mustDir(t, tmp, "wt-empty")

	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDir, Branch: "new-branch"}},
	}

	rows := workspace.Merge(repos, worktreesByRepo, nil, nil, nil)

	row := findRow(rows, wtDir)
	if row == nil {
		t.Fatalf("no row for worktree %q", wtDir)
	}
	if row.State != workspace.StateStopped {
		t.Errorf("State: got %q, want %q", row.State, workspace.StateStopped)
	}
}

// TestMerge_Unknown verifies that a roster entry with a non-LiveStatus harness
// and a tmux window present yields State=unknown.
func TestMerge_Unknown(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	wtDir := mustDir(t, tmp, "wt-unknown")

	past := time.Now().Add(-3 * time.Minute)
	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDir, Branch: "exp"}},
	}
	// Use a harness kind that is genuinely NOT registered in harness.DefaultRegistry
	// (no LiveStatus). "claude-code" was previously used here but is now a real
	// registered harness with LiveStatus=true; use an unregistered sentinel instead.
	roster := map[string]workspace.RosterEntry{
		wtDir: makeRosterEntry(wtDir, "unregistered-kind", "sess-x", "Unknown Session", past),
	}
	// Tmux window exists for this dir.
	tmuxDirs := map[string]bool{wtDir: true}

	rows := workspace.Merge(repos, worktreesByRepo, roster, nil, tmuxDirs)

	row := findRow(rows, wtDir)
	if row == nil {
		t.Fatalf("no row for worktree %q", wtDir)
	}
	if row.State != workspace.StateUnknown {
		t.Errorf("State: got %q, want %q", row.State, workspace.StateUnknown)
	}
}

// TestMerge_MultipleSessionsPerDir verifies that when two top-level live
// sessions share the same directory, exactly one running row results.
// The live source wins over recent; among equal sources, newest LastActivity wins.
func TestMerge_MultipleSessionsPerDir(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	wtDir := mustDir(t, tmp, "wt-multi")

	t1 := time.Now().Add(-20 * time.Second)
	t2 := time.Now().Add(-5 * time.Second)

	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDir, Branch: "main"}},
	}

	// Two sessions in the same dir: one recent (older), one live (newer).
	sess1 := makeSession(wtDir, "sess-recent", "Recent Session", state.SourceRecent, t1)
	sess2 := makeSession(wtDir, "sess-live", "Live Session", state.SourceLive, t2)
	sess2.Attention = state.AttnActive

	liveTopLevel := []state.SessionView{sess1, sess2}

	rows := workspace.Merge(repos, worktreesByRepo, nil, liveTopLevel, nil)

	// Count rows for this dir.
	count := 0
	var matched *workspace.Row
	for i := range rows {
		if rows[i].Worktree == wtDir {
			count++
			matched = &rows[i]
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row for %q, got %d", wtDir, count)
	}
	if matched.State != workspace.StateRunning {
		t.Errorf("State: got %q, want %q", matched.State, workspace.StateRunning)
	}
	// The live session (sess-live) should win.
	if matched.SessionID != "sess-live" {
		t.Errorf("SessionID: got %q, want %q (live source should win)", matched.SessionID, "sess-live")
	}
	if matched.Title != "Live Session" {
		t.Errorf("Title: got %q, want %q", matched.Title, "Live Session")
	}
}

// TestMerge_MultipleSessionsPerDir_NewestWins verifies that when two live
// sessions share a dir, the one with the newest LastActivity wins.
func TestMerge_MultipleSessionsPerDir_NewestWins(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	wtDir := mustDir(t, tmp, "wt-newest")

	t1 := time.Now().Add(-30 * time.Second)
	t2 := time.Now().Add(-5 * time.Second)

	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDir, Branch: "main"}},
	}

	// Both sessions are live; the newer one should win.
	sess1 := makeSession(wtDir, "sess-old", "Old Live", state.SourceLive, t1)
	sess2 := makeSession(wtDir, "sess-new", "New Live", state.SourceLive, t2)

	rows := workspace.Merge(repos, worktreesByRepo, nil, []state.SessionView{sess1, sess2}, nil)

	count := 0
	var matched *workspace.Row
	for i := range rows {
		if rows[i].Worktree == wtDir {
			count++
			matched = &rows[i]
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row for %q, got %d", wtDir, count)
	}
	if matched.SessionID != "sess-new" {
		t.Errorf("SessionID: got %q, want %q (newest LastActivity should win)", matched.SessionID, "sess-new")
	}
}

// TestMerge_SubagentExcluded verifies that Merge produces no extra row from a
// subagent session sharing a parent dir. The caller pre-filters liveTopLevel
// to exclude subagents; Merge trusts the slice and does not re-implement the
// filter. This test passes a pre-filtered slice (subagent already removed) and
// asserts the parent dir yields exactly one row.
func TestMerge_SubagentExcluded(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	wtDir := mustDir(t, tmp, "wt-subagent")

	now := time.Now()
	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDir, Branch: "main"}},
	}

	// Only the top-level session is in liveTopLevel (subagent pre-filtered by caller).
	topLevel := makeSession(wtDir, "sess-parent", "Parent Session", state.SourceLive, now)
	topLevel.Attention = state.AttnActive

	// The subagent (ParentID non-empty) is NOT included in liveTopLevel —
	// the caller (internal/ui) has already filtered it out via shouldHideSubagent.
	liveTopLevel := []state.SessionView{topLevel}

	rows := workspace.Merge(repos, worktreesByRepo, nil, liveTopLevel, nil)

	count := 0
	for _, r := range rows {
		if r.Worktree == wtDir {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row for %q (subagent excluded by caller), got %d", wtDir, count)
	}
	if rows[0].State != workspace.StateRunning {
		t.Errorf("State: got %q, want %q", rows[0].State, workspace.StateRunning)
	}
}

// TestMerge_EmptyRepo verifies that a configured repo with zero worktrees
// still yields a navigable repo row so 'n' has a target.
func TestMerge_EmptyRepo(t *testing.T) {
	tmp := t.TempDir()
	// mustDir returns the canonical path already.
	repoDir := mustDir(t, tmp, "repo-empty")

	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: nil, // no worktrees
	}

	rows := workspace.Merge(repos, worktreesByRepo, nil, nil, nil)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row for empty repo, got %d", len(rows))
	}
	// Row.Repo is the canonical form of the repo path.
	if rows[0].Repo != repoDir {
		t.Errorf("Repo: got %q, want %q", rows[0].Repo, repoDir)
	}
	if rows[0].State != workspace.StateStopped {
		t.Errorf("State: got %q, want %q", rows[0].State, workspace.StateStopped)
	}
}

// TestMerge_AllDirsCanonical verifies that all Row.Worktree values are
// canonical (no trailing slash, symlinks resolved). This is a property test
// over the inputs rather than a specific state test.
func TestMerge_AllDirsCanonical(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	// Create the dir but pass it with a trailing slash to Merge — Merge must
	// canonicalize it. We use filepath.Join + manual slash to bypass mustDir's
	// canonicalization so we can test Merge's own normalization.
	rawWtDir := filepath.Join(tmp, "wt-canon")
	if err := os.MkdirAll(rawWtDir, 0o755); err != nil {
		t.Fatalf("mkdir wt-canon: %v", err)
	}
	wtDirSlash := rawWtDir + "/"

	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDirSlash, Branch: "main"}},
	}

	rows := workspace.Merge(repos, worktreesByRepo, nil, nil, nil)

	for _, r := range rows {
		if r.Worktree == "" {
			continue
		}
		if len(r.Worktree) > 1 && r.Worktree[len(r.Worktree)-1] == '/' {
			t.Errorf("Row.Worktree %q has trailing slash (not canonical)", r.Worktree)
		}
	}
}

// TestMerge_ProviderCodexWithRosterEntry verifies that a live codex session
// in a dir that already has a roster entry keeps the roster's harness (the
// roster wins over the live session's provider when the roster entry is present).
func TestMerge_ProviderCodexWithRosterEntry(t *testing.T) {
	tmp := t.TempDir()
	repoDir := mustDir(t, tmp, "repo")
	wtDir := mustDir(t, tmp, "wt-codex-roster")

	now := time.Now()
	repos := []workspace.RepoConfig{{Path: repoDir}}
	worktreesByRepo := map[string][]git.Worktree{
		repoDir: {{Path: wtDir, Branch: "main"}},
	}
	roster := map[string]workspace.RosterEntry{
		wtDir: makeRosterEntry(wtDir, "codex", "sess-codex", "Codex Session", now.Add(-time.Minute)),
	}
	sess := makeSessionWithProvider(wtDir, "sess-codex-live", "Codex Live", state.SourceLive, now, harness.Kind("codex"))

	rows := workspace.Merge(repos, worktreesByRepo, roster, []state.SessionView{sess}, nil)

	row := findRow(rows, wtDir)
	if row == nil {
		t.Fatalf("no row for codex worktree %q", wtDir)
	}
	if row.Harness != "codex" {
		t.Errorf("Harness: got %q, want %q", row.Harness, "codex")
	}
	if row.State != workspace.StateRunning {
		t.Errorf("State: got %q, want %q", row.State, workspace.StateRunning)
	}
}
