// Package tmuxctl controls tmux windows on behalf of cogitator, tagging each
// window with the canonical worktree path it was opened for (@cog_dir option).
//
// # Design
//
// Every public function accepts a Runner — a thin interface over the tmux CLI —
// so the argv builders and output parsers are fully unit-testable without a
// live tmux server. The package-level helpers (EnsureWindow, FindWindowByDir,
// etc.) use DefaultRunner, which shells out to the real tmux binary.
//
// # Single-server assumption
//
// cogitator assumes it runs inside the single tmux server it should jump
// within. Multi-socket / multi-server setups are not supported: all tmux
// commands are issued without -L or -S flags, so they target the server
// identified by $TMUX (the default when inside a tmux session).
//
// # Availability gate
//
// Available() returns false when $TMUX is unset (i.e. the process is not
// running inside a tmux session). All operations that require tmux return
// mux.ErrNotAvailable in that case so callers can degrade gracefully.
//
// No import of bubbletea or internal/ui is permitted in this package.
package tmuxctl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// Runner is the interface through which tmuxctl issues tmux commands.
// The default implementation shells out to the real tmux binary; tests inject
// a fake that records calls and returns canned output.
//
// args is the full argument list passed to tmux (e.g. ["list-windows", "-a"]).
// The implementation must return the combined stdout of the command, or a
// non-nil error if the command fails.
type Runner interface {
	Run(args ...string) (string, error)
}

// RunnerFunc is a function that implements Runner. It is convenient for
// constructing inline fakes in tests.
type RunnerFunc func(args ...string) (string, error)

// Run implements Runner.
func (f RunnerFunc) Run(args ...string) (string, error) { return f(args...) }

// execRunner is the production Runner that shells out to the tmux binary.
type execRunner struct{}

// Run executes tmux with the given arguments and returns stdout.
func (execRunner) Run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	out, err := cmd.Output()
	if err != nil {
		// Include stderr in the error message when available.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// DefaultRunner is the package-level Runner used by the top-level helpers.
// Tests replace it by calling the With* variants directly.
var DefaultRunner Runner = execRunner{}

// Available reports whether cogitator is running inside a tmux session.
// It returns false when $TMUX is unset; all operations return mux.ErrNotAvailable
// in that case.
func Available() bool {
	return os.Getenv("TMUX") != ""
}

// EnsureWindowModeWith is the injectable variant of EnsureWindowMode.
func EnsureWindowModeWith(r Runner, dir, name string, argv []string, mode mux.LaunchMode) (mux.Target, error) {
	if !Available() {
		return "", mux.ErrNotAvailable
	}

	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		return "", fmt.Errorf("tmuxctl: canonicalize dir %q: %w", dir, err)
	}

	// Check whether a window already exists for this canonical dir.
	existing, err := FindWindowByDirWith(r, canonical)
	if err == nil {
		// Window already exists — return it without creating a new one.
		return existing, nil
	}
	if !errors.Is(err, mux.ErrWindowNotFound) {
		return "", fmt.Errorf("tmuxctl: find existing window: %w", err)
	}

	// No existing window — create one in the requested mode.
	if mode == mux.ModeSession {
		return newSessionWith(r, canonical, name, argv)
	}
	return newWindowWith(r, canonical, name, argv)
}

// newWindowWith creates a new tmux window running argv, names it name, and
// sets @cog_dir to canonical. Returns the mux.Target of the new window.
//
// remain-on-exit is set to "on" for the window so that when the harness
// process exits (e.g. after a crash or explicit quit), the window stays open
// in the "dead" state rather than being auto-closed by tmux. This is required
// for WindowProcessAlive to detect the dead state and for RelaunchInWindow to
// revive the process via respawn-pane.
func newWindowWith(r Runner, canonical, name string, argv []string) (mux.Target, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("tmuxctl: argv must not be empty")
	}

	// Build: tmux new-window -d -c <canonical> -n <name> -P -F '#{session_name}:#{window_index}' <argv...>
	// -d: do not switch to the new window (don't disturb the current window).
	// -c: set the working directory of the new window's pane to the canonical
	//     worktree path. This satisfies the harness contract (internal/harness/opencode.go)
	//     which requires CWD == worktree for the launched process.
	// -P -F: print the target of the new window so we can return it.
	args := []string{
		"new-window",
		"-d",
		"-c", canonical,
		"-n", name,
		"-P", "-F", "#{session_name}:#{window_index}",
	}
	args = append(args, argv...)

	out, err := r.Run(args...)
	if err != nil {
		return "", fmt.Errorf("tmuxctl: new-window: %w", err)
	}

	target := mux.Target(strings.TrimSpace(out))
	if target == "" {
		return "", fmt.Errorf("tmuxctl: new-window returned empty target")
	}

	// Enable remain-on-exit so the window survives process exit and
	// WindowProcessAlive / RelaunchInWindow can detect and revive it.
	if err := setOptionWith(r, target, "remain-on-exit", "on"); err != nil {
		return "", fmt.Errorf("tmuxctl: set remain-on-exit on %s: %w", target, err)
	}

	// Tag the window with the canonical worktree path.
	if err := setOptionWith(r, target, "@cog_dir", canonical); err != nil {
		return "", fmt.Errorf("tmuxctl: set @cog_dir on %s: %w", target, err)
	}

	return target, nil
}

// newSessionWith creates a new detached tmux session whose first window runs an
// interactive shell in canonical, then launches argv inside that shell. It
// names the session a sanitized form of name, sets @cog_dir to canonical, and
// returns the mux.Target ("session:index") of the session's first window.
//
// The harness is run via send-keys into the shell rather than as the pane's own
// command. This is what the user gets from a hand-made session: when the harness
// exits, the shell stays put at the worktree CWD instead of the pane dying or
// the (single-window) session collapsing. remain-on-exit is therefore left at
// its default (off) — the shell, not remain-on-exit, keeps the window alive.
//
// Because the shell persists, the dead-pane revival path (WindowProcessAlive /
// RelaunchInWindow) does not apply in session mode: a resume lands the client in
// the still-open session rather than respawning the harness.
//
// tmux session names may not contain "." or ":" (they are reserved in target
// syntax); both are replaced with "-" via sanitizeSessionName. Dedup keys on
// @cog_dir, not the name, so a sanitized-name collision only affects the label
// shown in the session list.
func newSessionWith(r Runner, canonical, name string, argv []string) (mux.Target, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("tmuxctl: argv must not be empty")
	}

	session := sanitizeSessionName(name)

	// Build: tmux new-session -d -s <session> -c <canonical> -P -F '#{session_name}:#{window_index}'
	// -d: create the session detached (don't switch the current client to it).
	// -s: session name.
	// -c: set the working directory of the session's first pane to the
	//     canonical worktree path (harness contract: CWD == worktree).
	// -P -F: print the target of the new session's window so we can return it.
	//
	// No command is appended, so the window runs the default shell. The harness
	// is started afterwards via send-keys so exiting it returns to that shell.
	args := []string{
		"new-session",
		"-d",
		"-s", session,
		"-c", canonical,
		"-P", "-F", "#{session_name}:#{window_index}",
	}

	out, err := r.Run(args...)
	if err != nil {
		// Older or manually-created sessions may not have @cog_dir set, so the
		// dedup lookup above can miss them. If tmux says the requested session
		// already exists, reuse it instead of surfacing a launch failure.
		if isDuplicateSessionError(err, session) {
			target, findErr := findWindowBySessionWith(r, session)
			if findErr != nil {
				return "", fmt.Errorf("tmuxctl: find duplicate session %q: %w", session, findErr)
			}
			return target, nil
		}
		return "", fmt.Errorf("tmuxctl: new-session: %w", err)
	}

	target := mux.Target(strings.TrimSpace(out))
	if target == "" {
		return "", fmt.Errorf("tmuxctl: new-session returned empty target")
	}

	// Tag the window with the canonical worktree path before running anything.
	if err := setOptionWith(r, target, "@cog_dir", canonical); err != nil {
		return "", fmt.Errorf("tmuxctl: set @cog_dir on %s: %w", target, err)
	}

	// Run the harness inside the shell. The command is typed as one literal key
	// argument followed by Enter, so quitting the harness drops back to the
	// shell rather than ending the pane.
	cmdline := shellJoin(argv)
	if _, err := r.Run("send-keys", "-t", string(target), cmdline, "Enter"); err != nil {
		return "", fmt.Errorf("tmuxctl: send-keys harness to %s: %w", target, err)
	}

	return target, nil
}

// shellJoin renders argv as a single POSIX-sh command line, quoting each element
// as needed so it can be typed into a shell via send-keys without the shell
// re-splitting or expanding it.
func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

// shellQuote returns s quoted for safe use as a single POSIX-sh word. Values
// made up only of unambiguously safe characters are returned unquoted; anything
// else is wrapped in single quotes with embedded single quotes escaped.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool { return !isShellSafe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isShellSafe reports whether r can appear unquoted in a POSIX-sh word without
// being treated specially by the shell.
func isShellSafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("_@%+=:,./-", r)
}

func isDuplicateSessionError(err error, session string) bool {
	if err == nil || session == "" {
		return false
	}

	const marker = "duplicate session:"
	msg := err.Error()
	i := strings.LastIndex(msg, marker)
	if i < 0 {
		return false
	}
	duplicate := strings.TrimSpace(msg[i+len(marker):])
	return duplicate == session
}

func findWindowBySessionWith(r Runner, session string) (mux.Target, error) {
	out, err := r.Run(
		"list-windows", "-a",
		"-F", "#{session_name}:#{window_index}",
	)
	if err != nil {
		return "", fmt.Errorf("list-windows: %w", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		target := mux.Target(line)
		if sessionOf(target) == session {
			return target, nil
		}
	}

	return "", mux.ErrWindowNotFound
}

// sanitizeSessionName replaces tmux-reserved characters ("." and ":") with "-"
// so name is usable as a session name. An empty result falls back to "cog".
func sanitizeSessionName(name string) string {
	s := strings.NewReplacer(".", "-", ":", "-").Replace(name)
	s = strings.TrimSpace(s)
	if s == "" {
		return "cog"
	}
	return s
}

// setOptionWith sets a tmux window option on target.
func setOptionWith(r Runner, target mux.Target, option, value string) error {
	_, err := r.Run("set-option", "-w", "-t", string(target), option, value)
	if err != nil {
		return fmt.Errorf("set-option %s %s: %w", option, value, err)
	}
	return nil
}

// FindWindowByDirWith is the injectable variant of FindWindowByDir.
func FindWindowByDirWith(r Runner, dir string) (mux.Target, error) {
	if !Available() {
		return "", mux.ErrNotAvailable
	}

	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		return "", fmt.Errorf("tmuxctl: canonicalize dir %q: %w", dir, err)
	}

	return findByCanonicalDir(r, canonical)
}

// findByCanonicalDir searches all windows for one whose @cog_dir equals
// canonical. It is called with an already-canonical path.
func findByCanonicalDir(r Runner, canonical string) (mux.Target, error) {
	// List all windows across all sessions with their @cog_dir option.
	// Format: "session_name:window_index @cog_dir_value"
	out, err := r.Run(
		"list-windows", "-a",
		"-F", "#{session_name}:#{window_index} #{@cog_dir}",
	)
	if err != nil {
		return "", fmt.Errorf("tmuxctl: list-windows: %w", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		target, cogDir, found := strings.Cut(line, " ")
		if !found {
			// No @cog_dir set on this window — skip.
			continue
		}

		cogDir = strings.TrimSpace(cogDir)
		if cogDir == canonical {
			return mux.Target(target), nil
		}
	}

	return "", mux.ErrWindowNotFound
}

// WindowProcessAliveWith is the injectable variant of WindowProcessAlive.
func WindowProcessAliveWith(r Runner, target mux.Target) (bool, error) {
	if !Available() {
		return false, mux.ErrNotAvailable
	}

	out, err := r.Run(
		"display-message", "-t", string(target),
		"-p", "#{pane_dead}",
	)
	if err != nil {
		return false, fmt.Errorf("tmuxctl: display-message pane_dead for %s: %w", target, err)
	}

	// "0" = alive, "1" = dead.
	return strings.TrimSpace(out) == "0", nil
}

// RelaunchInWindowWith is the injectable variant of RelaunchInWindow.
func RelaunchInWindowWith(r Runner, target mux.Target, argv []string) error {
	if !Available() {
		return mux.ErrNotAvailable
	}
	if len(argv) == 0 {
		return fmt.Errorf("tmuxctl: argv must not be empty")
	}

	// respawn-pane -k: kill the existing (dead) pane and start a new process.
	args := []string{"respawn-pane", "-k", "-t", string(target)}
	args = append(args, argv...)

	if _, err := r.Run(args...); err != nil {
		return fmt.Errorf("tmuxctl: respawn-pane in %s: %w", target, err)
	}
	return nil
}

// SelectForClientWith is the injectable variant of SelectForClient.
func SelectForClientWith(r Runner, target mux.Target, client string) error {
	if !Available() {
		return mux.ErrNotAvailable
	}

	if _, err := r.Run("select-window", "-t", string(target)); err != nil {
		return fmt.Errorf("tmuxctl: select-window %s: %w", target, err)
	}
	if _, err := r.Run(switchClientArgs(client, string(target))...); err != nil {
		return fmt.Errorf("tmuxctl: switch-client %s: %w", target, err)
	}
	return nil
}

// switchClientArgs builds a switch-client argv, adding -c only when client is set.
func switchClientArgs(client, target string) []string {
	if client == "" {
		return []string{"switch-client", "-t", target}
	}
	return []string{"switch-client", "-c", client, "-t", target}
}

// SelectSessionForClientWith is the injectable variant of SelectSessionForClient.
func SelectSessionForClientWith(r Runner, target mux.Target, client string) error {
	if !Available() {
		return mux.ErrNotAvailable
	}

	session := sessionOf(target)
	if _, err := r.Run(switchClientArgs(client, session)...); err != nil {
		return fmt.Errorf("tmuxctl: switch-client %s: %w", session, err)
	}
	return nil
}

// SelectPaneForClient moves client to the window and pane identified by paneID
// (e.g. "%12"). Pane ids are server-unique, so no session qualifier is needed.
func SelectPaneForClient(r Runner, paneID, client string) error {
	if !Available() {
		return mux.ErrNotAvailable
	}

	if _, err := r.Run("select-window", "-t", paneID); err != nil {
		return fmt.Errorf("tmuxctl: select-window %s: %w", paneID, err)
	}
	if _, err := r.Run("select-pane", "-t", paneID); err != nil {
		return fmt.Errorf("tmuxctl: select-pane %s: %w", paneID, err)
	}
	if _, err := r.Run(switchClientArgs(client, paneID)...); err != nil {
		return fmt.Errorf("tmuxctl: switch-client %s: %w", paneID, err)
	}
	return nil
}

// sessionOf returns the session component of a "session:index" mux.Target. If
// target has no ":" it is returned unchanged (already a bare session name).
func sessionOf(target mux.Target) string {
	s := string(target)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}

// KillWindowWith is the injectable variant of KillWindow.
func KillWindowWith(r Runner, target mux.Target) error {
	if !Available() {
		return mux.ErrNotAvailable
	}
	if _, err := r.Run("kill-window", "-t", string(target)); err != nil {
		return fmt.Errorf("tmuxctl: kill-window %s: %w", target, err)
	}
	return nil
}

// KillSessionWith is the injectable variant of KillSession.
func KillSessionWith(r Runner, target mux.Target) error {
	if !Available() {
		return mux.ErrNotAvailable
	}
	session := sessionOf(target)
	if _, err := r.Run("kill-session", "-t", session); err != nil {
		return fmt.Errorf("tmuxctl: kill-session %s: %w", session, err)
	}
	return nil
}

// ListCogDirsWith is the injectable variant of ListCogDirs.
func ListCogDirsWith(r Runner) (map[string]bool, error) {
	if !Available() {
		return nil, mux.ErrNotAvailable
	}

	out, err := r.Run("list-windows", "-a", "-F", "#{@cog_dir}")
	if err != nil {
		return nil, fmt.Errorf("tmuxctl: list-windows for cog dirs: %w", err)
	}

	result := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		canonical, err := pathnorm.Canonical(line)
		if err != nil {
			// Non-fatal: skip paths that cannot be canonicalized.
			continue
		}
		result[canonical] = true
	}
	return result, nil
}

// paneFormat is tab-separated because paths may contain spaces.
var paneFormat = strings.Join([]string{
	"#{pane_id}", "#{window_id}", "#{session_id}", "#{session_name}",
	"#{pane_current_path}", "#{pane_current_command}",
	"#{@cog_dir}", "#{@cog_status}", "#{@cog_reason}", "#{@cog_since}",
	"#{window_index}", "#{window_name}", "#{automatic-rename}",
}, "\t")

// ListPanes returns every pane on the server in a single list-panes call.
func ListPanes(r Runner) ([]mux.Pane, error) {
	if !Available() {
		return nil, mux.ErrNotAvailable
	}

	out, err := r.Run("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		return nil, fmt.Errorf("tmuxctl: list-panes: %w", err)
	}

	var panes []mux.Pane
	for _, line := range splitLines(out) {
		fields := strings.Split(line, "\t")
		if len(fields) != 13 {
			continue
		}

		path := fields[4]
		if canonical, err := pathnorm.Canonical(path); err == nil {
			path = canonical
		}

		number, _ := strconv.Atoi(fields[10])
		panes = append(panes, mux.Pane{
			PaneID:      fields[0],
			WindowID:    fields[1],
			SessionID:   fields[2],
			SessionName: fields[3],
			Cwd:         path,
			Command:     fields[5],
			CogDir:      fields[6],
			CogStatus:   fields[7],
			CogReason:   fields[8],
			CogSince:    fields[9],
			TabName:     chosenWindowName(fields[11], fields[12]),
			TabNumber:   number,
		})
	}
	return panes, nil
}

// chosenWindowName is name when someone set it: rename-window and new-window
// -n both turn automatic-rename off, so a window still renaming itself after
// its running command carries no chosen name.
func chosenWindowName(name, automaticRename string) string {
	if automaticRename == "1" {
		return ""
	}
	return name
}

// ListClientFocus returns the session and window each attached client is on.
func ListClientFocus(r Runner) ([]mux.ClientFocus, error) {
	if !Available() {
		return nil, mux.ErrNotAvailable
	}

	out, err := r.Run("list-clients", "-F",
		"#{client_name}\t#{session_id}\t#{window_id}\t#{client_activity}")
	if err != nil {
		return nil, fmt.Errorf("tmuxctl: list-clients: %w", err)
	}

	var clients []mux.ClientFocus
	for _, line := range splitLines(out) {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			continue
		}

		activity, _ := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
		clients = append(clients, mux.ClientFocus{
			Client:           fields[0],
			SessionID:        fields[1],
			WindowID:         fields[2],
			ActivityUnixSecs: activity,
		})
	}
	return clients, nil
}

// splitLines splits command output into lines, dropping empty ones. Unlike
// TrimSpace per line it keeps trailing tabs, which delimit empty fields.
func splitLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// optionBoundary separates per-option output in the chained show-options call.
const optionBoundary = "\x1f-cog-end-\x1f"

// ReadGlobalOptions returns the current global value of each named option.
// Unset options are absent from the map, so callers can diff against what tmux
// actually holds. Values must be single-line.
func ReadGlobalOptions(r Runner, names []string) (map[string]string, error) {
	if !Available() {
		return nil, mux.ErrNotAvailable
	}

	values := make(map[string]string, len(names))
	if len(names) == 0 {
		return values, nil
	}

	var args []string
	for i, name := range names {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "show-options", "-gqv", name, ";", "display-message", "-p", optionBoundary)
	}

	out, err := r.Run(args...)
	if err != nil {
		return nil, fmt.Errorf("tmuxctl: show-options: %w", err)
	}

	index := 0
	var value []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != optionBoundary {
			value = append(value, line)
			continue
		}
		if index < len(names) && len(value) > 0 {
			values[names[index]] = strings.Join(value, "\n")
		}
		index++
		value = nil
	}
	return values, nil
}

func optionArgs(w mux.OptionWrite) []string {
	args := []string{"set-option"}
	if w.Global {
		args = append(args, "-g")
	} else {
		args = append(args, "-w")
	}
	if w.Unset {
		args = append(args, "-u")
	}
	if !w.Global {
		args = append(args, "-t", w.Target)
	}
	args = append(args, w.Name)
	if !w.Unset {
		args = append(args, w.Value)
	}
	return args
}

// ApplyOptions applies all writes in one tmux invocation, chaining commands
// with ";". tmux aborts the rest of a chain when one command fails, so callers
// must only target windows that exist. No-op on empty input.
func ApplyOptions(r Runner, writes []mux.OptionWrite) error {
	if len(writes) == 0 {
		return nil
	}
	if !Available() {
		return mux.ErrNotAvailable
	}

	var args []string
	for i, w := range writes {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, optionArgs(w)...)
	}

	if _, err := r.Run(args...); err != nil {
		return fmt.Errorf("tmuxctl: apply options: %w", err)
	}
	return nil
}

// DisplayMessage shows text in client's status line.
func DisplayMessage(r Runner, client, text string) error {
	if !Available() {
		return mux.ErrNotAvailable
	}

	args := []string{"display-message"}
	if client != "" {
		args = append(args, "-c", client)
	}
	args = append(args, text)

	if _, err := r.Run(args...); err != nil {
		return fmt.Errorf("tmuxctl: display-message: %w", err)
	}
	return nil
}

// CapturePane returns the last lines lines of target's visible history, with
// SGR styling kept and wrapped lines joined. target is a pane id or a window
// id (its active pane).
func CapturePane(r Runner, target string, lines int) (string, error) {
	if !Available() {
		return "", mux.ErrNotAvailable
	}

	out, err := r.Run("capture-pane", "-p", "-e", "-J", "-S", "-"+strconv.Itoa(lines), "-t", target)
	if err != nil {
		return "", fmt.Errorf("tmuxctl: capture-pane %s: %w", target, err)
	}
	return out, nil
}
