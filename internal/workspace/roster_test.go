package workspace_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// withStateEnv sets XDG_STATE_HOME to dir for the duration of the test and
// restores the original value (or unsets it) on cleanup.
func withStateEnv(t *testing.T, dir string) {
	t.Helper()
	orig, had := os.LookupEnv("XDG_STATE_HOME")
	if err := os.Setenv("XDG_STATE_HOME", dir); err != nil {
		t.Fatalf("setenv XDG_STATE_HOME: %v", err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("XDG_STATE_HOME", orig)
		} else {
			os.Unsetenv("XDG_STATE_HOME")
		}
	})
}

// TestRoster_LoadSaveRoundTrip verifies that Save followed by Load returns the
// same entries and that no atomic-write temp file is left behind.
func TestRoster_LoadSaveRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	withStateEnv(t, tmp)

	// Create a real directory so the entry is not pruned on load.
	worktree := filepath.Join(tmp, "myrepo")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	// Use the canonical path as the map key (symlinks resolved, e.g. on macOS
	// /var/folders → /private/var/folders).
	canonicalWorktree, err := pathnorm.Canonical(worktree)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", worktree, err)
	}

	now := time.Now().Truncate(time.Millisecond)
	m := map[string]workspace.RosterEntry{
		canonicalWorktree: {
			Dir:          canonicalWorktree,
			Harness:      "opencode",
			SessionID:    "sess-abc",
			Title:        "my session",
			LastActivity: now,
		},
	}

	if err := workspace.Save(m); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No temp file should remain after a successful save.
	// The roster dir is under the canonical form of tmp.
	canonicalTmp, err := pathnorm.Canonical(tmp)
	if err != nil {
		t.Fatalf("Canonical(tmp): %v", err)
	}
	rosterDir := filepath.Join(canonicalTmp, "cogitator")
	entries, err := os.ReadDir(rosterDir)
	if err != nil {
		t.Fatalf("ReadDir roster dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file after Save: %s", e.Name())
		}
	}

	loaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(loaded))
	}
	got, ok := loaded[canonicalWorktree]
	if !ok {
		t.Fatalf("entry for %q not found after round-trip", canonicalWorktree)
	}
	if got.Harness != "opencode" {
		t.Errorf("Harness: got %q, want %q", got.Harness, "opencode")
	}
	if got.SessionID != "sess-abc" {
		t.Errorf("SessionID: got %q, want %q", got.SessionID, "sess-abc")
	}
	if got.Title != "my session" {
		t.Errorf("Title: got %q, want %q", got.Title, "my session")
	}
	if !got.LastActivity.Equal(now) {
		t.Errorf("LastActivity: got %v, want %v", got.LastActivity, now)
	}
}

// TestRoster_Load_NoFile verifies that Load returns an empty map when the
// roster file does not exist.
func TestRoster_Load_NoFile(t *testing.T) {
	tmp := t.TempDir()
	withStateEnv(t, tmp)

	m, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load with no file: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %d entries", len(m))
	}
}

// TestRoster_Load_PrunesMissingWorktrees verifies that entries whose worktree
// directory no longer exists on disk are pruned on load.
func TestRoster_Load_PrunesMissingWorktrees(t *testing.T) {
	tmp := t.TempDir()
	withStateEnv(t, tmp)

	// A path that does not exist on disk.
	absent := filepath.Join(tmp, "gone")
	// A path that does exist.
	present := filepath.Join(tmp, "here")
	if err := os.MkdirAll(present, 0o755); err != nil {
		t.Fatalf("mkdir present: %v", err)
	}
	canonicalPresent, err := pathnorm.Canonical(present)
	if err != nil {
		t.Fatalf("Canonical(present): %v", err)
	}

	now := time.Now()
	m := map[string]workspace.RosterEntry{
		absent: {
			Dir:          absent,
			Harness:      "opencode",
			LastActivity: now,
		},
		canonicalPresent: {
			Dir:          canonicalPresent,
			Harness:      "opencode",
			LastActivity: now,
		},
	}
	if err := workspace.Save(m); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := loaded[absent]; ok {
		t.Errorf("absent worktree %q should have been pruned", absent)
	}
	if _, ok := loaded[canonicalPresent]; !ok {
		t.Errorf("present worktree %q should be retained", canonicalPresent)
	}
}

// TestRecorder_TwoSnapshotsSameDirLatestWins feeds two snapshots for the same
// directory (increasing LastActivity) to the recorder and asserts that only
// one entry exists with the latest title, and no temp file is left behind.
func TestRecorder_TwoSnapshotsSameDirLatestWins(t *testing.T) {
	tmp := t.TempDir()
	withStateEnv(t, tmp)

	// Create a real directory so the entry survives Load's pruning.
	worktree := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	// The recorder stores entries under the canonical (symlink-resolved) path.
	// On macOS, t.TempDir() returns /var/folders/... which resolves to
	// /private/var/folders/... so we must look up by the canonical key.
	canonicalWorktree, err := pathnorm.Canonical(worktree)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", worktree, err)
	}

	t1 := time.Now().Add(-10 * time.Second).Truncate(time.Millisecond)
	t2 := t1.Add(5 * time.Second)

	snap1 := state.Snapshot{
		Sessions: []state.SessionView{
			{
				SessionID:    "sess-1",
				Title:        "first title",
				Directory:    worktree,
				LastActivity: t1,
				// ParentID empty → top-level session
			},
		},
	}
	snap2 := state.Snapshot{
		Sessions: []state.SessionView{
			{
				SessionID:    "sess-1",
				Title:        "second title",
				Directory:    worktree,
				LastActivity: t2,
			},
		},
	}

	// Feed snapshots directly into the recorder via a buffered channel so we
	// don't need a live store.
	snapCh := make(chan state.Snapshot, 4)
	snapCh <- snap1
	snapCh <- snap2

	// Close the channel so RunSync exits after draining both snapshots.
	close(snapCh)

	rec := workspace.NewRecorder()

	// RunSync drives the recorder synchronously in a goroutine; we wait for
	// it to finish via the done channel.
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.RunSync(snapCh)
	}()
	<-done

	// Verify the roster on disk.
	loaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load after recorder: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(loaded), loaded)
	}
	got, ok := loaded[canonicalWorktree]
	if !ok {
		t.Fatalf("entry for canonical key %q not found; map keys: %v", canonicalWorktree, mapKeys(loaded))
	}
	if got.Title != "second title" {
		t.Errorf("Title: got %q, want %q", got.Title, "second title")
	}
	if !got.LastActivity.Equal(t2) {
		t.Errorf("LastActivity: got %v, want %v", got.LastActivity, t2)
	}

	// No temp file should remain.
	canonicalTmp, err := pathnorm.Canonical(tmp)
	if err != nil {
		t.Fatalf("Canonical(tmp): %v", err)
	}
	rosterDir := filepath.Join(canonicalTmp, "cogitator")
	entries, err := os.ReadDir(rosterDir)
	if err != nil {
		t.Fatalf("ReadDir roster dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// TestRoster_AttentionPersistsAndReverts verifies three attention-related
// behaviours:
//
//	(a) A snapshot whose session has Attention "finished" is persisted with
//	    that label in the roster entry.
//	(b) A subsequent snapshot for the SAME dir+session with equal LastActivity
//	    but a different Attention (e.g. a view-driven revert to "inactive")
//	    still forces a write — the most-recent-activity guard is bypassed for
//	    attention-only changes on the same session.
//	(c) Loading a roster.json that was written by an older build (no
//	    "attention" field) yields entries with empty Attention and no error.
func TestRoster_AttentionPersistsAndReverts(t *testing.T) {
	tmp := t.TempDir()
	withStateEnv(t, tmp)

	worktree := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	canonicalWorktree, err := pathnorm.Canonical(worktree)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", worktree, err)
	}

	ts := time.Now().Truncate(time.Millisecond)

	// (a) Snapshot with Attention "finished" — should be persisted.
	snapFinished := state.Snapshot{
		Sessions: []state.SessionView{
			{
				SessionID:    "sess-x",
				Title:        "my work",
				Directory:    worktree,
				LastActivity: ts,
				Attention:    state.AttnFinished,
			},
		},
	}

	// (b) Same dir+session, same LastActivity, attention reverts to "inactive".
	snapReverted := state.Snapshot{
		Sessions: []state.SessionView{
			{
				SessionID:    "sess-x",
				Title:        "my work",
				Directory:    worktree,
				LastActivity: ts, // unchanged
				Attention:    state.AttnInactive,
			},
		},
	}

	snapCh := make(chan state.Snapshot, 4)
	snapCh <- snapFinished
	snapCh <- snapReverted
	close(snapCh)

	rec := workspace.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.RunSync(snapCh)
	}()
	<-done

	loaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := loaded[canonicalWorktree]
	if !ok {
		t.Fatalf("entry for %q not found", canonicalWorktree)
	}
	// After the revert snapshot the attention should be "inactive", not "finished".
	if got.Attention != string(state.AttnInactive) {
		t.Errorf("(b) Attention after revert: got %q, want %q", got.Attention, state.AttnInactive)
	}

	// (c) Load a roster.json written without the "attention" field.
	legacyJSON := `{"entries":[{"dir":"` + canonicalWorktree + `","harness":"opencode","lastActivity":"` + ts.Format(time.RFC3339Nano) + `"}]}`
	rosterPath, err := workspace.RosterPath()
	if err != nil {
		t.Fatalf("RosterPath: %v", err)
	}
	if err := os.WriteFile(rosterPath, []byte(legacyJSON), 0o644); err != nil {
		t.Fatalf("write legacy roster: %v", err)
	}
	legacyLoaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load legacy roster: %v", err)
	}
	legacyEntry, ok := legacyLoaded[canonicalWorktree]
	if !ok {
		t.Fatalf("legacy entry for %q not found", canonicalWorktree)
	}
	if legacyEntry.Attention != "" {
		t.Errorf("(c) legacy entry Attention: got %q, want empty string", legacyEntry.Attention)
	}
}

func TestRoster_ProviderPersistsWithStaleHarness(t *testing.T) {
	tmp := t.TempDir()
	withStateEnv(t, tmp)

	worktree := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	canonicalWorktree, err := pathnorm.Canonical(worktree)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", worktree, err)
	}

	ts := time.Now().Truncate(time.Millisecond)
	seed := map[string]workspace.RosterEntry{
		canonicalWorktree: {
			Dir:          canonicalWorktree,
			Harness:      "opencode",
			SessionID:    "sess-x",
			LastActivity: ts,
			Attention:    string(state.AttnFinished),
		},
	}
	if err := workspace.Save(seed); err != nil {
		t.Fatalf("Save seed: %v", err)
	}

	snapCh := make(chan state.Snapshot, 1)
	snapCh <- state.Snapshot{Sessions: []state.SessionView{{
		SessionID:    "sess-x",
		Directory:    worktree,
		Provider:     "codex",
		LastActivity: ts,
		Attention:    state.AttnFinished,
	}}}
	close(snapCh)

	rec := workspace.NewRecorder()
	rec.RunSync(snapCh)

	loaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := loaded[canonicalWorktree]
	if got.Harness != "opencode" {
		t.Fatalf("Harness = %q, want opencode", got.Harness)
	}
	if got.Provider != "codex" {
		t.Fatalf("Provider = %q, want codex", got.Provider)
	}
}

// TestRoster_AttentionFinishedPersists verifies that a snapshot with Attention
// "finished" is recorded with that label, and that a different session with a
// newer LastActivity still wins the dir slot (existing multi-session semantics
// are preserved).
func TestRoster_AttentionFinishedPersists(t *testing.T) {
	tmp := t.TempDir()
	withStateEnv(t, tmp)

	worktree := filepath.Join(tmp, "proj2")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	canonicalWorktree, err := pathnorm.Canonical(worktree)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", worktree, err)
	}

	t1 := time.Now().Truncate(time.Millisecond)
	t2 := t1.Add(5 * time.Second)

	// First snapshot: session A finishes.
	snapA := state.Snapshot{
		Sessions: []state.SessionView{
			{
				SessionID:    "sess-a",
				Title:        "session A",
				Directory:    worktree,
				LastActivity: t1,
				Attention:    state.AttnFinished,
			},
		},
	}
	// Second snapshot: a different session B with a newer LastActivity.
	snapB := state.Snapshot{
		Sessions: []state.SessionView{
			{
				SessionID:    "sess-b",
				Title:        "session B",
				Directory:    worktree,
				LastActivity: t2,
				Attention:    state.AttnActive,
			},
		},
	}

	snapCh := make(chan state.Snapshot, 4)
	snapCh <- snapA
	snapCh <- snapB
	close(snapCh)

	rec := workspace.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.RunSync(snapCh)
	}()
	<-done

	loaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := loaded[canonicalWorktree]
	if !ok {
		t.Fatalf("entry for %q not found", canonicalWorktree)
	}
	// Session B has the greater LastActivity and must win.
	if got.SessionID != "sess-b" {
		t.Errorf("SessionID: got %q, want %q", got.SessionID, "sess-b")
	}
	if got.Attention != string(state.AttnActive) {
		t.Errorf("Attention: got %q, want %q", got.Attention, state.AttnActive)
	}
}

// mapKeys returns the keys of m as a slice, for diagnostic messages.
func mapKeys(m map[string]workspace.RosterEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestRecorder_SubagentSessionsIgnored verifies that sessions with a non-empty
// ParentID (subagent sessions) are not recorded in the roster.
func TestRecorder_SubagentSessionsIgnored(t *testing.T) {
	tmp := t.TempDir()
	withStateEnv(t, tmp)

	worktree := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	snap := state.Snapshot{
		Sessions: []state.SessionView{
			{
				SessionID:    "sub-1",
				Title:        "subagent",
				Directory:    worktree,
				ParentID:     "parent-sess", // non-empty → subagent, must be ignored
				LastActivity: time.Now(),
			},
		},
	}

	snapCh := make(chan state.Snapshot, 4)
	snapCh <- snap
	close(snapCh)

	rec := workspace.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.RunSync(snapCh)
	}()
	<-done

	loaded, err := workspace.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected 0 entries (subagent should be ignored), got %d: %v", len(loaded), loaded)
	}
}
