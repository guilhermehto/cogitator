package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/state"
)

// RosterEntry records the last-known state of a single worktree. It is keyed
// by the canonical worktree directory so entries survive reboots and process
// restarts. Only facts that are stable across restarts are persisted; runtime
// state (host, port, instance id) is intentionally omitted.
//
// Limitation: only the most-recent session per worktree is retained. If a
// worktree hosts multiple sessions over time, earlier sessions are silently
// replaced by the one with the greatest LastActivity timestamp.
type RosterEntry struct {
	// Dir is the canonical absolute path to the worktree (map key, also
	// stored inline for human readability of the JSON file).
	Dir string `json:"dir"`
	// Harness is the tool that manages the session (e.g. "opencode").
	Harness string `json:"harness"`
	// SessionID is the last-known session identifier. Optional — present when
	// the session was observed live; absent for entries loaded from a previous
	// run where the session id was not recorded. Used for --session resume when
	// present; per-directory resume works without it.
	SessionID string `json:"sessionId,omitempty"`
	// Title is the last-known session title.
	Title string `json:"title,omitempty"`
	// LastActivity is the timestamp of the most recent activity in the session.
	LastActivity time.Time `json:"lastActivity"`
}

// rosterFile is the on-disk JSON representation of the roster map.
type rosterFile struct {
	Entries []RosterEntry `json:"entries"`
}

// rosterDir returns the directory that holds the roster file.
// It honours $XDG_STATE_HOME and falls back to ~/.local/state/cogitator/.
func rosterDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "cogitator"), nil
}

// RosterPath returns the absolute path to the roster file.
func RosterPath() (string, error) {
	dir, err := rosterDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "roster.json"), nil
}

// Load reads the roster from disk and returns a map keyed by canonical
// worktree directory. If the file does not exist, Load returns an empty map
// (no error). Entries whose worktree directory no longer exists on disk are
// pruned so missing rows do not accumulate indefinitely.
func Load() (map[string]RosterEntry, error) {
	path, err := RosterPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]RosterEntry{}, nil
		}
		return nil, fmt.Errorf("read roster %s: %w", path, err)
	}

	var raw rosterFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse roster %s: %w", path, err)
	}

	m := make(map[string]RosterEntry, len(raw.Entries))
	for _, e := range raw.Entries {
		// Prune entries whose worktree no longer exists on disk.
		if _, statErr := os.Stat(e.Dir); statErr != nil {
			continue
		}
		m[e.Dir] = e
	}
	return m, nil
}

// Save writes the roster map to disk atomically (temp file + rename) so a
// crash mid-write never leaves a corrupt file. The parent directory is created
// if it does not exist.
//
// Save must only be called from the single recorder goroutine; concurrent
// callers must send upserts over the channel returned by NewRecorder instead.
func Save(m map[string]RosterEntry) error {
	path, err := RosterPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create roster dir %s: %w", dir, err)
	}

	entries := make([]RosterEntry, 0, len(m))
	for _, e := range m {
		entries = append(entries, e)
	}
	raw := rosterFile{Entries: entries}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal roster: %w", err)
	}

	// Write to a temp file in the same directory so the rename is atomic on
	// POSIX systems (same filesystem, single syscall).
	tmp, err := os.CreateTemp(dir, "roster-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp roster file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp roster file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp roster file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename roster file: %w", err)
	}
	return nil
}

// Recorder drains a store subscription channel and persists roster updates.
// It is the single writer of roster.json; all other components must send
// upserts over the Upserts channel rather than calling Save directly.
//
// Roster completeness is best-effort: the store's publish is non-blocking with
// a cap-4 buffer, so snapshots may be dropped under load. This is documented
// and accepted — the roster reflects the most-recently-observed state, not a
// guaranteed audit log.
type Recorder struct {
	// Upserts is the channel other components use to inject entries without
	// calling Save directly. The recorder is the sole writer of roster.json.
	Upserts chan RosterEntry
}

// NewRecorder creates a Recorder. Call Run to start the background goroutine.
func NewRecorder() *Recorder {
	return &Recorder{
		Upserts: make(chan RosterEntry, 16),
	}
}

// Run starts the recorder goroutine. It subscribes to snapshots via snapshots,
// upserts entries for every top-level SessionView with a non-empty Directory,
// and writes the roster atomically after each batch. It stops when ctx is
// cancelled.
//
// Run must be called exactly once per Recorder. The caller is responsible for
// ensuring ctx is cancelled when the recorder should stop (e.g. via the
// RunTUI defer cancel).
func (r *Recorder) Run(ctx context.Context, snapshots <-chan state.Snapshot) {
	go func() {
		r.loop(ctx, snapshots)
	}()
}

// RunSync runs the recorder loop synchronously in the calling goroutine. It
// exits when ctx is cancelled or snapshots is closed. Intended for tests that
// need deterministic sequencing without spawning a goroutine.
func (r *Recorder) RunSync(snapshots <-chan state.Snapshot) {
	r.loop(context.Background(), snapshots)
}

// loop is the shared implementation for Run and RunSync.
func (r *Recorder) loop(ctx context.Context, snapshots <-chan state.Snapshot) {
	m, err := Load()
	if err != nil {
		// Non-fatal: start with an empty roster rather than refusing to run.
		m = map[string]RosterEntry{}
	}

	for {
		select {
		case <-ctx.Done():
			return

		case snap, ok := <-snapshots:
			if !ok {
				return
			}
			changed := r.applySnapshot(m, snap)
			if changed {
				// Atomic write off the hot receive path. Errors are
				// non-fatal: the in-memory map is still up to date and
				// the next snapshot will retry.
				_ = Save(m)
			}

		case entry, ok := <-r.Upserts:
			if !ok {
				return
			}
			upsert(m, entry)
			_ = Save(m)
		}
	}
}

// applySnapshot upserts an entry for every top-level SessionView (ParentID
// empty) with a non-empty Directory. Returns true if any entry changed.
//
// The Harness field is preserved from the existing roster entry when one
// exists, so a create-time write (e.g. for a Codex worktree) is not silently
// overwritten by a live-discovery snapshot that only knows "opencode".
func (r *Recorder) applySnapshot(m map[string]RosterEntry, snap state.Snapshot) bool {
	changed := false
	for _, sv := range snap.Sessions {
		if sv.ParentID != "" || sv.Directory == "" {
			continue
		}
		canonical, err := pathnorm.Canonical(sv.Directory)
		if err != nil {
			// Unresolvable path — skip rather than storing a bad key.
			continue
		}
		// Default harness for live-discovered sessions is opencode; preserve
		// any harness already recorded for this directory (e.g. codex set at
		// create time) so a snapshot never silently downgrades the kind.
		harnessKind := "opencode"
		if cur, ok := m[canonical]; ok && cur.Harness != "" {
			harnessKind = cur.Harness
		}
		entry := RosterEntry{
			Dir:          canonical,
			Harness:      harnessKind,
			SessionID:    sv.SessionID,
			Title:        sv.Title,
			LastActivity: sv.LastActivity,
		}
		if upsert(m, entry) {
			changed = true
		}
	}
	return changed
}

// upsert inserts or updates entry in m, keeping the entry with the greater
// LastActivity timestamp. Returns true if the map was modified.
func upsert(m map[string]RosterEntry, entry RosterEntry) bool {
	cur, ok := m[entry.Dir]
	if ok && !entry.LastActivity.After(cur.LastActivity) {
		return false
	}
	m[entry.Dir] = entry
	return true
}
