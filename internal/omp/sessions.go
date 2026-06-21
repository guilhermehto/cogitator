// Package omp provides a reader for Oh My Pi (omp) coding-agent session
// transcript files.
//
// Session files live at:
//
//	<agent-dir>/sessions/<dir-encoded>/<timestamp>_<sessionId>.jsonl
//
// where <agent-dir> defaults to ~/.omp/agent and is overridable via
// $PI_CODING_AGENT_DIR (or $PI_CONFIG_DIR/agent). Each line is a JSON object.
// Line 1 is always the session header (type "session"); its fields carry the
// session id, cwd, title, and creation timestamp. When the header has no
// title, one is derived from the first user "message" entry. Last-activity is
// the timestamp of the final parseable line.
//
// The package is intentionally free of internal/ui, bubbletea, internal/oc,
// and internal/state — it is a pure filesystem-in / structs-out parser.
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
	// ID is the session id from the header (matches the id suffix in the
	// filename and the id the shipped extension reports over the hook IPC).
	ID string

	// Dir is the working directory from the header, normalized via
	// pathnorm.Canonical so it reconciles with worktree and roster paths.
	Dir string

	// Title is the header title, or the first user message when the header
	// has none. Empty when neither is available.
	Title string

	// Created is the session initiation time from the header.
	Created time.Time

	// LastActivity is the timestamp of the last parseable line in the file.
	LastActivity time.Time
}

// maxTitleRunes is the maximum number of Unicode code points kept in a
// derived title. Long prompts are truncated with an ellipsis.
const maxTitleRunes = 80

// ReadSessions walks <agent-dir>/sessions/**/*.jsonl, parses each file, and
// returns the resulting sessions ordered by LastActivity descending.
//
// ompHome, when non-empty, is the agent directory (the parent of sessions/).
// When empty it is resolved from $PI_CODING_AGENT_DIR, then $PI_CONFIG_DIR/agent,
// then ~/.omp/agent.
//
// Tolerances:
//   - agent dir empty/absent → empty slice, nil error.
//   - A file that disappears between listing and open → silently skipped.
//   - Malformed JSON lines → skipped; the session is still returned if the
//     header was already parsed.
//   - A file whose first parseable line is not a session header → skipped
//     entirely (this excludes subagent transcripts and other non-session
//     JSONL, mirroring omp's own loader).
func ReadSessions(ompHome string) ([]Session, error) {
	agentDir, err := resolveOmpAgentDir(ompHome)
	if err != nil || agentDir == "" {
		return nil, nil //nolint:nilerr // absent/empty home is not an error
	}

	sessionsDir := filepath.Join(agentDir, "sessions")
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

// resolveOmpAgentDir returns the absolute path to the omp agent directory (the
// parent of sessions/). Precedence: explicit ompHome, then $PI_CODING_AGENT_DIR,
// then $PI_CONFIG_DIR/agent, then ~/.omp/agent. Returns ("", nil) when no home
// directory can be determined.
func resolveOmpAgentDir(ompHome string) (string, error) {
	if ompHome != "" {
		return ompHome, nil
	}
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("PI_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "agent"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil //nolint:nilerr // no home dir → treat as absent
	}
	return filepath.Join(home, ".omp", "agent"), nil
}

// isSessionFile reports whether name is a session JSONL file.
func isSessionFile(name string) bool {
	return strings.HasSuffix(name, ".jsonl")
}

// parseSessionFile reads a single session JSONL file and returns the parsed
// Session. ok is false when the file cannot be opened or its first parseable
// line is not a valid session header.
func parseSessionFile(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		// File removed between listing and open — tolerated.
		return Session{}, false
	}
	defer f.Close()

	var (
		s            Session
		seenHeader   bool
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

		if !seenHeader {
			// The first parseable line MUST be the session header; anything
			// else means this is not a session file (e.g. a subagent
			// transcript) and is rejected wholesale.
			if raw.Type != "session" || raw.ID == "" {
				return Session{}, false
			}
			seenHeader = true
			created, _ := parseTimestamp(raw.Timestamp)
			dir, _ := pathnorm.Canonical(raw.CWD)
			s = Session{
				ID:      raw.ID,
				Dir:     dir,
				Title:   truncateTitle(strings.TrimSpace(raw.Title)),
				Created: created,
			}
			lastActivity = created
			continue
		}

		if ts, ok := parseTimestamp(raw.Timestamp); ok {
			lastActivity = ts
		}

		// Derive a title from the first user message only when the header
		// carried none.
		if s.Title == "" && raw.Type == "message" && len(raw.Message) > 0 {
			if title := userMessageTitle(raw.Message); title != "" {
				s.Title = truncateTitle(title)
			}
		}
	}
	// scanner.Err() is intentionally ignored: a truncated/partial final line
	// is already handled by the json.Unmarshal skip above; a read error after
	// a valid header still yields a usable session.

	if !seenHeader {
		return Session{}, false
	}

	s.LastActivity = lastActivity
	return s, true
}

// userMessageTitle extracts the first text from a "message" entry payload when
// its role is "user". Returns "" for non-user messages or empty content.
// content may be a plain string or an array of {type,text} content blocks.
func userMessageTitle(payload json.RawMessage) string {
	var m msgPayload
	if err := json.Unmarshal(payload, &m); err != nil {
		return ""
	}
	if m.Role != "user" || len(m.Content) == 0 {
		return ""
	}

	// content as a plain string.
	var asString string
	if err := json.Unmarshal(m.Content, &asString); err == nil {
		return strings.TrimSpace(asString)
	}

	// content as an array of blocks; return the first non-empty text.
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return strings.TrimSpace(b.Text)
			}
		}
	}
	return ""
}

// truncateTitle trims s to at most maxTitleRunes runes, appending "…" when
// truncated. The first line only is kept.
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
	// small (one file per worktree session).
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && sessions[j].LastActivity.After(sessions[j-1].LastActivity); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
}

// rawLine is the top-level shape of every session JSONL line. It carries both
// header fields (id/cwd/title) and the message-entry payload so a single
// unmarshal serves both.
type rawLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	ID        string          `json:"id"`
	CWD       string          `json:"cwd"`
	Title     string          `json:"title"`
	Message   json.RawMessage `json:"message"`
}

// msgPayload is the relevant subset of a "message" entry's message object.
type msgPayload struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock is one block of an array-form message content.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
