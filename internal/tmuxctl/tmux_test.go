package tmuxctl

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// ---- helpers ----------------------------------------------------------------

// fakeRunner records every call and returns canned responses in order.
// If responses is exhausted it returns ("", nil).
type fakeRunner struct {
	calls     [][]string
	responses []fakeResponse
}

type fakeResponse struct {
	out string
	err error
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	// Record the call.
	cp := make([]string, len(args))
	copy(cp, args)
	f.calls = append(f.calls, cp)

	if len(f.responses) == 0 {
		return "", nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp.out, resp.err
}

func (f *fakeRunner) push(out string, err error) {
	f.responses = append(f.responses, fakeResponse{out: out, err: err})
}

// assertCall checks that the n-th recorded call (0-indexed) starts with the
// expected prefix arguments.
func assertCall(t *testing.T, f *fakeRunner, n int, prefix ...string) {
	t.Helper()
	if n >= len(f.calls) {
		t.Fatalf("expected call #%d but only %d calls recorded", n, len(f.calls))
	}
	got := f.calls[n]
	for i, want := range prefix {
		if i >= len(got) || got[i] != want {
			t.Errorf("call #%d arg[%d]: got %q, want %q (full call: %v)", n, i, got[i], want, got)
		}
	}
}

// withTMUX sets $TMUX to a non-empty value for the duration of the test and
// restores it afterwards. This makes Available() return true.
func withTMUX(t *testing.T) {
	t.Helper()
	prev := os.Getenv("TMUX")
	os.Setenv("TMUX", "/tmp/tmux-test,1234,0")
	t.Cleanup(func() { os.Setenv("TMUX", prev) })
}

// withoutTMUX unsets $TMUX for the duration of the test.
func withoutTMUX(t *testing.T) {
	t.Helper()
	prev := os.Getenv("TMUX")
	os.Unsetenv("TMUX")
	t.Cleanup(func() { os.Setenv("TMUX", prev) })
}

// ---- Available() ------------------------------------------------------------

func TestAvailable_TrueWhenTMUXSet(t *testing.T) {
	withTMUX(t)
	if !Available() {
		t.Error("Available() = false, want true when $TMUX is set")
	}
}

func TestAvailable_FalseWhenTMUXUnset(t *testing.T) {
	withoutTMUX(t)
	if Available() {
		t.Error("Available() = true, want false when $TMUX is unset")
	}
}

// ---- ErrNotAvailable gate ---------------------------------------------------

func TestEnsureWindow_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	_, err := EnsureWindowWith(r, "/tmp/x", "r/x", []string{"sleep", "60"})
	if !errors.Is(err, ErrNotAvailable) {
		t.Errorf("EnsureWindowWith: got %v, want ErrNotAvailable", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no tmux calls when not available, got %d", len(r.calls))
	}
}

func TestFindWindowByDir_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	_, err := FindWindowByDirWith(r, "/tmp/x")
	if !errors.Is(err, ErrNotAvailable) {
		t.Errorf("FindWindowByDirWith: got %v, want ErrNotAvailable", err)
	}
}

func TestWindowProcessAlive_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	_, err := WindowProcessAliveWith(r, "main:1")
	if !errors.Is(err, ErrNotAvailable) {
		t.Errorf("WindowProcessAliveWith: got %v, want ErrNotAvailable", err)
	}
}

func TestRelaunchInWindow_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	err := RelaunchInWindowWith(r, "main:1", []string{"sleep", "60"})
	if !errors.Is(err, ErrNotAvailable) {
		t.Errorf("RelaunchInWindowWith: got %v, want ErrNotAvailable", err)
	}
}

func TestSelect_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	err := SelectWith(r, "main:1")
	if !errors.Is(err, ErrNotAvailable) {
		t.Errorf("SelectWith: got %v, want ErrNotAvailable", err)
	}
}

// ---- FindWindowByDir parsing ------------------------------------------------

func TestFindWindowByDir_Found(t *testing.T) {
	withTMUX(t)

	// Simulate list-windows output: three windows, second has @cog_dir set.
	listOut := "main:0 \nmain:1 /private/tmp/wt\nmain:2 /other/path\n"

	r := &fakeRunner{}
	r.push(listOut, nil)

	target, err := FindWindowByDirWith(r, "/private/tmp/wt")
	if err != nil {
		t.Fatalf("FindWindowByDirWith: unexpected error: %v", err)
	}
	if target != "main:1" {
		t.Errorf("target = %q, want %q", target, "main:1")
	}

	// Verify the list-windows call used the correct format.
	assertCall(t, r, 0, "list-windows", "-a", "-F", "#{session_name}:#{window_index} #{@cog_dir}")
}

func TestFindWindowByDir_NotFound(t *testing.T) {
	withTMUX(t)

	listOut := "main:0 /some/other/path\n"
	r := &fakeRunner{}
	r.push(listOut, nil)

	_, err := FindWindowByDirWith(r, "/tmp/wt")
	if !errors.Is(err, ErrWindowNotFound) {
		t.Errorf("FindWindowByDirWith: got %v, want ErrWindowNotFound", err)
	}
}

func TestFindWindowByDir_EmptyOutput(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil)

	_, err := FindWindowByDirWith(r, "/tmp/wt")
	if !errors.Is(err, ErrWindowNotFound) {
		t.Errorf("FindWindowByDirWith empty output: got %v, want ErrWindowNotFound", err)
	}
}

func TestFindWindowByDir_WindowWithNoCogDir(t *testing.T) {
	withTMUX(t)

	// A window line with no space (no @cog_dir value) should be skipped.
	listOut := "main:0\nmain:1 /private/tmp/target\n"
	r := &fakeRunner{}
	r.push(listOut, nil)

	target, err := FindWindowByDirWith(r, "/private/tmp/target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "main:1" {
		t.Errorf("target = %q, want %q", target, "main:1")
	}
}

// ---- EnsureWindow: create vs dedup ------------------------------------------

func TestEnsureWindow_CreatesNewWindow(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	// FindWindowByDir call: no existing window.
	r.push("main:0 /other/path\n", nil)
	// new-window call: returns the new target.
	r.push("main:1\n", nil)
	// set-option remain-on-exit: no output needed.
	r.push("", nil)
	// set-option @cog_dir: no output needed.
	r.push("", nil)

	target, err := EnsureWindowWith(r, "/private/tmp/newwt", "repo/branch", []string{"sleep", "60"})
	if err != nil {
		t.Fatalf("EnsureWindowWith: unexpected error: %v", err)
	}
	if target != "main:1" {
		t.Errorf("target = %q, want %q", target, "main:1")
	}

	// Call 0: list-windows (FindWindowByDir)
	assertCall(t, r, 0, "list-windows", "-a")
	// Call 1: new-window with -d, -n, -P, -F, then argv
	assertCall(t, r, 1, "new-window", "-d", "-n", "repo/branch", "-P", "-F", "#{session_name}:#{window_index}", "sleep", "60")
	// Call 2: set-option remain-on-exit so the window survives process exit
	assertCall(t, r, 2, "set-option", "-w", "-t", "main:1", "remain-on-exit", "on")
	// Call 3: set-option to tag @cog_dir
	assertCall(t, r, 3, "set-option", "-w", "-t", "main:1", "@cog_dir")
}

func TestEnsureWindow_DeduplicatesByDir(t *testing.T) {
	withTMUX(t)

	// The directory already has a window.
	listOut := "main:0 /private/tmp/existingwt\n"
	r := &fakeRunner{}
	r.push(listOut, nil)

	target, err := EnsureWindowWith(r, "/private/tmp/existingwt", "repo/branch", []string{"sleep", "60"})
	if err != nil {
		t.Fatalf("EnsureWindowWith: unexpected error: %v", err)
	}
	if target != "main:0" {
		t.Errorf("target = %q, want %q", target, "main:0")
	}

	// Only one call should have been made (the list-windows lookup).
	if len(r.calls) != 1 {
		t.Errorf("expected 1 call (list-windows), got %d: %v", len(r.calls), r.calls)
	}
}

func TestEnsureWindow_EmptyArgvError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	// FindWindowByDir: no existing window.
	r.push("", nil)

	_, err := EnsureWindowWith(r, "/private/tmp/wt", "repo/branch", nil)
	if err == nil {
		t.Error("expected error for empty argv, got nil")
	}
}

// ---- WindowProcessAlive pane_dead parsing -----------------------------------

func TestWindowProcessAlive_Alive(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("0\n", nil) // pane_dead = 0 → alive

	alive, err := WindowProcessAliveWith(r, "main:2")
	if err != nil {
		t.Fatalf("WindowProcessAliveWith: unexpected error: %v", err)
	}
	if !alive {
		t.Error("alive = false, want true when pane_dead=0")
	}

	assertCall(t, r, 0, "display-message", "-t", "main:2", "-p", "#{pane_dead}")
}

func TestWindowProcessAlive_Dead(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("1\n", nil) // pane_dead = 1 → dead

	alive, err := WindowProcessAliveWith(r, "main:2")
	if err != nil {
		t.Fatalf("WindowProcessAliveWith: unexpected error: %v", err)
	}
	if alive {
		t.Error("alive = true, want false when pane_dead=1")
	}
}

// ---- RelaunchInWindow argv builder ------------------------------------------

func TestRelaunchInWindow_BuildsCorrectArgv(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil)

	err := RelaunchInWindowWith(r, "main:3", []string{"opencode", "--mdns", "/wt"})
	if err != nil {
		t.Fatalf("RelaunchInWindowWith: unexpected error: %v", err)
	}

	assertCall(t, r, 0, "respawn-pane", "-k", "-t", "main:3", "opencode", "--mdns", "/wt")
}

func TestRelaunchInWindow_EmptyArgvError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	err := RelaunchInWindowWith(r, "main:3", nil)
	if err == nil {
		t.Error("expected error for empty argv, got nil")
	}
}

// ---- Select argv builder ----------------------------------------------------

func TestSelect_BuildsCorrectArgv(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil)

	err := SelectWith(r, "main:5")
	if err != nil {
		t.Fatalf("SelectWith: unexpected error: %v", err)
	}

	assertCall(t, r, 0, "select-window", "-t", "main:5")
}

// ---- Runner error propagation -----------------------------------------------

func TestFindWindowByDir_RunnerError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", errors.New("tmux: server not found"))

	_, err := FindWindowByDirWith(r, "/tmp/wt")
	if err == nil {
		t.Error("expected error when runner fails, got nil")
	}
	if strings.Contains(err.Error(), "list-windows") == false {
		t.Errorf("error should mention list-windows, got: %v", err)
	}
}

func TestWindowProcessAlive_RunnerError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", errors.New("no such window"))

	_, err := WindowProcessAliveWith(r, "main:99")
	if err == nil {
		t.Error("expected error when runner fails, got nil")
	}
}

func TestSelect_RunnerError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", errors.New("no such window"))

	err := SelectWith(r, "main:99")
	if err == nil {
		t.Error("expected error when runner fails, got nil")
	}
}

// ---- ListCogDirs ------------------------------------------------------------

func TestListCogDirs_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	_, err := ListCogDirsWith(r)
	if !errors.Is(err, ErrNotAvailable) {
		t.Errorf("ListCogDirsWith: got %v, want ErrNotAvailable", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no tmux calls when not available, got %d", len(r.calls))
	}
}

func TestListCogDirs_ReturnsCanonicalSet(t *testing.T) {
	withTMUX(t)

	// Simulate list-windows output: three lines, one empty (no @cog_dir set).
	listOut := "/private/tmp/wt-a\n/private/tmp/wt-b\n\n"
	r := &fakeRunner{}
	r.push(listOut, nil)

	dirs, err := ListCogDirsWith(r)
	if err != nil {
		t.Fatalf("ListCogDirsWith: unexpected error: %v", err)
	}
	if !dirs["/private/tmp/wt-a"] {
		t.Errorf("expected /private/tmp/wt-a in result, got %v", dirs)
	}
	if !dirs["/private/tmp/wt-b"] {
		t.Errorf("expected /private/tmp/wt-b in result, got %v", dirs)
	}
	if len(dirs) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(dirs), dirs)
	}

	// Verify the correct tmux command was issued.
	assertCall(t, r, 0, "list-windows", "-a", "-F", "#{@cog_dir}")
}

func TestListCogDirs_EmptyOutput(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil)

	dirs, err := ListCogDirsWith(r)
	if err != nil {
		t.Fatalf("ListCogDirsWith empty output: unexpected error: %v", err)
	}
	if len(dirs) != 0 {
		t.Errorf("expected empty map for empty output, got %v", dirs)
	}
}

func TestListCogDirs_RunnerError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", errors.New("tmux: server not found"))

	_, err := ListCogDirsWith(r)
	if err == nil {
		t.Error("expected error when runner fails, got nil")
	}
	if !strings.Contains(err.Error(), "list-windows") {
		t.Errorf("error should mention list-windows, got: %v", err)
	}
}
