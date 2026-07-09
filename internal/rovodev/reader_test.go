package rovodev_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/rovodev"
)

// canonicalDir returns the pathnorm.Canonical form of dir, failing the test on
// error. Used to build expected Dir values that match what the reader produces.
func canonicalDir(t *testing.T, dir string) string {
	t.Helper()
	c, err := pathnorm.Canonical(dir)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", dir, err)
	}
	return c
}

// writeSession creates a sessions/<id> directory with the given metadata.json
// and session_context.json contents (either may be empty to skip that file),
// then stamps both files' mtime to modTime.
func writeSession(t *testing.T, home, id, metaJSON, ctxJSON string, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(home, "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if metaJSON != "" {
		p := filepath.Join(dir, "metadata.json")
		if err := os.WriteFile(p, []byte(metaJSON), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	if ctxJSON != "" {
		p := filepath.Join(dir, "session_context.json")
		if err := os.WriteFile(p, []byte(ctxJSON), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
}

// TestReadSessions_Metadata verifies the common path: title and workspace_path
// come from the small metadata.json, id from the directory name, and
// LastActivity from file mtime.
func TestReadSessions_Metadata(t *testing.T) {
	home := t.TempDir()
	mod := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	writeSession(t, home,
		"3291a3ef-09a0-494e-b2d6-a53292702724",
		`{"title":"Verify TCS Activation ID Mapping","workspace_path":"/tmp/wt-a"}`,
		`{"id":"3291a3ef-09a0-494e-b2d6-a53292702724","timestamp":"2026-06-25T22:18:18.834763Z"}`,
		mod,
	)

	sessions, err := rovodev.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1: %+v", len(sessions), sessions)
	}
	s := sessions[0]
	if s.ID != "3291a3ef-09a0-494e-b2d6-a53292702724" {
		t.Errorf("ID = %q, want the directory name", s.ID)
	}
	if s.Title != "Verify TCS Activation ID Mapping" {
		t.Errorf("Title = %q", s.Title)
	}
	if want := canonicalDir(t, "/tmp/wt-a"); s.Dir != want {
		t.Errorf("Dir = %q, want %q", s.Dir, want)
	}
	if !s.LastActivity.Equal(mod) {
		t.Errorf("LastActivity = %v, want %v (file mtime)", s.LastActivity, mod)
	}
}

// TestReadSessions_ContextFallback verifies that a session directory without
// metadata.json falls back to session_context.json for the workspace path,
// initial-prompt title, and created timestamp.
func TestReadSessions_ContextFallback(t *testing.T) {
	home := t.TempDir()
	mod := time.Now().Add(-1 * time.Minute).Truncate(time.Second)
	writeSession(t, home,
		"c02e78d1-9ac7-420d-98c4-c6f189704aa3",
		"", // no metadata.json
		`{"id":"c02e78d1","workspace_path":"/tmp/wt-b","initial_prompt":"fix the pipeline\nsecond line","timestamp":"2026-06-03T22:19:10.899590Z"}`,
		mod,
	)

	sessions, err := rovodev.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if want := canonicalDir(t, "/tmp/wt-b"); s.Dir != want {
		t.Errorf("Dir = %q, want %q", s.Dir, want)
	}
	if s.Title != "fix the pipeline" {
		t.Errorf("Title = %q, want first line of initial_prompt", s.Title)
	}
	if s.Created.IsZero() {
		t.Error("Created is zero, want the context timestamp")
	}
}

// TestReadSessions_SortedByRecency verifies most-recent-first ordering and that
// non-session directories (no recognised files) are skipped.
func TestReadSessions_SortedByRecency(t *testing.T) {
	home := t.TempDir()
	old := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	recent := time.Now().Add(-1 * time.Minute).Truncate(time.Second)
	writeSession(t, home, "old", `{"title":"old","workspace_path":"/tmp/o"}`, "", old)
	writeSession(t, home, "recent", `{"title":"recent","workspace_path":"/tmp/r"}`, "", recent)
	// A directory with no session files must be ignored.
	if err := os.MkdirAll(filepath.Join(home, "sessions", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	sessions, err := rovodev.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2 (empty dir skipped)", len(sessions))
	}
	if sessions[0].ID != "recent" || sessions[1].ID != "old" {
		t.Errorf("order = [%q,%q], want [recent, old]", sessions[0].ID, sessions[1].ID)
	}
}

// TestReadSessions_AbsentHome verifies that an empty/missing home yields no
// sessions and no error.
func TestReadSessions_AbsentHome(t *testing.T) {
	sessions, err := rovodev.ReadSessions(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("got %d sessions, want 0", len(sessions))
	}
}
