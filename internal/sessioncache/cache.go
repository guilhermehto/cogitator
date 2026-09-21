package sessioncache

import "os"

// Cache reuses parsed summaries between scans. Owned by one polling goroutine;
// values must not contain mutable state shared with their callers.
type Cache[T any] struct {
	entries map[string]entry[T]
	seen    map[string]bool
}

type entry[T any] struct {
	stamp   os.FileInfo
	session T
}

func (c *Cache[T]) BeginScan() {
	if c == nil {
		return
	}
	if c.entries == nil {
		c.entries = make(map[string]entry[T])
		c.seen = make(map[string]bool)
	}
	clear(c.seen)
}

func (c *Cache[T]) EndScan() {
	if c == nil {
		return
	}
	for path := range c.entries {
		if !c.seen[path] {
			delete(c.entries, path)
		}
	}
}

func (c *Cache[T]) Read(path string, parse func(string) (T, bool)) (T, bool) {
	if c == nil {
		return parse(path)
	}
	c.seen[path] = true
	before, err := os.Stat(path)
	if err != nil {
		delete(c.entries, path)
		var zero T
		return zero, false
	}
	if previous, ok := c.entries[path]; ok && unchanged(previous.stamp, before) {
		return previous.session, true
	}
	delete(c.entries, path)
	session, ok := parse(path)
	if ok {
		// A writer may append or replace the transcript while it is being parsed.
		// Retry next poll unless the same file remained unchanged throughout.
		after, err := os.Stat(path)
		if err == nil && unchanged(before, after) {
			c.entries[path] = entry[T]{stamp: after, session: session}
		}
	}
	return session, ok
}

func unchanged(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}
