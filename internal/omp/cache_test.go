package omp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/guilhermehto/cogitator/internal/sessioncache"
)

func TestCachedSessionScanDiscoversAndOrdersNewSessions(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var cache sessioncache.Cache[Session]
	for i := range 3 {
		content := fmt.Sprintf(`{"type":"session","id":"session-%d","cwd":%q,"title":"Session %d","timestamp":"2026-09-18T00:00:0%dZ"}`, i, home, i, i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.jsonl", i)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		sessions, err := readSessions(home, &cache)
		if err != nil || len(sessions) != i+1 {
			t.Fatalf("sessions=%d, err=%v", len(sessions), err)
		}
		for j, session := range sessions {
			want := fmt.Sprintf("session-%d", i-j)
			if session.ID != want {
				t.Fatalf("session[%d]=%q, want %q", j, session.ID, want)
			}
		}
	}
}
