// Package codex provides a reader for Codex CLI rollout transcript files.
//
// Rollout files live at:
//
//	$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<ISO8601>-<session-uuid>.jsonl
//
// CODEX_HOME defaults to ~/.codex when empty. Each line is a JSON object:
//
//	{"timestamp":"<ISO8601>","type":"<t>","payload":{...}}
//
// Line 1 is always type "session_meta"; its payload carries the session id,
// cwd, and created timestamp. The title is derived from the first
// "event_msg" line whose payload.type == "user_message". Last-activity is
// the timestamp of the final parseable line.
//
// The package is intentionally free of internal/ui, bubbletea, internal/oc,
// and internal/state — it is a pure filesystem-in / structs-out parser.
package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// Session holds the parsed summary of a single Codex rollout transcript.
type Session struct {
	// ID is the session UUID from the session_meta payload.
	ID string

	// Dir is the working directory from the session_meta payload,
	// normalized via pathnorm.Canonical so it reconciles with worktree
	// and roster paths.
	Dir string

	// Title is derived from the first user_message in the transcript.
	// Empty when no user_message line exists.
	Title string

	// Created is the session initiation time from the session_meta payload.
	Created time.Time

	// LastActivity is the timestamp of the last parseable line in the file.
	LastActivity time.Time
}

// maxTitleRunes is the maximum number of Unicode code points kept in a
// derived title. Long prompts are truncated with an ellipsis.
const maxTitleRunes = 80

// ReadSessions walks $codexHome/sessions/**/rollout-*.jsonl, parses each
// file, and returns the resulting sessions. The returned slice is ordered
// by LastActivity descending (most-recent first).
//
// Tolerances:
//   - codexHome empty or pointing at a non-existent directory → empty slice,
//     nil error.
//   - A rollout file that disappears between directory listing and open →
//     silently skipped.
//   - Malformed or partial JSON lines → skipped; the session is still
//     returned if the meta line was already parsed.
//   - A file with no parseable session_meta line → skipped entirely.
func ReadSessions(codexHome string) ([]Session, error) {
	root, err := resolveCodexHome(codexHome)
	if err != nil || root == "" {
		return nil, nil //nolint:nilerr // absent/empty home is not an error
	}

	sessionsDir := filepath.Join(root, "sessions")
	if _, err := os.Stat(sessionsDir); err != nil {
		// sessions dir absent — not an error, just no sessions yet.
		return nil, nil
	}

	var sessions []Session
	err = filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A directory or file that disappeared mid-walk — skip it.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !isRolloutFile(d.Name()) {
			return nil
		}
		s, ok := parseRolloutFile(path)
		if ok {
			sessions = append(sessions, s)
		}
		return nil
	})
	if err != nil {
		// WalkDir itself errored (e.g. sessions dir removed after Stat) —
		// return whatever we collected rather than failing.
		return sessions, nil
	}

	sortByLastActivityDesc(sessions)
	return sessions, nil
}

// resolveCodexHome returns the absolute path to CODEX_HOME. When codexHome is
// empty it defaults to ~/.codex. Returns ("", nil) when the home directory
// cannot be determined.
func resolveCodexHome(codexHome string) (string, error) {
	if codexHome != "" {
		return codexHome, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil //nolint:nilerr // no home dir → treat as absent
	}
	return filepath.Join(home, ".codex"), nil
}

// isRolloutFile reports whether name matches the rollout-*.jsonl pattern.
func isRolloutFile(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

// parseRolloutFile reads a single rollout JSONL file and returns the parsed
// Session. ok is false when the file cannot be opened or contains no valid
// session_meta line.
func parseRolloutFile(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		// File removed between WalkDir listing and open — tolerated.
		return Session{}, false
	}
	defer f.Close()

	var (
		s            Session
		hasMeta      bool
		lastActivity time.Time
	)

	scanner := bufio.NewScanner(f)
	// Increase the buffer for long lines (e.g. large agent messages).
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw rawLine
		if err := json.Unmarshal(line, &raw); err != nil {
			// Malformed or partial line — skip without error.
			continue
		}

		ts, ok := parseTimestamp(raw.Timestamp)
		if !ok {
			continue
		}
		lastActivity = ts

		switch raw.Type {
		case "session_meta":
			var p metaPayload
			if err := json.Unmarshal(raw.Payload, &p); err != nil {
				continue
			}
			created, _ := parseTimestamp(p.Timestamp)
			dir, _ := pathnorm.Canonical(p.CWD)
			s = Session{
				ID:      p.ID,
				Dir:     dir,
				Created: created,
			}
			hasMeta = true

		case "event_msg":
			if s.Title != "" {
				break
			}
			var p eventMsgPayload
			if err := json.Unmarshal(raw.Payload, &p); err != nil {
				continue
			}
			if p.Type == "user_message" && p.Message != "" {
				s.Title = truncateTitle(strings.TrimSpace(p.Message))
			}
		}
	}
	// scanner.Err() is intentionally ignored: a truncated/partial final line
	// is already handled by the json.Unmarshal skip above; a read error after
	// a valid meta line still yields a usable session.

	if !hasMeta {
		return Session{}, false
	}

	s.LastActivity = lastActivity
	return s, true
}

// truncateTitle trims s to at most maxTitleRunes runes, appending "…" when
// truncated.
func truncateTitle(s string) string {
	if utf8.RuneCountInString(s) <= maxTitleRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxTitleRunes]) + "…"
}

// parseTimestamp parses an ISO 8601 / RFC 3339 timestamp string.
func parseTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// Try without sub-second precision.
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

// sortByLastActivityDesc sorts sessions in-place, most-recent first.
func sortByLastActivityDesc(sessions []Session) {
	// Insertion sort is fine for the typical small number of sessions.
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && sessions[j].LastActivity.After(sessions[j-1].LastActivity); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
}

// rawLine is the top-level shape of every rollout JSONL line.
type rawLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// metaPayload is the payload of a session_meta line.
type metaPayload struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
}

// eventMsgPayload is the payload of an event_msg line.
type eventMsgPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
