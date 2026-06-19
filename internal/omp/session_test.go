package omp_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/omp"
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

// writeSession writes a session JSONL file into <home>/sessions/<enc>/<name>
// with the given lines, creating parent dirs.
func writeSession(t *testing.T, home, enc, name string, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", enc)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadSessions_WellFormed_HeaderTitle(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "-tmp-wt", "2026-06-19T10-46-12-274Z_019edf7d-0232-7000-a748-ad17e565f8dc.jsonl",
		`{"type":"session","version":3,"id":"019edf7d-0232-7000-a748-ad17e565f8dc","timestamp":"2026-06-19T10:46:12.274Z","cwd":"/tmp/wt","title":"Add omp support"}`,
		`{"type":"message","id":"ff6e5395","timestamp":"2026-06-19T10:50:32.540Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"message","id":"5ce7207a","timestamp":"2026-06-19T10:50:34.950Z","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
	)

	sessions, err := omp.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]

	if s.ID != "019edf7d-0232-7000-a748-ad17e565f8dc" {
		t.Errorf("ID = %q", s.ID)
	}
	if s.Title != "Add omp support" {
		t.Errorf("Title = %q, want header title", s.Title)
	}
	if want := canonicalDir(t, "/tmp/wt"); s.Dir != want {
		t.Errorf("Dir = %q, want %q", s.Dir, want)
	}
	if want := mustParseTime(t, "2026-06-19T10:46:12.274Z"); !s.Created.Equal(want) {
		t.Errorf("Created = %v, want %v", s.Created, want)
	}
	if want := mustParseTime(t, "2026-06-19T10:50:34.950Z"); !s.LastActivity.Equal(want) {
		t.Errorf("LastActivity = %v, want %v", s.LastActivity, want)
	}
}

func TestReadSessions_TitleFallbackToFirstUserMessage(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "-tmp-wt", "2026-06-19T10-46-12-274Z_abc.jsonl",
		`{"type":"session","version":3,"id":"abc","timestamp":"2026-06-19T10:46:12.274Z","cwd":"/tmp/wt"}`,
		`{"type":"message","id":"m1","timestamp":"2026-06-19T10:50:32.540Z","message":{"role":"user","content":[{"type":"text","text":"fix the parser bug"}]}}`,
	)

	sessions, err := omp.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].Title != "fix the parser bug" {
		t.Errorf("Title = %q, want first user message", sessions[0].Title)
	}
}

func TestReadSessions_TruncatedFinalLine(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "-tmp-wt", "2026-06-19T10-46-12-274Z_trunc.jsonl",
		`{"type":"session","version":3,"id":"trunc","timestamp":"2026-06-19T10:46:12.274Z","cwd":"/tmp/wt","title":"t"}`,
		`{"type":"message","id":"m1","timestamp":"2026-06-19T11:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`,
		`{"type":"message","id":"m2","timestamp":"2026-06-19T11:0`, // garbage truncated line
	)

	sessions, err := omp.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if want := mustParseTime(t, "2026-06-19T11:00:02.000Z"); !sessions[0].LastActivity.Equal(want) {
		t.Errorf("LastActivity = %v, want %v (garbage line skipped)", sessions[0].LastActivity, want)
	}
}

func TestReadSessions_SkipsNonHeaderFirstLine(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "-tmp-wt", "2026-06-19T10-46-12-274Z_nohdr.jsonl",
		`{"type":"message","id":"m1","timestamp":"2026-06-19T11:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`,
	)

	sessions, err := omp.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0 (no header line)", len(sessions))
	}
}

func TestReadSessions_SortedByLastActivityDesc(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "-a", "2026-06-19T10-00-00-000Z_old.jsonl",
		`{"type":"session","id":"old","timestamp":"2026-06-19T10:00:00.000Z","cwd":"/tmp/a","title":"a"}`,
		`{"type":"message","timestamp":"2026-06-19T10:05:00.000Z","message":{"role":"user","content":[]}}`,
	)
	writeSession(t, home, "-b", "2026-06-19T12-00-00-000Z_new.jsonl",
		`{"type":"session","id":"new","timestamp":"2026-06-19T12:00:00.000Z","cwd":"/tmp/b","title":"b"}`,
		`{"type":"message","timestamp":"2026-06-19T12:05:00.000Z","message":{"role":"user","content":[]}}`,
	)

	sessions, err := omp.ReadSessions(home)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].ID != "new" || sessions[1].ID != "old" {
		t.Errorf("order = [%s, %s], want [new, old]", sessions[0].ID, sessions[1].ID)
	}
}

func TestReadSessions_EmptyAndAbsentHome(t *testing.T) {
	// Empty sessions dir.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s, err := omp.ReadSessions(home); err != nil || len(s) != 0 {
		t.Errorf("empty dir: got %d sessions, err %v; want 0, nil", len(s), err)
	}

	// Non-existent home.
	absent := filepath.Join(t.TempDir(), "does-not-exist")
	if s, err := omp.ReadSessions(absent); err != nil || len(s) != 0 {
		t.Errorf("absent home: got %d sessions, err %v; want 0, nil", len(s), err)
	}
}

func TestSessionIDFromFilename(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"2026-06-19T10-46-12-274Z_019edf7d-0232-7000-a748-ad17e565f8dc.jsonl", "019edf7d-0232-7000-a748-ad17e565f8dc"},
		{"/abs/path/2026-06-19T10-46-12-274Z_abc.jsonl", "abc"},
		{"no-underscore.jsonl", ""},
		{"trailing_.jsonl", ""},
	}
	for _, tc := range tests {
		if got := omp.SessionIDFromFilename(tc.name); got != tc.want {
			t.Errorf("SessionIDFromFilename(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
