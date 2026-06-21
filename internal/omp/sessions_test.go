package omp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/omp"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// writeSessionFile writes a JSONL file under <agentDir>/sessions/<enc>/ and
// returns the agent dir. encDir scopes the file like omp's dir-encoded layout.
func writeSessionFile(t *testing.T, agentDir, encDir, filename, content string) {
	t.Helper()
	dir := filepath.Join(agentDir, "sessions", encDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func iso(ts time.Time) string { return ts.UTC().Format(time.RFC3339Nano) }

func TestReadSessions_HeaderTitlePreferred(t *testing.T) {
	agentDir := t.TempDir()
	created := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	last := created.Add(2 * time.Minute)

	content := `{"type":"session","version":3,"id":"abc123","timestamp":"` + iso(created) + `","cwd":"/tmp/wt","title":"Fix the parser"}
{"type":"message","id":"e1","parentId":null,"timestamp":"` + iso(last) + `","message":{"role":"user","content":[{"type":"text","text":"ignored because header has a title"}]}}
`
	writeSessionFile(t, agentDir, "-tmp-wt", "2026-06-19T10-00-00-000Z_abc123.jsonl", content)

	got, err := omp.ReadSessions(agentDir)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	s := got[0]
	if s.ID != "abc123" {
		t.Errorf("ID = %q, want abc123", s.ID)
	}
	if s.Title != "Fix the parser" {
		t.Errorf("Title = %q, want header title", s.Title)
	}
	wantDir, _ := pathnorm.Canonical("/tmp/wt")
	if s.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", s.Dir, wantDir)
	}
	if !s.Created.Equal(created) {
		t.Errorf("Created = %v, want %v", s.Created, created)
	}
	if !s.LastActivity.Equal(last) {
		t.Errorf("LastActivity = %v, want %v (last line)", s.LastActivity, last)
	}
}

func TestReadSessions_TitleDerivedFromUserMessage(t *testing.T) {
	agentDir := t.TempDir()
	ts := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)

	// array-form content
	arr := `{"type":"session","version":3,"id":"arr1","timestamp":"` + iso(ts) + `","cwd":"/tmp/a"}
{"type":"message","id":"e1","parentId":null,"timestamp":"` + iso(ts) + `","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}
{"type":"message","id":"e2","parentId":"e1","timestamp":"` + iso(ts) + `","message":{"role":"user","content":[{"type":"text","text":"  first user prompt  "}]}}
`
	writeSessionFile(t, agentDir, "-a", "2026_arr1.jsonl", arr)

	// string-form content
	str := `{"type":"session","version":3,"id":"str1","timestamp":"` + iso(ts) + `","cwd":"/tmp/b"}
{"type":"message","id":"e1","parentId":null,"timestamp":"` + iso(ts) + `","message":{"role":"user","content":"plain string prompt"}}
`
	writeSessionFile(t, agentDir, "-b", "2026_str1.jsonl", str)

	got, err := omp.ReadSessions(agentDir)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	titles := map[string]string{}
	for _, s := range got {
		titles[s.ID] = s.Title
	}
	if titles["arr1"] != "first user prompt" {
		t.Errorf("arr1 Title = %q, want trimmed first user text", titles["arr1"])
	}
	if titles["str1"] != "plain string prompt" {
		t.Errorf("str1 Title = %q, want string content", titles["str1"])
	}
}

func TestReadSessions_NonSessionFileSkipped(t *testing.T) {
	agentDir := t.TempDir()
	ts := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)

	// A subagent transcript: first line is NOT a session header.
	sub := `{"type":"message","id":"e1","parentId":null,"timestamp":"` + iso(ts) + `","message":{"role":"user","content":"sub"}}
`
	writeSessionFile(t, agentDir, "-x/sess", "Worker.jsonl", sub)

	// A header missing its id is also rejected.
	noID := `{"type":"session","version":3,"timestamp":"` + iso(ts) + `","cwd":"/tmp/c"}
`
	writeSessionFile(t, agentDir, "-c", "2026_noid.jsonl", noID)

	got, err := omp.ReadSessions(agentDir)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0 (non-session files skipped): %+v", len(got), got)
	}
}

func TestReadSessions_OrderedByLastActivityDescAndTolerant(t *testing.T) {
	agentDir := t.TempDir()
	old := time.Date(2026, 6, 19, 8, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)

	// Malformed trailing line must be tolerated; session still parsed.
	mk := func(id string, last time.Time) string {
		return `{"type":"session","version":3,"id":"` + id + `","timestamp":"` + iso(old) + `","cwd":"/tmp/` + id + `","title":"` + id + `"}
{"type":"message","id":"e1","parentId":null,"timestamp":"` + iso(last) + `","message":{"role":"user","content":"x"}}
{not valid json
`
	}
	writeSessionFile(t, agentDir, "-1", "2026_one.jsonl", mk("one", old))
	writeSessionFile(t, agentDir, "-2", "2026_two.jsonl", mk("two", recent))
	writeSessionFile(t, agentDir, "-3", "2026_three.jsonl", mk("three", mid))

	got, err := omp.ReadSessions(agentDir)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantOrder := []string{"two", "three", "one"}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("position %d = %q, want %q (desc by LastActivity)", i, got[i].ID, id)
		}
	}
}

func TestReadSessions_TitleTruncated(t *testing.T) {
	agentDir := t.TempDir()
	ts := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	long := strings.Repeat("x", 200)
	content := `{"type":"session","version":3,"id":"long1","timestamp":"` + iso(ts) + `","cwd":"/tmp/l","title":"` + long + `"}
`
	writeSessionFile(t, agentDir, "-l", "2026_long1.jsonl", content)

	got, err := omp.ReadSessions(agentDir)
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !strings.HasSuffix(got[0].Title, "…") {
		t.Errorf("Title = %q, want truncated with ellipsis", got[0].Title)
	}
	if n := len([]rune(got[0].Title)); n > 81 { // 80 runes + ellipsis
		t.Errorf("Title rune count = %d, want <= 81", n)
	}
}

func TestReadSessions_AbsentDir(t *testing.T) {
	got, err := omp.ReadSessions(filepath.Join(t.TempDir(), "no-such-agent-dir"))
	if err != nil {
		t.Fatalf("ReadSessions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0 for absent dir", len(got))
	}
}
