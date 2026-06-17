package singleinstance

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeProc is a scriptable Process for exercising the takeover logic without
// touching real OS processes.
type fakeProc struct {
	name      string
	nameKnown bool
	aliveFn   func(pid int) bool
	onTerm    func()
	onKill    func()
	terms     int
	kills     int
}

func (f *fakeProc) Alive(pid int) bool { return f.aliveFn(pid) }
func (f *fakeProc) Name(int) (string, bool) {
	return f.name, f.nameKnown
}
func (f *fakeProc) Terminate(int) error {
	f.terms++
	if f.onTerm != nil {
		f.onTerm()
	}
	return nil
}
func (f *fakeProc) Kill(int) error {
	f.kills++
	if f.onKill != nil {
		f.onKill()
	}
	return nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// lockerFor builds a Locker over a temp pidfile with a no-op sleep and tight
// timers so waitExit loops resolve quickly.
func lockerFor(t *testing.T, self int, proc Process) (*Locker, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cogitator.pid")
	l := newLocker(path, self, proc, quietLogger(), 10*time.Millisecond, time.Millisecond, func(time.Duration) {})
	return l, path
}

func writePidfile(t *testing.T, path string, pid int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}
}

func readPidfile(t *testing.T, path string) (int, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pidfile not an int: %q", string(b))
	}
	return pid, true
}

func TestAcquireRecordsSelfWhenNoPriorInstance(t *testing.T) {
	proc := &fakeProc{aliveFn: func(int) bool { return false }}
	l, path := lockerFor(t, 4242, proc)

	release, err := l.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if proc.terms != 0 || proc.kills != 0 {
		t.Fatalf("did not expect any signals; terms=%d kills=%d", proc.terms, proc.kills)
	}
	if pid, ok := readPidfile(t, path); !ok || pid != 4242 {
		t.Fatalf("pidfile = (%d,%v), want (4242,true)", pid, ok)
	}

	release()
	if _, ok := readPidfile(t, path); ok {
		t.Fatalf("release should have removed the pidfile")
	}
}

func TestAcquireEvictsLivePriorCogitatorOnSIGTERM(t *testing.T) {
	dead := false
	proc := &fakeProc{
		name:      "cogitator",
		nameKnown: true,
		aliveFn:   func(int) bool { return !dead },
		onTerm:    func() { dead = true },
	}
	l, path := lockerFor(t, 999, proc)
	writePidfile(t, path, 17263)

	if _, err := l.Acquire(); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if proc.terms != 1 {
		t.Fatalf("expected one SIGTERM, got %d", proc.terms)
	}
	if proc.kills != 0 {
		t.Fatalf("expected no SIGKILL when SIGTERM works, got %d", proc.kills)
	}
	if pid, ok := readPidfile(t, path); !ok || pid != 999 {
		t.Fatalf("pidfile = (%d,%v), want (999,true)", pid, ok)
	}
}

func TestAcquireEscalatesToSIGKILLWhenSIGTERMIgnored(t *testing.T) {
	dead := false
	proc := &fakeProc{
		name:      "/Users/x/go/bin/cogitator",
		nameKnown: true,
		aliveFn:   func(int) bool { return !dead },
		onKill:    func() { dead = true }, // only SIGKILL ends it
	}
	l, path := lockerFor(t, 1, proc)
	writePidfile(t, path, 555)

	if _, err := l.Acquire(); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if proc.terms != 1 {
		t.Fatalf("expected one SIGTERM, got %d", proc.terms)
	}
	if proc.kills != 1 {
		t.Fatalf("expected one SIGKILL after SIGTERM ignored, got %d", proc.kills)
	}
}

func TestAcquireDoesNotSignalUnrelatedReusedPID(t *testing.T) {
	proc := &fakeProc{
		name:      "bash",
		nameKnown: true,
		aliveFn:   func(int) bool { return true },
	}
	l, path := lockerFor(t, 7, proc)
	writePidfile(t, path, 888)

	if _, err := l.Acquire(); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if proc.terms != 0 || proc.kills != 0 {
		t.Fatalf("must not signal a non-cogitator process; terms=%d kills=%d", proc.terms, proc.kills)
	}
	if pid, _ := readPidfile(t, path); pid != 7 {
		t.Fatalf("pidfile = %d, want 7 (overwritten)", pid)
	}
}

func TestAcquireDoesNotSignalWhenIdentityUnknown(t *testing.T) {
	proc := &fakeProc{
		nameKnown: false,
		aliveFn:   func(int) bool { return true },
	}
	l, path := lockerFor(t, 7, proc)
	writePidfile(t, path, 888)

	if _, err := l.Acquire(); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if proc.terms != 0 || proc.kills != 0 {
		t.Fatalf("must not signal an unidentifiable process; terms=%d kills=%d", proc.terms, proc.kills)
	}
}

func TestReleaseLeavesPidfileOwnedByNewerInstance(t *testing.T) {
	proc := &fakeProc{aliveFn: func(int) bool { return false }}
	l, path := lockerFor(t, 100, proc)

	release, err := l.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// A newer instance takes over the pidfile.
	writePidfile(t, path, 200)

	release()
	if pid, ok := readPidfile(t, path); !ok || pid != 200 {
		t.Fatalf("release clobbered a newer owner's pidfile: (%d,%v)", pid, ok)
	}
}

func TestAcquireIgnoresStalePidfileNamingSelf(t *testing.T) {
	// A leftover pidfile naming our own PID must not cause self-eviction.
	proc := &fakeProc{aliveFn: func(int) bool { return true }, name: "cogitator", nameKnown: true}
	l, path := lockerFor(t, 333, proc)
	writePidfile(t, path, 333)

	if _, err := l.Acquire(); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if proc.terms != 0 || proc.kills != 0 {
		t.Fatalf("must not signal self; terms=%d kills=%d", proc.terms, proc.kills)
	}
}
