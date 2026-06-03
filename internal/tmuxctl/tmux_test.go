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
	// Call 1: new-window with -d, -c <canonical>, -n, -P, -F, then argv.
	// The -c flag sets the pane's working directory to the canonical worktree
	// path, satisfying the harness CWD contract.
	assertCall(t, r, 1, "new-window", "-d", "-c", "/private/tmp/newwt", "-n", "repo/branch", "-P", "-F", "#{session_name}:#{window_index}", "sleep", "60")
	// Explicit assertion: the -c value must be the canonical worktree dir.
	newWindowCall := r.calls[1]
	cFlagIdx := -1
	for i, arg := range newWindowCall {
		if arg == "-c" {
			cFlagIdx = i
			break
		}
	}
	if cFlagIdx == -1 {
		t.Error("new-window call missing -c flag")
	} else if cFlagIdx+1 >= len(newWindowCall) {
		t.Error("new-window -c flag has no value")
	} else if got := newWindowCall[cFlagIdx+1]; got != "/private/tmp/newwt" {
		t.Errorf("new-window -c value = %q, want %q", got, "/private/tmp/newwt")
	}
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

// ---- EnsureWindowMode: session creation -------------------------------------

func TestEnsureWindowMode_CreatesNewSession(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	// FindWindowByDir: no existing window.
	r.push("main:0 /other/path\n", nil)
	// new-session: returns the new target.
	r.push("repo-branch:0\n", nil)
	// set-option remain-on-exit.
	r.push("", nil)
	// set-option @cog_dir.
	r.push("", nil)

	target, err := EnsureWindowModeWith(r, "/private/tmp/newwt", "repo/branch", []string{"sleep", "60"}, ModeSession)
	if err != nil {
		t.Fatalf("EnsureWindowModeWith: unexpected error: %v", err)
	}
	if target != "repo-branch:0" {
		t.Errorf("target = %q, want %q", target, "repo-branch:0")
	}

	// Call 0: list-windows (dedup lookup).
	assertCall(t, r, 0, "list-windows", "-a")
	// Call 1: new-session with -d, -s <sanitized name>, -c <canonical>, -P, -F, argv.
	// The "/" in "repo/branch" is not reserved for sessions; "." and ":" are,
	// and there are none here, so the session name is unchanged.
	assertCall(t, r, 1, "new-session", "-d", "-s", "repo/branch", "-c", "/private/tmp/newwt", "-P", "-F", "#{session_name}:#{window_index}", "sleep", "60")
	// Call 2/3: remain-on-exit + @cog_dir on the new window.
	assertCall(t, r, 2, "set-option", "-w", "-t", "repo-branch:0", "remain-on-exit", "on")
	assertCall(t, r, 3, "set-option", "-w", "-t", "repo-branch:0", "@cog_dir")
}

func TestEnsureWindowMode_DuplicateSessionReusesExistingSession(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	// Dedup by @cog_dir does not find older/untagged tmux windows.
	r.push("main:0 /other/path\n", nil)
	// Creating the session fails because a session with the derived name already exists.
	r.push("", errors.New("tmux new-session: duplicate session: repo/branch"))
	// Fallback resolves the existing session to one of its windows.
	r.push("main:0\nrepo/branch:2\nrepo/branch:3\n", nil)

	target, err := EnsureWindowModeWith(r, "/private/tmp/newwt", "repo/branch", []string{"sleep", "60"}, ModeSession)
	if err != nil {
		t.Fatalf("EnsureWindowModeWith: unexpected error: %v", err)
	}
	if target != "repo/branch:2" {
		t.Errorf("target = %q, want %q", target, "repo/branch:2")
	}

	assertCall(t, r, 0, "list-windows", "-a", "-F", "#{session_name}:#{window_index} #{@cog_dir}")
	assertCall(t, r, 1, "new-session", "-d", "-s", "repo/branch", "-c", "/private/tmp/newwt")
	assertCall(t, r, 2, "list-windows", "-a", "-F", "#{session_name}:#{window_index}")
	if len(r.calls) != 3 {
		t.Errorf("expected no set-option calls when reusing existing session, got %d calls: %v", len(r.calls), r.calls)
	}
}

func TestEnsureWindowMode_SessionSanitizesName(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil)                     // dedup: no window
	r.push("my-repo-1-2-feat:0\n", nil) // new-session target
	r.push("", nil)                     // remain-on-exit
	r.push("", nil)                     // @cog_dir

	_, err := EnsureWindowModeWith(r, "/private/tmp/wt", "my.repo:1.2/feat", []string{"sleep", "60"}, ModeSession)
	if err != nil {
		t.Fatalf("EnsureWindowModeWith: unexpected error: %v", err)
	}

	sessionCall := r.calls[1]
	var sName string
	for i, arg := range sessionCall {
		if arg == "-s" && i+1 < len(sessionCall) {
			sName = sessionCall[i+1]
			break
		}
	}
	if strings.ContainsAny(sName, ".:") {
		t.Errorf("session name %q must not contain '.' or ':'", sName)
	}
	if sName != "my-repo-1-2/feat" {
		t.Errorf("session name = %q, want %q", sName, "my-repo-1-2/feat")
	}
}

func TestEnsureWindowMode_SessionEmptyArgvError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil) // dedup: no window

	_, err := EnsureWindowModeWith(r, "/private/tmp/wt", "repo/branch", nil, ModeSession)
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
	r.push("", nil) // select-window
	r.push("", nil) // switch-client

	err := SelectWith(r, "main:5")
	if err != nil {
		t.Fatalf("SelectWith: unexpected error: %v", err)
	}

	// Select must both focus the window and switch the client to its session
	// so launching a worktree in a different session actually moves the client.
	assertCall(t, r, 0, "select-window", "-t", "main:5")
	assertCall(t, r, 1, "switch-client", "-t", "main:5")
}

func TestSelectSession_SwitchesToSessionOnly(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil) // switch-client

	err := SelectSessionWith(r, "repo-a:2")
	if err != nil {
		t.Fatalf("SelectSessionWith: unexpected error: %v", err)
	}

	// Only switch-client, targeting the bare session name so tmux restores
	// the session's last-active window (not the index in the Target).
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call (switch-client), got %d: %v", len(r.calls), r.calls)
	}
	assertCall(t, r, 0, "switch-client", "-t", "repo-a")
}

func TestSelectSession_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	if err := SelectSessionWith(r, "repo-a:0"); !errors.Is(err, ErrNotAvailable) {
		t.Errorf("SelectSessionWith: got %v, want ErrNotAvailable", err)
	}
}

func TestSessionOf(t *testing.T) {
	cases := map[Target]string{
		"repo-a:2":     "repo-a",
		"my/repo:0":    "my/repo",
		"weird:1:2":    "weird:1",
		"bare-session": "bare-session",
	}
	for in, want := range cases {
		if got := sessionOf(in); got != want {
			t.Errorf("sessionOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelect_SwitchClientErrorPropagates(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil)                              // select-window succeeds
	r.push("", errors.New("can't find session")) // switch-client fails

	err := SelectWith(r, "main:5")
	if err == nil {
		t.Fatal("expected error when switch-client fails, got nil")
	}
	if !strings.Contains(err.Error(), "switch-client") {
		t.Errorf("error should mention switch-client, got: %v", err)
	}
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

// ---- KillWindow -------------------------------------------------------------

func TestKillWindow_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	if err := KillWindowWith(r, "main:1"); !errors.Is(err, ErrNotAvailable) {
		t.Errorf("KillWindowWith: got %v, want ErrNotAvailable", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no tmux calls when not available, got %d", len(r.calls))
	}
}

func TestKillWindow_BuildsCorrectArgv(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil)

	if err := KillWindowWith(r, "main:3"); err != nil {
		t.Fatalf("KillWindowWith: unexpected error: %v", err)
	}
	assertCall(t, r, 0, "kill-window", "-t", "main:3")
}

func TestKillWindow_RunnerError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", errors.New("no such window"))

	if err := KillWindowWith(r, "main:99"); err == nil {
		t.Error("expected error when runner fails, got nil")
	}
}

// ---- KillSession ------------------------------------------------------------

func TestKillSession_NotAvailable(t *testing.T) {
	withoutTMUX(t)
	r := &fakeRunner{}
	if err := KillSessionWith(r, "repo-a:0"); !errors.Is(err, ErrNotAvailable) {
		t.Errorf("KillSessionWith: got %v, want ErrNotAvailable", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no tmux calls when not available, got %d", len(r.calls))
	}
}

func TestKillSession_BuildsCorrectArgv(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", nil)

	if err := KillSessionWith(r, "repo-a:3"); err != nil {
		t.Fatalf("KillSessionWith: unexpected error: %v", err)
	}
	assertCall(t, r, 0, "kill-session", "-t", "repo-a")
}

func TestKillSession_RunnerError(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	r.push("", errors.New("no such session"))

	if err := KillSessionWith(r, "repo-a:99"); err == nil {
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
