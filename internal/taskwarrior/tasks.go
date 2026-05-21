// Package taskwarrior provides a thin client over the `task` CLI binary.
//
// # Single-instance assumption
//
// The client operates against a single Taskwarrior database. The data
// directory, RC file, and context are determined by the environment of the
// cogitator process itself: $TASKDATA, $TASKRC, and $HOME/.task are all
// inherited as-is. No isolation or multi-instance routing is performed.
//
// # Binary resolution
//
// The `task` binary is resolved via $PATH at the time [NewClient] is called,
// using the same $PATH that the cogitator process sees. If `task` is not on
// $PATH, [Client.Available] returns false and all operations return an error.
//
// # Undo and confirmation
//
// [Client.Undo] passes rc.confirmation:no to suppress the interactive prompt.
// Very old Taskwarrior versions (pre-2.4) may still prompt despite this flag;
// handling that case is out of scope.
package taskwarrior

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// isoBasicLayout is the date format Taskwarrior uses in its JSON export.
// Example: "20260601T120000Z"
const isoBasicLayout = "20060102T150405Z"

// runner is the function signature for executing the task binary.
// Separating it from Client allows tests to inject a fake without shelling out.
type runner func(ctx context.Context, args ...string) (stdout, stderr []byte, err error)

// Client wraps the `task` CLI. Obtain one via [NewClient].
// The zero value is safe to call (Available returns false).
type Client struct {
	bin string
	run runner
}

// NewClient resolves the `task` binary via exec.LookPath and returns a Client.
// If `task` is not found, the returned Client is non-nil but Available returns
// false; callers should check Available before use.
func NewClient() *Client {
	c := &Client{}
	if bin, err := exec.LookPath("task"); err == nil {
		c.bin = bin
	}
	c.run = c.defaultRunner
	return c
}

// Available reports whether the client has a usable `task` binary.
func (c *Client) Available() bool {
	return c != nil && c.bin != ""
}

// defaultRunner is the production runner: shells out via exec.CommandContext
// and captures both stdout and stderr.
func (c *Client) defaultRunner(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, c.bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// taskRaw is the JSON shape emitted by `task export`. Fields not present in
// the export are left at their zero values.
type taskRaw struct {
	ID          json.Number `json:"id"`
	UUID        string      `json:"uuid"`
	Description string      `json:"description"`
	Project     string      `json:"project"`
	Tags        []string    `json:"tags"`
	Priority    string      `json:"priority"`
	Due         string      `json:"due"`
	Urgency     float64     `json:"urgency"`
	Status      string      `json:"status"`
}

// TaskView is the parsed, display-ready representation of a Taskwarrior task.
// ID is the short numeric identifier used in CLI calls; UUID is the stable
// identifier. Due is zero if the task has no due date.
type TaskView struct {
	ID          string
	UUID        string
	Description string
	Project     string
	Tags        []string
	Priority    string
	Due         time.Time
	Urgency     float64
	Status      string
}

// parseISOBasic parses a Taskwarrior ISO-basic date string ("20060102T150405Z").
// Returns the zero time if s is empty or unparseable.
func parseISOBasic(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(isoBasicLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// toTaskView converts a raw JSON task record into a TaskView.
func toTaskView(r taskRaw) TaskView {
	id := r.ID.String()
	// Normalise: if the JSON number is "0" (task not yet committed), keep it.
	// Strip any decimal suffix that json.Number may produce for whole numbers.
	if i, err := strconv.ParseInt(id, 10, 64); err == nil {
		id = strconv.FormatInt(i, 10)
	}
	return TaskView{
		ID:          id,
		UUID:        r.UUID,
		Description: r.Description,
		Project:     r.Project,
		Tags:        r.Tags,
		Priority:    r.Priority,
		Due:         parseISOBasic(r.Due),
		Urgency:     r.Urgency,
		Status:      r.Status,
	}
}

// Export fetches all pending tasks from Taskwarrior and returns them as
// TaskViews. An empty result set is not an error.
func (c *Client) Export(ctx context.Context) ([]TaskView, error) {
	if !c.Available() {
		return nil, fmt.Errorf("task: binary not found")
	}
	args := []string{
		"rc.json.array=on",
		"rc.context:",
		"status:pending",
		"export",
	}
	stdout, stderr, err := c.run(ctx, args...)
	if err != nil {
		return nil, wrapErr(ctx, "export", err, stderr)
	}

	// Taskwarrior may emit an empty array or nothing at all.
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[]")) {
		return []TaskView{}, nil
	}

	var raws []taskRaw
	if err := json.Unmarshal(trimmed, &raws); err != nil {
		return nil, fmt.Errorf("task export: parse JSON: %w", err)
	}

	views := make([]TaskView, len(raws))
	for i, r := range raws {
		views[i] = toTaskView(r)
	}
	return views, nil
}

// Add creates a new task from a DSL string (e.g. "buy milk project:home +errand").
// Quoted spans in dsl are preserved as single tokens.
func (c *Client) Add(ctx context.Context, dsl string) error {
	if !c.Available() {
		return fmt.Errorf("task: binary not found")
	}
	args := append(writeFlags(), "add")
	args = append(args, tokenizeDSL(dsl)...)
	_, stderr, err := c.run(ctx, args...)
	if err != nil {
		return wrapErr(ctx, "add", err, stderr)
	}
	return nil
}

// Modify applies a DSL string to an existing task identified by id.
func (c *Client) Modify(ctx context.Context, id, dsl string) error {
	if !c.Available() {
		return fmt.Errorf("task: binary not found")
	}
	args := append(writeFlags(), id, "modify")
	args = append(args, tokenizeDSL(dsl)...)
	_, stderr, err := c.run(ctx, args...)
	if err != nil {
		return wrapErr(ctx, "modify", err, stderr)
	}
	return nil
}

// Done marks a task as completed.
func (c *Client) Done(ctx context.Context, id string) error {
	if !c.Available() {
		return fmt.Errorf("task: binary not found")
	}
	args := append(writeFlags(), id, "done")
	_, stderr, err := c.run(ctx, args...)
	if err != nil {
		return wrapErr(ctx, "done", err, stderr)
	}
	return nil
}

// Delete removes a task permanently.
func (c *Client) Delete(ctx context.Context, id string) error {
	if !c.Available() {
		return fmt.Errorf("task: binary not found")
	}
	args := append(writeFlags(), id, "delete")
	_, stderr, err := c.run(ctx, args...)
	if err != nil {
		return wrapErr(ctx, "delete", err, stderr)
	}
	return nil
}

// Undo reverses the most recent Taskwarrior operation.
// rc.confirmation:no suppresses the interactive prompt on modern Taskwarrior;
// very old versions may still prompt — that case is out of scope.
func (c *Client) Undo(ctx context.Context) error {
	if !c.Available() {
		return fmt.Errorf("task: binary not found")
	}
	args := append(writeFlags(), "undo")
	_, stderr, err := c.run(ctx, args...)
	if err != nil {
		return wrapErr(ctx, "undo", err, stderr)
	}
	return nil
}

// writeFlags returns the standard rc overrides prepended to every write command.
// rc.confirmation:no prevents interactive prompts.
// rc.context: clears any active context filter so writes are unconditional.
func writeFlags() []string {
	return []string{"rc.confirmation:no", "rc.context:"}
}

// wrapErr wraps a runner error with verb and stderr context.
// If the context was cancelled or timed out, that error is returned directly
// so the UI can distinguish timeouts from task failures.
func wrapErr(ctx context.Context, verb string, err error, stderr []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("task %s: %w (stderr: %s)", verb, err, bytes.TrimSpace(stderr))
}

// tokenizeDSL splits a Taskwarrior DSL string into tokens, respecting
// double-quoted spans. A quoted span becomes a single token (without the
// surrounding quotes). Unquoted whitespace is the delimiter.
//
// Examples:
//
//	"buy milk project:home"       → ["buy", "milk", "project:home"]
//	`"two words" project:foo`     → ["two words", "project:foo"]
//	`description:"long name" +tag` → ["description:long name", "+tag"]
func tokenizeDSL(s string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false

	for _, r := range s {
		switch {
		case r == '"':
			// Toggle quoted mode; do not include the quote character itself.
			inQuote = !inQuote
		case !inQuote && unicode.IsSpace(r):
			// Delimiter outside a quoted span: flush the current token.
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}
