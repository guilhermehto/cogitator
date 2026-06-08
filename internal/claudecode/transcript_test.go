package claudecode_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// canonicalDir returns the pathnorm.Canonical form of dir, failing the test on
// error.
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

// findSession returns the session with the given ID from the slice, or nil.
func findSession(sessions []claudecode.Session, id string) *claudecode.Session {
	for i := range sessions {
		if sessions[i].ID == id {
			return &sessions[i]
		}
	}
	return nil
}

// TestReadSessions_NonExistentHome verifies that a missing ~/.claude returns
// an empty slice and no error.
func TestReadSessions_NonExistentHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "does-not-exist")
	sessions, err := claudecode.ReadSessions(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

// TestReadSessions_EmptyProjectsDir verifies that a ~/.claude with an empty
// projects directory returns an empty slice and no error.
func TestReadSessions_EmptyProjectsDir(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	sessions, err := claudecode.ReadSessions(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

// TestReadSessions_ArrayFormTitle verifies that a session whose user message
// content is an array of {type,text} blocks is parsed correctly.
func TestReadSessions_ArrayFormTitle(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}

	// aaaaaaaa session: array-form content, has ai-title — ai-title should win.
	s := findSession(sessions, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if s == nil {
		t.Fatalf("session aaaaaaaa not found; got: %+v", sessions)
	}
	if s.Title != "Add Feature X" {
		t.Errorf("Title = %q, want %q (ai-title should take precedence)", s.Title, "Add Feature X")
	}
	wantDir := canonicalDir(t, "/tmp/test-project")
	if s.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", s.Dir, wantDir)
	}
}

// TestReadSessions_AITitlePrecedence verifies that the latest ai-title line
// wins over the first user message when both are present.
func TestReadSessions_AITitlePrecedence(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}

	// aaaaaaaa session has both a user message ("add feature X") and an
	// ai-title ("Add Feature X"). The ai-title must win.
	s := findSession(sessions, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if s == nil {
		t.Fatalf("session aaaaaaaa not found")
	}
	if s.Title != "Add Feature X" {
		t.Errorf("Title = %q, want ai-title %q", s.Title, "Add Feature X")
	}
}

// TestReadSessions_StringFormTitle verifies that a session whose user message
// content is a plain string is parsed correctly.
func TestReadSessions_StringFormTitle(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}

	// bbbbbbbb session: string-form content, no ai-title.
	s := findSession(sessions, "bbbbbbbb-cccc-dddd-eeee-ffffffffffff")
	if s == nil {
		t.Fatalf("session bbbbbbbb not found; got: %+v", sessions)
	}
	if s.Title != "fix the login bug" {
		t.Errorf("Title = %q, want %q", s.Title, "fix the login bug")
	}
}

// TestReadSessions_SyntheticFirstLineSkip verifies that synthetic user lines
// (Human: prefix, slash-commands, <command-name> XML) are skipped when
// deriving the title, and the first real user line is used instead.
func TestReadSessions_SyntheticFirstLineSkip(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}

	// cccccccc session: first two user lines are synthetic, third is real.
	s := findSession(sessions, "cccccccc-dddd-eeee-ffff-000000000000")
	if s == nil {
		t.Fatalf("session cccccccc not found; got: %+v", sessions)
	}
	if s.Title != "real user message after synthetic lines" {
		t.Errorf("Title = %q, want %q", s.Title, "real user message after synthetic lines")
	}
}

// TestReadSessions_MalformedLineSkipped verifies that a malformed JSON line
// does not abort parsing; the session is still returned with the correct title.
func TestReadSessions_MalformedLineSkipped(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}

	// dddddddd session: has a malformed line before the user message.
	s := findSession(sessions, "dddddddd-eeee-ffff-0000-111111111111")
	if s == nil {
		t.Fatalf("session dddddddd not found; got: %+v", sessions)
	}
	if s.Title != "refactor the parser" {
		t.Errorf("Title = %q, want %q", s.Title, "refactor the parser")
	}
}

// TestReadSessions_MissingCWDSkipped verifies that a session with no cwd on
// any line is skipped entirely (empty Dir would break dir-keyed merge/jump).
func TestReadSessions_MissingCWDSkipped(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}

	// eeeeeeee session: no cwd field on any line — must be absent from results.
	s := findSession(sessions, "eeeeeeee-ffff-0000-1111-222222222222")
	if s != nil {
		t.Errorf("session eeeeeeee should be skipped (no cwd), but was returned: %+v", s)
	}
}

// TestReadSessions_OversizedLine verifies that a file containing a >1 MiB line
// is still parsed: the session is returned with the correct title and the line
// after the oversized one is processed (LastActivity reflects it).
func TestReadSessions_OversizedLine(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}

	// ffffffff session: line 2 is >1 MiB; line 3 is a normal user line.
	s := findSession(sessions, "ffffffff-0000-1111-2222-333333333333")
	if s == nil {
		t.Fatalf("session ffffffff not found; got IDs: %v", sessionIDs(sessions))
	}
	// Title comes from line 1 (first user line: "analyze this image").
	if s.Title != "analyze this image" {
		t.Errorf("Title = %q, want %q", s.Title, "analyze this image")
	}
	// LastActivity must be the timestamp of line 3 (after the oversized line).
	wantLast := mustParseTime(t, "2026-06-06T05:00:10.000Z")
	if !s.LastActivity.Equal(wantLast) {
		t.Errorf("LastActivity = %v, want %v (line after oversized must be processed)", s.LastActivity, wantLast)
	}
}

// TestReadSessions_SortedByLastActivityDesc verifies that sessions are returned
// most-recent first.
func TestReadSessions_SortedByLastActivityDesc(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) < 2 {
		t.Skip("need at least 2 sessions in testdata to verify sort order")
	}
	for i := 1; i < len(sessions); i++ {
		if sessions[i].LastActivity.After(sessions[i-1].LastActivity) {
			t.Errorf("sessions not sorted desc: [%d].LastActivity (%v) > [%d].LastActivity (%v)",
				i, sessions[i].LastActivity, i-1, sessions[i-1].LastActivity)
		}
	}
}

// TestReadSessions_FileRemovedBeforeRead verifies that a session file that
// disappears between directory listing and open is silently skipped.
func TestReadSessions_FileRemovedBeforeRead(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-tmp-keep")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	keepContent := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"keep this"}]},"uuid":"u1","timestamp":"2026-06-07T10:00:01.000Z","cwd":"/tmp/keep","sessionId":"keep0000-0000-0000-0000-000000000000","version":"2.1.0"}
`
	goneContent := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"gone"}]},"uuid":"u1","timestamp":"2026-06-07T09:00:01.000Z","cwd":"/tmp/gone","sessionId":"gone0000-0000-0000-0000-000000000000","version":"2.1.0"}
`
	keepFile := filepath.Join(projDir, "keep0000-0000-0000-0000-000000000000.jsonl")
	goneFile := filepath.Join(projDir, "gone0000-0000-0000-0000-000000000000.jsonl")

	if err := os.WriteFile(keepFile, []byte(keepContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goneFile, []byte(goneContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(goneFile); err != nil {
		t.Fatal(err)
	}

	sessions, err := claudecode.ReadSessions(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1; sessions: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != "keep0000-0000-0000-0000-000000000000" {
		t.Errorf("unexpected session ID %q", sessions[0].ID)
	}
}

// TestReadSessions_CreatedAndLastActivity verifies that Created is the first
// parseable timestamp and LastActivity is the last.
func TestReadSessions_CreatedAndLastActivity(t *testing.T) {
	sessions, err := claudecode.ReadSessions(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}

	// aaaaaaaa session: first timestamp is the queue-operation line.
	s := findSession(sessions, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if s == nil {
		t.Fatalf("session aaaaaaaa not found")
	}
	wantCreated := mustParseTime(t, "2026-06-01T10:00:00.000Z")
	if !s.Created.Equal(wantCreated) {
		t.Errorf("Created = %v, want %v", s.Created, wantCreated)
	}
	wantLast := mustParseTime(t, "2026-06-01T10:00:10.000Z")
	if !s.LastActivity.Equal(wantLast) {
		t.Errorf("LastActivity = %v, want %v", s.LastActivity, wantLast)
	}
}

// TestReadSessions_DirIsCanonical verifies that Dir is normalized via
// pathnorm.Canonical so it reconciles with worktree/roster paths.
func TestReadSessions_DirIsCanonical(t *testing.T) {
	realDir := t.TempDir()

	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-tmp-canon")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"uuid":"u1","timestamp":"2026-06-07T11:00:01.000Z","cwd":"` + realDir + `","sessionId":"canon000-0000-0000-0000-000000000000","version":"2.1.0"}
`
	if err := os.WriteFile(filepath.Join(projDir, "canon000-0000-0000-0000-000000000000.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := claudecode.ReadSessions(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	wantDir := canonicalDir(t, realDir)
	if sessions[0].Dir != wantDir {
		t.Errorf("Dir = %q, want canonical %q", sessions[0].Dir, wantDir)
	}
}

// TestReadSessions_RealClaudeHome is a smoke test that reads the real
// ~/.claude/projects directory (if it exists) and verifies that at least one
// session has a non-empty Dir and ID.
func TestReadSessions_RealClaudeHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	claudeHome := filepath.Join(home, ".claude")
	if _, err := os.Stat(filepath.Join(claudeHome, "projects")); err != nil {
		t.Skip("~/.claude/projects does not exist")
	}

	sessions, err := claudecode.ReadSessions(claudeHome)
	if err != nil {
		t.Fatalf("ReadSessions on real home: %v", err)
	}
	if len(sessions) == 0 {
		t.Skip("no sessions found in ~/.claude/projects")
	}

	for _, s := range sessions {
		if s.ID == "" {
			t.Errorf("session has empty ID: %+v", s)
		}
		if s.Dir == "" {
			t.Errorf("session %q has empty Dir (should have been skipped)", s.ID)
		}
	}

	// Log a sample for manual inspection.
	t.Logf("found %d sessions; most recent: ID=%s Dir=%s Title=%q Created=%v LastActivity=%v",
		len(sessions),
		sessions[0].ID,
		sessions[0].Dir,
		sessions[0].Title,
		sessions[0].Created,
		sessions[0].LastActivity,
	)
}

// sessionIDs returns the IDs of all sessions, for diagnostic output.
func sessionIDs(sessions []claudecode.Session) []string {
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	return ids
}

// TestReadSessions_TitleTruncation verifies that a very long user message is
// truncated to 80 runes with an ellipsis.
func TestReadSessions_TitleTruncation(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-tmp-trunc")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	longMsg := strings.Repeat("x", 100)
	content := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + longMsg + `"}]},"uuid":"u1","timestamp":"2026-06-07T12:00:01.000Z","cwd":"/tmp/trunc","sessionId":"trunc000-0000-0000-0000-000000000000","version":"2.1.0"}
`
	if err := os.WriteFile(filepath.Join(projDir, "trunc000-0000-0000-0000-000000000000.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := claudecode.ReadSessions(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	title := sessions[0].Title
	runes := []rune(title)
	if len(runes) != 81 { // 80 runes + ellipsis (1 rune)
		t.Errorf("Title rune count = %d, want 81 (80 + ellipsis); title = %q", len(runes), title)
	}
	if !strings.HasSuffix(title, "…") {
		t.Errorf("Title does not end with ellipsis: %q", title)
	}
}
