package sessioncache_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/sessioncache"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(path string) (string, bool) {
	content, err := os.ReadFile(path)
	return string(content), err == nil
}

func TestUnchangedFilesAreNotReadAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "original")
	var cache sessioncache.Cache[string]
	reads := 0
	parse := func(path string) (string, bool) {
		reads++
		return readFile(path)
	}
	for range 3 {
		cache.BeginScan()
		got, ok := cache.Read(path, parse)
		cache.EndScan()
		if !ok || got != "original" {
			t.Fatalf("got %q, %v", got, ok)
		}
	}
	if reads != 1 {
		t.Fatalf("read unchanged file %d times, want 1", reads)
	}
}

func TestChangedFilesAreReadAgain(t *testing.T) {
	for _, change := range []string{"append", "truncate", "rewrite", "replace"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeFile(t, path, "original")
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			var cache sessioncache.Cache[string]
			cache.BeginScan()
			cache.Read(path, readFile)
			cache.EndScan()
			want := "original appended"
			switch change {
			case "truncate":
				want = "short"
			case "rewrite", "replace":
				want = "modified"
			}
			if change == "replace" {
				replacement := path + ".new"
				writeFile(t, replacement, want)
				if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, path, want)
				stamp := before.ModTime()
				if change == "rewrite" {
					stamp = stamp.Add(time.Second)
				}
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			cache.BeginScan()
			got, ok := cache.Read(path, readFile)
			cache.EndScan()
			if !ok || got != want {
				t.Fatalf("got %q, %v; want %q", got, ok, want)
			}
		})
	}
}

func TestMissingFilesAreNotReturnedFromCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "original")
	var cache sessioncache.Cache[string]
	cache.BeginScan()
	cache.Read(path, readFile)
	cache.EndScan()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cache.BeginScan()
	_, ok := cache.Read(path, readFile)
	cache.EndScan()
	if ok {
		t.Fatal("returned deleted file")
	}
}

func TestFilesNotSeenDuringScanAreEvicted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "original")
	var cache sessioncache.Cache[string]
	reads := 0
	parse := func(path string) (string, bool) {
		reads++
		return readFile(path)
	}
	cache.BeginScan()
	cache.Read(path, parse)
	cache.EndScan()
	cache.BeginScan()
	cache.EndScan()
	cache.BeginScan()
	cache.Read(path, parse)
	cache.EndScan()
	if reads != 2 {
		t.Fatalf("read count = %d, want 2 after eviction", reads)
	}
}

func TestFilesChangedDuringReadAreRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "original")
	var cache sessioncache.Cache[string]
	cache.BeginScan()
	cache.Read(path, func(path string) (string, bool) {
		content, ok := readFile(path)
		writeFile(t, path, "appended content")
		return content, ok
	})
	cache.EndScan()
	cache.BeginScan()
	got, ok := cache.Read(path, readFile)
	cache.EndScan()
	if !ok || got != "appended content" {
		t.Fatalf("got %q, %v", got, ok)
	}
}

func TestFailedReadsAreRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "original")
	var cache sessioncache.Cache[string]
	cache.BeginScan()
	cache.Read(path, func(string) (string, bool) { return "", false })
	cache.EndScan()
	cache.BeginScan()
	got, ok := cache.Read(path, readFile)
	cache.EndScan()
	if !ok || got != "original" {
		t.Fatalf("got %q, %v", got, ok)
	}
}
