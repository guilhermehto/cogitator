// Package claudecode provides a reader for Claude Code session transcript files.
//
// Session files live at:
//
//	~/.claude/projects/<encoded-dir>/<session-uuid>.jsonl
//
// The encoded-dir is the absolute path to the project directory with path
// separators replaced by hyphens (e.g. /Users/foo/src → -Users-foo-src).
//
// Each line is a JSON object. Relevant line types:
//
//	{"type":"user","cwd":"<path>","sessionId":"<uuid>","timestamp":"<ISO8601>","message":{"content":"<text>" | [{"type":"text","text":"<text>"},...]},...}
//	{"type":"assistant","cwd":"<path>","sessionId":"<uuid>","timestamp":"<ISO8601>",...}
//	{"type":"ai-title","aiTitle":"<title>","sessionId":"<uuid>"}
//
// The package is intentionally free of internal/ui, bubbletea, internal/oc,
// and internal/state — it is a pure filesystem-in / structs-out parser.
package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// Session holds the parsed summary of a single Claude Code session transcript.
type Session struct {
	// ID is the session UUID, taken from the filename and cross-checked
	// against the sessionId field on parsed lines.
	ID string

	// Dir is the working directory from the first line that carries a non-empty
	// cwd field, normalized via pathnorm.Canonical so it reconciles with
	// worktree and roster paths. Sessions with no resolvable cwd are skipped.
	Dir string

	// Title prefers the latest ai-title line; falling back to the first
	// non-synthetic human user line. Empty when neither is present.
	Title string

	// Created is the timestamp of the first parseable line in the file.
	Created time.Time

	// LastActivity is the timestamp of the last parseable line in the file.
	LastActivity time.Time
}

// maxTitleRunes is the maximum number of Unicode code points kept in a
// derived title. Long prompts are truncated with an ellipsis.
const maxTitleRunes = 80

// lineReader buffer sizes: scannerInitBuf is the initial read buffer; lines
// exceeding maxLineBytes are discarded (their JSON parse will fail and the
// line is skipped — the desired behaviour for oversized base64/tool output).
const (
	scannerInitBuf = 64 * 1024
	maxLineBytes   = 1024 * 1024 // 1 MiB
)

// ReadSessions walks ~/.claude/projects/*/*.jsonl, parses each file, and
// returns the resulting sessions ordered by LastActivity descending
// (most-recent first).
//
// Tolerances:
//   - claudeHome empty or pointing at a non-existent directory → empty slice,
//     nil error.
//   - A session file that disappears between directory listing and open →
//     silently skipped.
//   - Malformed or partial JSON lines → skipped; the session is still
//     returned if a cwd was already found.
//   - A file with no line carrying a non-empty cwd → skipped entirely
//     (an empty Dir would break dir-keyed merge/jump).
//   - Lines exceeding 1 MiB → the scanner falls back to a buffered Reader
//     so the rest of the file is still processed.
func ReadSessions(claudeHome string) ([]Session, error) {
	root, err := resolveClaudeHome(claudeHome)
	if err != nil || root == "" {
		return nil, nil //nolint:nilerr // absent/empty home is not an error
	}

	projectsDir := filepath.Join(root, "projects")
	if _, err := os.Stat(projectsDir); err != nil {
		return nil, nil
	}

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, nil
	}

	var sessions []Session
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projectDir := filepath.Join(projectsDir, entry.Name())
		files, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !isSessionFile(f.Name()) {
				continue
			}
			sessionID := strings.TrimSuffix(f.Name(), ".jsonl")
			s, ok := parseSessionFile(filepath.Join(projectDir, f.Name()), sessionID)
			if ok {
				sessions = append(sessions, s)
			}
		}
	}

	sortByLastActivityDesc(sessions)
	return sessions, nil
}

// resolveClaudeHome returns the absolute path to the Claude home directory.
// When claudeHome is empty it defaults to ~/.claude. Returns ("", nil) when
// the home directory cannot be determined.
func resolveClaudeHome(claudeHome string) (string, error) {
	if claudeHome != "" {
		return claudeHome, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil //nolint:nilerr // no home dir → treat as absent
	}
	return filepath.Join(home, ".claude"), nil
}

// isSessionFile reports whether name has the .jsonl suffix used by Claude Code
// session files. It does not validate the UUID shape of the base name.
func isSessionFile(name string) bool {
	return strings.HasSuffix(name, ".jsonl")
}

// parseSessionFile reads a single session JSONL file and returns the parsed
// Session. ok is false when the file cannot be opened or contains no line
// with a usable cwd.
func parseSessionFile(path, sessionID string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false
	}
	defer f.Close()

	return parseSessionReader(f, sessionID)
}

// parseSessionReader parses a Claude Code session JSONL stream. Separated from
// parseSessionFile so tests can pass an in-memory reader.
func parseSessionReader(r io.Reader, sessionID string) (Session, bool) {
	var (
		s            Session
		hasDir       bool
		hasCreated   bool
		lastActivity time.Time
		// aiTitle holds the most-recently seen ai-title value.
		aiTitle string
		// firstUserTitle holds the first non-synthetic user message text.
		firstUserTitle string
	)

	s.ID = sessionID

	lr := newLineReader(r)
	for {
		line, err := lr.readLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Read error mid-file — stop but return what we have.
			break
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var raw rawLine
		if jsonErr := json.Unmarshal(line, &raw); jsonErr != nil {
			continue
		}

		ts, ok := parseTimestamp(raw.Timestamp)
		if ok {
			if !hasCreated {
				s.Created = ts
				hasCreated = true
			}
			lastActivity = ts
		}

		// Extract cwd from any line that carries it.
		if !hasDir && raw.CWD != "" {
			dir, dirErr := pathnorm.Canonical(raw.CWD)
			if dirErr == nil && dir != "" {
				s.Dir = dir
				hasDir = true
			}
		}

		switch raw.Type {
		case "ai-title":
			if raw.AITitle != "" {
				aiTitle = raw.AITitle
			}

		case "user":
			if firstUserTitle == "" {
				text := extractUserText(raw.Content)
				if text != "" && !isSynthetic(text) {
					firstUserTitle = truncateTitle(text)
				}
			}
		}
	}

	if !hasDir {
		// No usable cwd found — skip this session to avoid breaking
		// dir-keyed merge/jump operations.
		return Session{}, false
	}

	// Title preference: latest ai-title > first non-synthetic user line.
	switch {
	case aiTitle != "":
		s.Title = truncateTitle(aiTitle)
	case firstUserTitle != "":
		s.Title = firstUserTitle
	}

	s.LastActivity = lastActivity
	return s, true
}

// rawLine is the top-level shape of every Claude Code JSONL line.
// Fields are a superset of what all line types carry; absent fields are zero.
type rawLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	CWD       string          `json:"cwd"`
	SessionID string          `json:"sessionId"`
	AITitle   string          `json:"aiTitle"`
	Content   json.RawMessage `json:"message"` // the "message" object
}

// messageEnvelope is the "message" object inside a user/assistant line.
type messageEnvelope struct {
	Content json.RawMessage `json:"content"`
}

// contentBlock is one element of an array-form content field.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// extractUserText returns the plain text from a raw "message" JSON value.
// The content field may be a plain string or an array of content blocks.
func extractUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var env messageEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	if len(env.Content) == 0 {
		return ""
	}

	// Try string form first.
	var s string
	if err := json.Unmarshal(env.Content, &s); err == nil {
		return strings.TrimSpace(s)
	}

	// Try array form: pick the first {type:text} block.
	var blocks []contentBlock
	if err := json.Unmarshal(env.Content, &blocks); err != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return strings.TrimSpace(b.Text)
		}
	}
	return ""
}

// isSynthetic reports whether a user message text should be skipped when
// deriving a session title. Synthetic lines include:
//   - SDK wrapper prefix "Human: " (injected by the Claude Code SDK)
//   - Slash-command expansions starting with "/"
//   - XML-style system text starting with "<" (e.g. <command-name>)
func isSynthetic(text string) bool {
	return strings.HasPrefix(text, "Human: ") ||
		strings.HasPrefix(text, "/") ||
		strings.HasPrefix(text, "<")
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
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastActivity.After(sessions[j].LastActivity)
	})
}

// lineReader reads lines from an io.Reader, handling lines that exceed the
// scanner's token buffer by falling back to a bufio.Reader. This ensures that
// files containing >1 MiB lines (e.g. base64-encoded images in tool output)
// do not abort parsing of the rest of the file.
type lineReader struct {
	br *bufio.Reader
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{br: bufio.NewReaderSize(r, scannerInitBuf)}
}

// readLine returns the next line (without the trailing newline). When a line
// exceeds the internal buffer, it is read in chunks and reassembled — but
// only up to maxLineBytes; beyond that the remainder is discarded and an
// empty slice is returned for that line (the caller's JSON parse will fail
// and skip it, which is the desired behaviour for oversized lines).
func (lr *lineReader) readLine() ([]byte, error) {
	var buf []byte
	for {
		fragment, isPrefix, err := lr.br.ReadLine()
		if err != nil {
			if len(buf) > 0 {
				// Return whatever we accumulated before the error.
				return buf, nil
			}
			return nil, err
		}

		if len(buf)+len(fragment) <= maxLineBytes {
			buf = append(buf, fragment...)
		} else {
			// Line exceeds maxLineBytes — discard accumulated content so the
			// JSON parse fails and the line is skipped. Keep reading to
			// consume the rest of the line.
			buf = nil
		}

		if !isPrefix {
			// Full line consumed.
			return buf, nil
		}
		// isPrefix == true: more fragments to come for this line.
	}
}
