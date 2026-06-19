// Package omp provides a reader for Oh My Pi (omp) session transcript files
// and a discovery provider that feeds the cogitator sessions view.
//
// Session files live at:
//
//	$PI_CODING_AGENT_DIR/sessions/<dir-encoded>/<timestamp>_<sessionId>.jsonl
//
// PI_CODING_AGENT_DIR defaults to ~/.omp/agent when empty. Each line is a JSON
// object; line 1 is always the session header:
//
//	{"type":"session","id":"<id>","timestamp":"<ISO8601>","cwd":"<path>","title":"..."}
//
// The header carries the session id, cwd, created timestamp, and (once title
// generation runs) the title. When the header has no title, the title is
// derived from the first user "message" entry. Last-activity is the timestamp
// of the final parseable line.
//
// The package is intentionally free of internal/ui, bubbletea, internal/oc,
// and internal/state — it is a pure filesystem-in / structs-out parser plus a
// sink-out provider.
package omp

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

// Session holds the parsed summary of a single omp session transcript.
type Session struct {
	// ID is the session id from the header line.
	ID string

	// Dir is the working directory from the header, normalized via
	// pathnorm.Canonical so it reconciles with worktree and roster paths.
	Dir string

	// Title is the header title, or — when that is empty — derived from the
	// first user message in the transcript. Empty when neither is present.
	Title string

	// Created is the session initiation time from the header.
	Created time.Time

	// LastActivity is the timestamp of the last parseable line in the file.
	LastActivity time.Time
}

// maxTitleRunes is the maximum number of Unicode code points kept in a
// derived title. Long prompts are truncated with an ellipsis.
const maxTitleRunes = 80

// ReadSessions walks $home/sessions/**/*.jsonl, parses each file, and returns
// the resulting sessions ordered by LastActivity descending (most-recent
// first).
//
// Tolerances:
//   - home empty or pointing at a non-existent directory → empty slice, nil
//     error.
//   - A session file that disappears between directory listing and open →
//     silently skipped.
//   - Malformed or partial JSON lines → skipped; the session is still returned
//     if the header line was already parsed.
//   - A file whose first line is not a valid session header → skipped entirely.
func ReadSessions(home string) ([]Session, error) {
	root, err := resolveOMPHome(home)
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
		if !isSessionFile(d.Name()) {
			return nil
		}
		s, ok := parseSessionFile(path)
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

// resolveOMPHome returns the absolute path to the omp agent directory. When
// home is empty it falls back to $PI_CODING_AGENT_DIR, then ~/.omp/agent.
// Returns ("", nil) when no home directory can be determined.
func resolveOMPHome(home string) (string, error) {
	if home != "" {
		return home, nil
	}
	if env := os.Getenv("PI_CODING_AGENT_DIR"); env != "" {
		return env, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", nil //nolint:nilerr // no home dir → treat as absent
	}
	return filepath.Join(h, ".omp", "agent"), nil
}

// isSessionFile reports whether name is an omp session transcript
// (<timestamp>_<sessionId>.jsonl).
func isSessionFile(name string) bool {
	return strings.HasSuffix(name, ".jsonl") && strings.Contains(name, "_")
}

// SessionIDFromFilename extracts the session id from an omp session file name
// of the form "<timestamp>_<sessionId>.jsonl". It returns "" when name does
// not match that shape. The timestamp segment never contains "_", so splitting
// on the first "_" isolates the id.
func SessionIDFromFilename(name string) string {
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, ".jsonl")
	_, id, found := strings.Cut(base, "_")
	if !found || id == "" {
		return ""
	}
	return id
}

// parseSessionFile reads a single omp session JSONL file and returns the
// parsed Session. ok is false when the file cannot be opened or its first line
// is not a valid session header.
func parseSessionFile(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		// File removed between WalkDir listing and open — tolerated.
		return Session{}, false
	}
	defer f.Close()

	var (
		s            Session
		hasHeader    bool
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

		if ts, ok := parseTimestamp(raw.Timestamp); ok {
			lastActivity = ts
		}

		switch raw.Type {
		case "session":
			if !hasHeader {
				created, _ := parseTimestamp(raw.Timestamp)
				dir, _ := pathnorm.Canonical(raw.CWD)
				s = Session{
					ID:      raw.ID,
					Dir:     dir,
					Title:   strings.TrimSpace(raw.Title),
					Created: created,
				}
				hasHeader = true
			}

		case "message":
			if s.Title != "" {
				break
			}
			text := firstUserText(raw.Message)
			if text != "" {
				s.Title = truncateTitle(strings.TrimSpace(text))
			}
		}
	}
	// scanner.Err() is intentionally ignored: a truncated/partial final line is
	// already handled by the json.Unmarshal skip above; a read error after a
	// valid header still yields a usable session.

	if !hasHeader || s.ID == "" {
		return Session{}, false
	}

	s.LastActivity = lastActivity
	return s, true
}

// firstUserText returns the concatenated text of a user message entry, or ""
// when the entry is not a user message or carries no text content.
func firstUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m messageEntry
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if m.Role != "user" {
		return ""
	}
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" && c.Text != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(c.Text)
		}
	}
	return b.String()
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
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

// sortByLastActivityDesc sorts sessions in-place, most-recent first.
func sortByLastActivityDesc(sessions []Session) {
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && sessions[j].LastActivity.After(sessions[j-1].LastActivity); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
}

// rawLine is the top-level shape of every omp session JSONL line. Header lines
// (type "session") carry id/cwd/title inline; entry lines carry a message.
type rawLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	ID        string          `json:"id"`
	CWD       string          `json:"cwd"`
	Title     string          `json:"title"`
	Message   json.RawMessage `json:"message"`
}

// messageEntry is the "message" payload of a message line.
type messageEntry struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}
