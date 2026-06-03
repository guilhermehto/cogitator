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
// ErrNotAvailable in that case so callers can degrade gracefully.
//
// No import of bubbletea or internal/ui is permitted in this package.
package tmuxctl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// ErrNotAvailable is returned by all operations when $TMUX is unset, meaning
// cogitator is not running inside a tmux session and cannot control windows.
var ErrNotAvailable = errors.New("tmuxctl: not inside a tmux session ($TMUX unset)")

// ErrWindowNotFound is returned by FindWindowByDir when no window has a
// @cog_dir option matching the requested canonical directory.
var ErrWindowNotFound = errors.New("tmuxctl: window not found for directory")

// Target is a tmux window address in the form "session:index" (e.g. "main:3").
// It is stable for the lifetime of the window and can be passed to Select,
// WindowProcessAlive, and RelaunchInWindow.
type Target string

// LaunchMode selects how a new worktree is opened: as a window in the current
// tmux session (ModeWindow) or as a brand-new tmux session (ModeSession).
type LaunchMode int

const (
	// ModeWindow opens worktrees with `tmux new-window` (the default).
	ModeWindow LaunchMode = iota
	// ModeSession opens worktrees with `tmux new-session`.
	ModeSession
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
// It returns false when $TMUX is unset; all operations return ErrNotAvailable
// in that case.
func Available() bool {
	return os.Getenv("TMUX") != ""
}

// EnsureWindow opens a tmux window for the worktree at dir, or returns the
// existing window if one is already tagged with @cog_dir == canonical(dir).
//
// Parameters:
//   - dir:  the worktree directory (canonicalized internally via pathnorm.Canonical).
//   - name: the window name to use when creating a new window (e.g. "repo/branch").
//   - argv: the command to run in the new window (program + arguments, no shell).
//
// Returns the Target ("session:index") of the window, or ErrNotAvailable when
// not inside tmux.
//
// Deduplication is by @cog_dir (canonical path), not by window name. If a
// window already exists for the directory, its name is NOT updated.
func EnsureWindow(dir, name string, argv []string) (Target, error) {
	return EnsureWindowModeWith(DefaultRunner, dir, name, argv, ModeWindow)
}

// EnsureWindowWith is the injectable variant of EnsureWindow.
func EnsureWindowWith(r Runner, dir, name string, argv []string) (Target, error) {
	return EnsureWindowModeWith(r, dir, name, argv, ModeWindow)
}

// EnsureWindowMode opens a worktree at dir using the given LaunchMode, or
// returns the existing window/session if one is already tagged with
// @cog_dir == canonical(dir). See EnsureWindow for the parameter contract.
//
// Deduplication is by @cog_dir across all windows in all sessions, so an
// existing target is reused regardless of which mode created it.
func EnsureWindowMode(dir, name string, argv []string, mode LaunchMode) (Target, error) {
	return EnsureWindowModeWith(DefaultRunner, dir, name, argv, mode)
}

// EnsureWindowModeWith is the injectable variant of EnsureWindowMode.
func EnsureWindowModeWith(r Runner, dir, name string, argv []string, mode LaunchMode) (Target, error) {
	if !Available() {
		return "", ErrNotAvailable
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
	if !errors.Is(err, ErrWindowNotFound) {
		return "", fmt.Errorf("tmuxctl: find existing window: %w", err)
	}

	// No existing window — create one in the requested mode.
	if mode == ModeSession {
		return newSessionWith(r, canonical, name, argv)
	}
	return newWindowWith(r, canonical, name, argv)
}

// newWindowWith creates a new tmux window running argv, names it name, and
// sets @cog_dir to canonical. Returns the Target of the new window.
//
// remain-on-exit is set to "on" for the window so that when the harness
// process exits (e.g. after a crash or explicit quit), the window stays open
// in the "dead" state rather than being auto-closed by tmux. This is required
// for WindowProcessAlive to detect the dead state and for RelaunchInWindow to
// revive the process via respawn-pane.
func newWindowWith(r Runner, canonical, name string, argv []string) (Target, error) {
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

	target := Target(strings.TrimSpace(out))
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

// newSessionWith creates a new detached tmux session running argv, names it a
// sanitized form of name, and sets @cog_dir to canonical. Returns the Target
// ("session:index") of the new session's first window.
//
// As with newWindowWith, remain-on-exit is enabled so the window survives
// process exit, and the window is tagged with @cog_dir for dedup.
//
// tmux session names may not contain "." or ":" (they are reserved in target
// syntax); both are replaced with "-" via sanitizeSessionName. Dedup keys on
// @cog_dir, not the name, so a sanitized-name collision only affects the label
// shown in the session list.
func newSessionWith(r Runner, canonical, name string, argv []string) (Target, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("tmuxctl: argv must not be empty")
	}

	session := sanitizeSessionName(name)

	// Build: tmux new-session -d -s <session> -c <canonical> -P -F '#{session_name}:#{window_index}' <argv...>
	// -d: create the session detached (don't switch the current client to it).
	// -s: session name.
	// -c: set the working directory of the session's first pane to the
	//     canonical worktree path (harness contract: CWD == worktree).
	// -P -F: print the target of the new session's window so we can return it.
	args := []string{
		"new-session",
		"-d",
		"-s", session,
		"-c", canonical,
		"-P", "-F", "#{session_name}:#{window_index}",
	}
	args = append(args, argv...)

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

	target := Target(strings.TrimSpace(out))
	if target == "" {
		return "", fmt.Errorf("tmuxctl: new-session returned empty target")
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

func findWindowBySessionWith(r Runner, session string) (Target, error) {
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
		target := Target(line)
		if sessionOf(target) == session {
			return target, nil
		}
	}

	return "", ErrWindowNotFound
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
func setOptionWith(r Runner, target Target, option, value string) error {
	_, err := r.Run("set-option", "-w", "-t", string(target), option, value)
	if err != nil {
		return fmt.Errorf("set-option %s %s: %w", option, value, err)
	}
	return nil
}

// FindWindowByDir returns the Target of the tmux window whose @cog_dir option
// equals the canonical form of dir. Returns ErrWindowNotFound when no such
// window exists, or ErrNotAvailable when not inside tmux.
func FindWindowByDir(dir string) (Target, error) {
	return FindWindowByDirWith(DefaultRunner, dir)
}

// FindWindowByDirWith is the injectable variant of FindWindowByDir.
func FindWindowByDirWith(r Runner, dir string) (Target, error) {
	if !Available() {
		return "", ErrNotAvailable
	}

	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		return "", fmt.Errorf("tmuxctl: canonicalize dir %q: %w", dir, err)
	}

	return findByCanonicalDir(r, canonical)
}

// findByCanonicalDir searches all windows for one whose @cog_dir equals
// canonical. It is called with an already-canonical path.
func findByCanonicalDir(r Runner, canonical string) (Target, error) {
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
			return Target(target), nil
		}
	}

	return "", ErrWindowNotFound
}

// WindowProcessAlive reports whether the process running in the given window's
// pane is still alive. It uses tmux's #{pane_dead} format variable: "1" means
// the pane's process has exited (dead), "0" means it is still running.
//
// Returns ErrNotAvailable when not inside tmux.
func WindowProcessAlive(target Target) (bool, error) {
	return WindowProcessAliveWith(DefaultRunner, target)
}

// WindowProcessAliveWith is the injectable variant of WindowProcessAlive.
func WindowProcessAliveWith(r Runner, target Target) (bool, error) {
	if !Available() {
		return false, ErrNotAvailable
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

// RelaunchInWindow re-runs argv in the given window's pane. It is intended for
// windows whose process has exited (WindowProcessAlive returned false) but
// whose window still exists (the pane is in the "dead" state showing the exit
// code). It uses `tmux respawn-pane -k` to kill the dead pane and start a new
// process in its place.
//
// Returns ErrNotAvailable when not inside tmux.
func RelaunchInWindow(target Target, argv []string) error {
	return RelaunchInWindowWith(DefaultRunner, target, argv)
}

// RelaunchInWindowWith is the injectable variant of RelaunchInWindow.
func RelaunchInWindowWith(r Runner, target Target, argv []string) error {
	if !Available() {
		return ErrNotAvailable
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

// Select moves the attached tmux client to the given window. It first selects
// the window within its session (`select-window`), then switches the client to
// that session (`switch-client`). The switch-client step is required because
// the target may live in a different session than the one the client is
// currently attached to — as it always does in session launch mode, where each
// worktree opens as its own session. select-window alone only changes the
// active window inside the target's session and never moves the client there.
//
// Returns ErrNotAvailable when not inside tmux.
func Select(target Target) error {
	return SelectWith(DefaultRunner, target)
}

// SelectWith is the injectable variant of Select.
func SelectWith(r Runner, target Target) error {
	if !Available() {
		return ErrNotAvailable
	}

	if _, err := r.Run("select-window", "-t", string(target)); err != nil {
		return fmt.Errorf("tmuxctl: select-window %s: %w", target, err)
	}
	if _, err := r.Run("switch-client", "-t", string(target)); err != nil {
		return fmt.Errorf("tmuxctl: switch-client %s: %w", target, err)
	}
	return nil
}

// SelectSession switches the attached tmux client to the session that owns
// target, without selecting a specific window. tmux restores the session's
// last-active window, so jumping back to a worktree opened in session mode
// lands on whatever window you were last using in that session — not always
// the worktree's original window.
//
// target is a "session:index" address; only the session component is used.
//
// Returns ErrNotAvailable when not inside tmux.
func SelectSession(target Target) error {
	return SelectSessionWith(DefaultRunner, target)
}

// SelectSessionWith is the injectable variant of SelectSession.
func SelectSessionWith(r Runner, target Target) error {
	if !Available() {
		return ErrNotAvailable
	}

	session := sessionOf(target)
	if _, err := r.Run("switch-client", "-t", session); err != nil {
		return fmt.Errorf("tmuxctl: switch-client %s: %w", session, err)
	}
	return nil
}

// sessionOf returns the session component of a "session:index" Target. If
// target has no ":" it is returned unchanged (already a bare session name).
func sessionOf(target Target) string {
	s := string(target)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}

// KillWindow closes the tmux window addressed by target (`tmux kill-window`).
// It is used to clean up a worktree's window after the worktree directory is
// deleted, so no dead pane pointing at a missing directory is left behind. When
// target is the last window in its session, tmux closes the session too.
//
// Returns ErrNotAvailable when not inside tmux.
func KillWindow(target Target) error {
	return KillWindowWith(DefaultRunner, target)
}

// KillWindowWith is the injectable variant of KillWindow.
func KillWindowWith(r Runner, target Target) error {
	if !Available() {
		return ErrNotAvailable
	}
	if _, err := r.Run("kill-window", "-t", string(target)); err != nil {
		return fmt.Errorf("tmuxctl: kill-window %s: %w", target, err)
	}
	return nil
}

// ListCogDirs returns the set of canonical worktree directories that currently
// have a tmux window tagged with @cog_dir. It runs
// `tmux list-windows -a -F '#{@cog_dir}'` and canonicalizes each non-empty
// value via pathnorm.Canonical.
//
// The returned map is keyed by canonical path; the value is always true.
// Returns ErrNotAvailable when not inside tmux.
func ListCogDirs() (map[string]bool, error) {
	return ListCogDirsWith(DefaultRunner)
}

// ListCogDirsWith is the injectable variant of ListCogDirs.
func ListCogDirsWith(r Runner) (map[string]bool, error) {
	if !Available() {
		return nil, ErrNotAvailable
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
