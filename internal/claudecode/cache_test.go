package claudecode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guilhermehto/cogitator/internal/sessioncache"
)

func TestCachedTranscriptPicksUpTitleAndCompletedLine(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	content := `{"type":"user","cwd":"/tmp/project","timestamp":"2026-09-18T00:00:00Z","message":{"content":"Original title"}}` + "\n" + `{"type":"ai-title","aiTitle":"New`
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(content)
	var cache sessioncache.Cache[Session]
	checkTitle := func(want string) {
		t.Helper()
		sessions, err := readSessions(home, &cache)
		if err != nil || len(sessions) != 1 {
			t.Fatalf("sessions=%d, err=%v", len(sessions), err)
		}
		if sessions[0].Title != want {
			t.Fatalf("title=%q, want %q", sessions[0].Title, want)
		}
	}
	checkTitle("Original title")
	checkTitle("Original title")
	write(content + ` title"}` + "\n")
	checkTitle("New title")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	sessions, err := readSessions(home, &cache)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions=%d after deletion, err=%v", len(sessions), err)
	}
}
