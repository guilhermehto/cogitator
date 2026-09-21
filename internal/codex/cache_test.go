package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/sessioncache"
)

func TestCachedRolloutsMatchFreshReads(t *testing.T) {
	home := t.TempDir()
	if err := os.CopyFS(home, os.DirFS("testdata")); err != nil {
		t.Fatal(err)
	}
	var cache sessioncache.Cache[Session]
	check := func() {
		t.Helper()
		got, err := readSessions(home, &cache)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ReadSessions(home)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cached sessions differ: got %+v, want %+v", got, want)
		}
	}
	check()
	check()
	path := filepath.Join(home, "sessions", "2026", "06", "02", "rollout-2026-06-02T20-35-19-235Z-019e8a0c-3f58-7b91-8d0d-5b2b03d1677f.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString("\n" + `{"timestamp":"2026-09-18T00:00:00Z","type":"event_msg","payload":{"type":"agent_message","message":"done"}}` + "\n")
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	check()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check()
}

func BenchmarkReadSessions(b *testing.B) {
	home := b.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		b.Fatal(err)
	}
	message := `{"timestamp":"2026-09-18T00:00:00Z","type":"event_msg","payload":{"type":"agent_message","message":"` + strings.Repeat("x", 256) + `"}}` + "\n"
	for i := range 100 {
		header := fmt.Sprintf(`{"timestamp":"2026-09-18T00:00:00Z","type":"session_meta","payload":{"id":"session-%d","cwd":%q,"timestamp":"2026-09-18T00:00:00Z"}}`, i, home)
		content := header + "\n" + strings.Repeat(message, 200)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("rollout-%03d.jsonl", i)), []byte(content), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("cached=%t", cached), func(b *testing.B) {
			var cache *sessioncache.Cache[Session]
			if cached {
				cache = &sessioncache.Cache[Session]{}
			}
			if _, err := readSessions(home, cache); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				sessions, err := readSessions(home, cache)
				if err != nil || len(sessions) != 100 {
					b.Fatalf("sessions=%d, err=%v", len(sessions), err)
				}
			}
		})
	}
}

func TestCachedRolloutStillAgesOutOfLiveStatus(t *testing.T) {
	p := NewProvider("testdata", time.Second, time.Minute, nil)
	sessions, err := readSessions("testdata", &p.transcripts)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("sessions=%d, err=%v", len(sessions), err)
	}
	session := sessions[0]
	fresh := p.mergeToUpdate(session, hookOverlay{}, session.LastActivity)
	sessions, err = readSessions("testdata", &p.transcripts)
	if err != nil {
		t.Fatal(err)
	}
	stale := p.mergeToUpdate(sessions[0], hookOverlay{}, session.LastActivity.Add(2*time.Minute))
	if fresh.Source != "live" || stale.Source != "recent" {
		t.Fatalf("source changed from %q to %q", fresh.Source, stale.Source)
	}
}
