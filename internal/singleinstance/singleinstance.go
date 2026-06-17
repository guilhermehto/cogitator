// Package singleinstance enforces that at most one cogitator TUI runs at a
// time. On Acquire, a newer process evicts an older one ("last wins") so a
// freshly built `cogitator` always becomes the live hook-socket owner instead
// of silently degrading to poll-only mode when a stale instance still holds the
// socket.
package singleinstance

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/guilhermehto/cogitator/internal/hookipc"
)

// pidFilename is the runtime pidfile recording the live cogitator's PID. It sits
// in the same runtime directory as the hook sockets (see hookipc.RuntimePath).
const pidFilename = "cogitator.pid"

// Process abstracts the OS process operations the locker needs so the takeover
// logic is unit-testable without spawning real processes.
type Process interface {
	// Alive reports whether pid is a live process.
	Alive(pid int) bool
	// Name returns a best-effort process name; ok is false when it cannot be
	// determined (the caller then declines to signal, to avoid killing an
	// unrelated process that reused the PID).
	Name(pid int) (name string, ok bool)
	// Terminate sends SIGTERM to pid.
	Terminate(pid int) error
	// Kill sends SIGKILL to pid.
	Kill(pid int) error
}

// Locker records the current process in a pidfile and evicts a previous
// cogitator recorded there.
type Locker struct {
	path   string
	self   int
	proc   Process
	logger *slog.Logger
	grace  time.Duration
	poll   time.Duration
	sleep  func(time.Duration)
}

// New constructs a Locker writing to the default runtime pidfile path.
func New(logger *slog.Logger) *Locker {
	if logger == nil {
		logger = slog.Default()
	}
	return newLocker(hookipc.RuntimePath(pidFilename), os.Getpid(), osProcess{}, logger, 3*time.Second, 50*time.Millisecond, time.Sleep)
}

// newLocker is the dependency-injected constructor used by tests.
func newLocker(path string, self int, proc Process, logger *slog.Logger, grace, poll time.Duration, sleep func(time.Duration)) *Locker {
	return &Locker{path: path, self: self, proc: proc, logger: logger, grace: grace, poll: poll, sleep: sleep}
}

// Acquire ensures this process is the sole cogitator. A live cogitator recorded
// in the pidfile is terminated (SIGTERM, then SIGKILL after the grace period)
// before this process records itself. It returns a release func that removes
// the pidfile if it still names this process. A write failure is returned but
// is non-fatal to the caller — the TUI can still run without the guarantee.
func (l *Locker) Acquire() (release func(), err error) {
	if prev, ok := l.readPID(); ok && prev != l.self {
		l.evict(prev)
	}
	if err := l.writePID(); err != nil {
		return func() {}, err
	}
	return l.release, nil
}

// evict terminates a previous cogitator. It declines to signal a process it
// cannot positively identify as cogitator, so a stale pidfile whose PID has
// been reused by an unrelated process is never killed.
func (l *Locker) evict(pid int) {
	if !l.proc.Alive(pid) {
		return // stale pidfile; nothing to evict
	}

	name, known := l.proc.Name(pid)
	switch {
	case known && strings.Contains(strings.ToLower(name), "cogitator"):
		// Confirmed previous instance — proceed to evict.
	case known:
		l.logger.Warn("singleinstance: pidfile names a non-cogitator process; not evicting", "pid", pid, "name", name)
		return
	default:
		l.logger.Warn("singleinstance: cannot verify previous process identity; not evicting", "pid", pid)
		return
	}

	l.logger.Info("singleinstance: replacing previous cogitator (last wins)", "pid", pid)
	if err := l.proc.Terminate(pid); err != nil {
		l.logger.Warn("singleinstance: SIGTERM failed", "pid", pid, "err", err)
	}
	if l.waitExit(pid, l.grace) {
		return
	}

	l.logger.Warn("singleinstance: previous cogitator ignored SIGTERM; sending SIGKILL", "pid", pid)
	if err := l.proc.Kill(pid); err != nil {
		l.logger.Warn("singleinstance: SIGKILL failed", "pid", pid, "err", err)
	}
	if !l.waitExit(pid, l.grace) {
		// The hook sockets are still freed once the process dies; if it never
		// does we proceed anyway — hookipc.Listen detects and reclaims a stale
		// socket on bind.
		l.logger.Warn("singleinstance: previous cogitator still alive after SIGKILL; continuing", "pid", pid)
	}
}

// waitExit polls until pid is gone or max elapses. It reports whether the
// process exited within the window.
func (l *Locker) waitExit(pid int, max time.Duration) bool {
	for waited := time.Duration(0); ; waited += l.poll {
		if !l.proc.Alive(pid) {
			return true
		}
		if waited >= max {
			return false
		}
		l.sleep(l.poll)
	}
}

// readPID returns the PID recorded in the pidfile. ok is false when the file is
// absent or malformed.
func (l *Locker) readPID() (int, bool) {
	b, err := os.ReadFile(l.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			l.logger.Warn("singleinstance: read pidfile", "path", l.path, "err", err)
		}
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		l.logger.Warn("singleinstance: malformed pidfile", "path", l.path)
		return 0, false
	}
	return pid, true
}

// writePID records this process's PID atomically (temp file + rename) so a
// crash mid-write never leaves a torn pidfile.
func (l *Locker) writePID() error {
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(l.self)+"\n"), 0o600); err != nil {
		return fmt.Errorf("singleinstance: write pidfile: %w", err)
	}
	if err := os.Rename(tmp, l.path); err != nil {
		os.Remove(tmp) //nolint:errcheck // best-effort cleanup of the temp file
		return fmt.Errorf("singleinstance: rename pidfile: %w", err)
	}
	return nil
}

// release removes the pidfile, but only if it still names this process — if a
// newer instance has taken over, its pidfile is left intact.
func (l *Locker) release() {
	if pid, ok := l.readPID(); !ok || pid != l.self {
		return
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		l.logger.Warn("singleinstance: remove pidfile on shutdown", "err", err)
	}
}

// osProcess is the production Process backed by real OS calls.
type osProcess struct{}

func (osProcess) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 performs no delivery but still error-checks existence and
	// permissions: nil means alive; EPERM means alive but owned by another user.
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (osProcess) Name(pid int) (string, bool) {
	// Linux: /proc/<pid>/comm is the cheapest source.
	if b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm")); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			return name, true
		}
	}
	// macOS/BSD: fall back to ps.
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", false
	}
	if name := strings.TrimSpace(string(out)); name != "" {
		return name, true
	}
	return "", false
}

func (osProcess) Terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

func (osProcess) Kill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
