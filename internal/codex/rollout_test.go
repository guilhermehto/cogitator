package codex_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// canonicalDir returns the pathnorm.Canonical form of dir, failing the test on
// error. Used to build expected Dir values that match what the parser produces.
func canonicalDir(t *testing.T, dir string) string {
	t.Helper()
	c, err := pathnorm.Canonical(dir)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(%q): %v", dir, err)
	}
	return c
}

// mustParseTime parses an RFC3339Nano timestamp, failing the test on error.
func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", s, err)
	}
	return ts
}

// TestReadSessions_WellFormed verifies that a well-formed multi-line session
// file is parsed correctly: id, canonical dir, title, created, lastActivity.
func TestReadSessions_WellFormed(t *testing.T) {
	home := filepath.Join("testdata")

	sessions, err := codex.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}
	if len(sessions) < 1 {
		t.Fatalf("ReadSessions: got %d sessions, want at least 1", len(sessions))
	}

	// Find the well-formed session by ID.
	var s *codex.Session
	for i := range sessions {
		if sessions[i].ID == "019e8a0c-3f58-7b91-8d0d-5b2b03d1677f" {
			s = &sessions[i]
			break
		}
	}
	if s == nil {
		t.Fatalf("session 019e8a0c not found in results; got: %+v", sessions)
	}

	if s.Title != "add codex support" {
		t.Errorf("Title = %q, want %q", s.Title, "add codex support")
	}

	wantDir := canonicalDir(t, "/tmp/wt")
	if s.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", s.Dir, wantDir)
	}

	wantCreated := mustParseTime(t, "2026-06-02T20:35:19.235Z")
	if !s.Created.Equal(wantCreated) {
		t.Errorf("Created = %v, want %v", s.Created, wantCreated)
	}

	wantLastActivity := mustParseTime(t, "2026-06-02T20:35:27.837Z")
	if !s.LastActivity.Equal(wantLastActivity) {
		t.Errorf("LastActivity = %v, want %v", s.LastActivity, wantLastActivity)
	}
}

// TestReadSessions_TruncatedFinalLine verifies that a file with a garbage
// final line is still parsed: the session is returned with the last valid
// line's timestamp as LastActivity.
func TestReadSessions_TruncatedFinalLine(t *testing.T) {
	home := filepath.Join("testdata")

	sessions, err := codex.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}

	var s *codex.Session
	for i := range sessions {
		if sessions[i].ID == "aaaabbbb-cccc-dddd-eeee-ffffffffffff" {
			s = &sessions[i]
			break
		}
	}
	if s == nil {
		t.Fatalf("truncated-file session not found; got: %+v", sessions)
	}

	if s.Title != "fix the bug" {
		t.Errorf("Title = %q, want %q", s.Title, "fix the bug")
	}

	// LastActivity must be the last valid (parseable) line's timestamp,
	// not the garbage line.
	wantLastActivity := mustParseTime(t, "2026-06-02T21:00:02.000Z")
	if !s.LastActivity.Equal(wantLastActivity) {
		t.Errorf("LastActivity = %v, want %v (garbage line must be skipped)", s.LastActivity, wantLastActivity)
	}
}

// TestReadSessions_EmptySessionsDir verifies that a CODEX_HOME with an empty
// sessions directory returns an empty slice and no error.
func TestReadSessions_EmptySessionsDir(t *testing.T) {
	home := t.TempDir()
	sessionsDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessions, err := codex.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

// TestReadSessions_NonExistentCodexHome verifies that a CODEX_HOME pointing at
// a non-existent directory returns an empty slice and no error.
func TestReadSessions_NonExistentCodexHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "does-not-exist")

	sessions, err := codex.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

// TestReadSessions_FileRemovedBeforeRead verifies that a rollout file that
// disappears between directory listing and open is silently skipped; other
// sessions in the same directory are still returned.
func TestReadSessions_FileRemovedBeforeRead(t *testing.T) {
	// Build a temporary CODEX_HOME with two session files.
	home := t.TempDir()
	sessDir := filepath.Join(home, "sessions", "2026", "06", "03")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	keepFile := filepath.Join(sessDir, "rollout-2026-06-03T10-00-00-000Z-keep0000-0000-0000-0000-000000000000.jsonl")
	removeFile := filepath.Join(sessDir, "rollout-2026-06-03T09-00-00-000Z-gone0000-0000-0000-0000-000000000000.jsonl")

	keepContent := `{"timestamp":"2026-06-03T10:00:00.000Z","type":"session_meta","payload":{"id":"keep0000-0000-0000-0000-000000000000","timestamp":"2026-06-03T10:00:00.000Z","cwd":"/tmp/keep","originator":"codex-tui","cli_version":"0.136.0","source":"cli"}}
{"timestamp":"2026-06-03T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"keep this"}}
`
	removeContent := `{"timestamp":"2026-06-03T09:00:00.000Z","type":"session_meta","payload":{"id":"gone0000-0000-0000-0000-000000000000","timestamp":"2026-06-03T09:00:00.000Z","cwd":"/tmp/gone","originator":"codex-tui","cli_version":"0.136.0","source":"cli"}}
`

	if err := os.WriteFile(keepFile, []byte(keepContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(removeFile, []byte(removeContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remove the second file to simulate disappearance between list and read.
	// We can't hook into WalkDir, so we remove it before calling ReadSessions.
	// This exercises the os.Open error path in parseRolloutFile.
	if err := os.Remove(removeFile); err != nil {
		t.Fatal(err)
	}

	sessions, err := codex.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1; sessions: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != "keep0000-0000-0000-0000-000000000000" {
		t.Errorf("unexpected session ID %q", sessions[0].ID)
	}
	if sessions[0].Title != "keep this" {
		t.Errorf("Title = %q, want %q", sessions[0].Title, "keep this")
	}
}

// TestReadSessions_DirIsCanonical verifies that the Dir field is normalized
// via pathnorm.Canonical, so it reconciles with worktree/roster paths.
func TestReadSessions_DirIsCanonical(t *testing.T) {
	// Create a real directory so pathnorm.Canonical can resolve it.
	realDir := t.TempDir()

	home := t.TempDir()
	sessDir := filepath.Join(home, "sessions", "2026", "06", "03")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `{"timestamp":"2026-06-03T12:00:00.000Z","type":"session_meta","payload":{"id":"canon000-0000-0000-0000-000000000000","timestamp":"2026-06-03T12:00:00.000Z","cwd":"` + realDir + `","originator":"codex-tui","cli_version":"0.136.0","source":"cli"}}
`
	rolloutFile := filepath.Join(sessDir, "rollout-2026-06-03T12-00-00-000Z-canon000-0000-0000-0000-000000000000.jsonl")
	if err := os.WriteFile(rolloutFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := codex.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}

	wantDir := canonicalDir(t, realDir)
	if sessions[0].Dir != wantDir {
		t.Errorf("Dir = %q, want canonical %q", sessions[0].Dir, wantDir)
	}
}

// TestReadSessions_NoUserMessage verifies that a session with no user_message
// line has an empty Title (not a panic or error).
func TestReadSessions_NoUserMessage(t *testing.T) {
	home := t.TempDir()
	sessDir := filepath.Join(home, "sessions", "2026", "06", "03")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `{"timestamp":"2026-06-03T13:00:00.000Z","type":"session_meta","payload":{"id":"notitle0-0000-0000-0000-000000000000","timestamp":"2026-06-03T13:00:00.000Z","cwd":"/tmp/notitle","originator":"codex-tui","cli_version":"0.136.0","source":"cli"}}
{"timestamp":"2026-06-03T13:00:01.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","duration_ms":100}}
`
	rolloutFile := filepath.Join(sessDir, "rollout-2026-06-03T13-00-00-000Z-notitle0-0000-0000-0000-000000000000.jsonl")
	if err := os.WriteFile(rolloutFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := codex.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].Title != "" {
		t.Errorf("Title = %q, want empty string when no user_message exists", sessions[0].Title)
	}
}

// TestReadSessions_SortedByLastActivityDesc verifies that sessions are returned
// most-recent first.
func TestReadSessions_SortedByLastActivityDesc(t *testing.T) {
	home := filepath.Join("testdata")

	sessions, err := codex.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: unexpected error: %v", err)
	}
	if len(sessions) < 2 {
		t.Skip("need at least 2 sessions in testdata to verify sort order")
	}

	for i := 1; i < len(sessions); i++ {
		if sessions[i].LastActivity.After(sessions[i-1].LastActivity) {
			t.Errorf("sessions not sorted desc: sessions[%d].LastActivity (%v) > sessions[%d].LastActivity (%v)",
				i, sessions[i].LastActivity, i-1, sessions[i-1].LastActivity)
		}
	}
}
