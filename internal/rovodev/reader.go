// Package rovodev provides a reader for Atlassian Rovo Dev CLI session data.
//
// Sessions live at:
//
//	<rovodev-home>/sessions/<session-uuid>/
//	  metadata.json         (small: title, workspace_path)
//	  session_context.json  (large: full transcript; only its mtime is used)
//
// <rovodev-home> defaults to ~/.rovodev when empty. The <session-uuid> directory
// name is the session id used by `acli rovodev run --restore <id>`.
//
// Hot-path cost: session_context.json can be several MB, so the reader NEVER
// parses it on the common path — the tiny metadata.json supplies title and
// workspace_path, and file mtime supplies last-activity. session_context.json is
// parsed only as a fallback for the rare session directory that has no
// metadata.json.
//
// The package is intentionally free of internal/ui, bubbletea, internal/oc,
// and internal/state — it is a pure filesystem-in / structs-out parser.
package rovodev

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// Session holds the parsed summary of a single Rovo Dev session directory.
type Session struct {
	// ID is the session UUID (the sessions/<uuid> directory name), the value
	// passed to `acli rovodev run --restore <id>`.
	ID string

	// Dir is the workspace path, normalized via pathnorm.Canonical so it
	// reconciles with worktree and roster paths. May be empty when unknown.
	Dir string

	// Title is the session title from metadata.json, or the first line of the
	// initial prompt when metadata.json is absent. Empty when neither exists.
	Title string

	// Created is the session initiation time. Populated only from the fallback
	// context parse (metadata.json carries no timestamp); zero otherwise.
	Created time.Time

	// LastActivity is the most-recent mtime among the session's on-disk files,
	// the best available proxy for when the session was last written to.
	LastActivity time.Time
}

// maxTitleRunes is the maximum number of Unicode code points kept in a derived
// title. Long prompts are truncated with an ellipsis.
const maxTitleRunes = 80

// ReadSessions lists <rovodevHome>/sessions/<uuid>/ directories, summarizes each,
// and returns the sessions ordered by LastActivity descending (most-recent first).
//
// rovodevHome, when non-empty, is the Rovo Dev home directory (the parent of
// sessions/). When empty it defaults to ~/.rovodev.
//
// Tolerances:
//   - home empty/absent or sessions/ missing → empty slice, nil error.
//   - A directory with no readable metadata.json / session_context.json is
//     skipped (it is not a session).
//   - Malformed metadata.json → the session is still returned from mtime alone.
func ReadSessions(rovodevHome string) ([]Session, error) {
	root := resolveRovodevHome(rovodevHome)
	if root == "" {
		return nil, nil
	}

	sessionsDir := filepath.Join(root, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		// sessions dir absent (or unreadable) — not an error, just no sessions.
		return nil, nil //nolint:nilerr // absent home is not an error
	}

	var sessions []Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, ok := readSession(filepath.Join(sessionsDir, e.Name()), e.Name())
		if ok {
			sessions = append(sessions, s)
		}
	}

	sortByLastActivityDesc(sessions)
	return sessions, nil
}

// resolveRovodevHome returns the Rovo Dev home directory. When rovodevHome is
// empty it defaults to ~/.rovodev. Returns "" when the home dir cannot be
// determined.
func resolveRovodevHome(rovodevHome string) string {
	if rovodevHome != "" {
		return rovodevHome
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".rovodev")
}

// readSession summarizes a single sessions/<uuid> directory. ok is false when
// the directory holds none of the recognised session files (i.e. it is not a
// session directory).
func readSession(dir, id string) (Session, bool) {
	metaPath := filepath.Join(dir, "metadata.json")
	ctxPath := filepath.Join(dir, "session_context.json")

	lastActivity, hasFile := latestModTime(metaPath, ctxPath)
	if !hasFile {
		return Session{}, false
	}

	s := Session{ID: id, LastActivity: lastActivity}

	if meta, ok := readMetadata(metaPath); ok {
		s.Title = truncateTitle(meta.Title)
		s.Dir = canonical(meta.WorkspacePath)
		return s, true
	}

	// Fallback (rare): no metadata.json. Parse the large context file for just
	// the workspace path, initial prompt, and created timestamp. This full parse
	// is bounded to the handful of metadata-less directories, never the hot path.
	if ctx, ok := readContextFallback(ctxPath); ok {
		s.Dir = canonical(ctx.WorkspacePath)
		s.Title = truncateTitle(ctx.InitialPrompt)
		if t, ok := parseTimestamp(ctx.Timestamp); ok {
			s.Created = t
		}
	}
	return s, true
}

// metadata mirrors the small ~/.rovodev/sessions/<uuid>/metadata.json file.
type metadata struct {
	Title         string `json:"title"`
	WorkspacePath string `json:"workspace_path"`
}

// readMetadata reads and parses metadata.json. ok is false when the file is
// absent or unparseable.
func readMetadata(path string) (metadata, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return metadata{}, false
	}
	var m metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return metadata{}, false
	}
	return m, true
}

// contextFallback is the subset of session_context.json read only when
// metadata.json is missing.
type contextFallback struct {
	WorkspacePath string `json:"workspace_path"`
	InitialPrompt string `json:"initial_prompt"`
	Timestamp     string `json:"timestamp"`
}

// readContextFallback reads and parses session_context.json. It performs a full
// JSON parse (the file may be large), so callers must only reach it for the rare
// metadata-less session directory.
func readContextFallback(path string) (contextFallback, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return contextFallback{}, false
	}
	var c contextFallback
	if err := json.Unmarshal(data, &c); err != nil {
		return contextFallback{}, false
	}
	return c, true
}

// latestModTime returns the most-recent ModTime among the given paths and
// whether any of them existed. Missing paths are skipped silently.
func latestModTime(paths ...string) (time.Time, bool) {
	var latest time.Time
	var found bool
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		found = true
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest, found
}

// canonical returns the pathnorm.Canonical form of p, or "" when p is empty or
// cannot be canonicalized.
func canonical(p string) string {
	if p == "" {
		return ""
	}
	if c, err := pathnorm.Canonical(p); err == nil {
		return c
	}
	return ""
}

// truncateTitle keeps the first line of s, trimmed to at most maxTitleRunes
// runes with an ellipsis when truncated.
func truncateTitle(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxTitleRunes {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:maxTitleRunes])) + "…"
}

// parseTimestamp parses an ISO 8601 / RFC 3339 timestamp string.
func parseTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// sortByLastActivityDesc sorts sessions in-place, most-recent first.
func sortByLastActivityDesc(sessions []Session) {
	// Insertion sort keeps the dependency surface minimal; session counts are
	// modest (one directory per session).
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && sessions[j].LastActivity.After(sessions[j-1].LastActivity); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
}
