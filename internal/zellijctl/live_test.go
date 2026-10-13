//go:build live

// Live integration test for zellijctl. Run with:
//
//	go test -race -tags live -run Live ./internal/zellijctl/ -v
//
// It starts a scratch zellij session hosted by a client in a private tmux
// server (headless sessions don't materialise new tabs) and never touches
// other zellij sessions.
package zellijctl

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/mux"
)

// unsetZellijEnv drops inherited zellij variables so `--session` alone picks
// the scratch session even when the test runs inside zellij.
func unsetZellijEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_PANE_ID"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

func run(bin string, args ...string) (string, error) {
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, out)
	}

	return string(out), nil
}

func startScratchSession(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"zellij", "tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	unsetZellijEnv(t)

	name := "cogtest-" + strings.ToLower(rand.Text()[:8])
	var socket string
	t.Cleanup(func() {
		_, _ = run("tmux", "-L", name, "kill-server")
		_, _ = run("zellij", "kill-session", name)
		_, _ = run("zellij", "delete-session", name)
		if socket != "" {
			_ = os.Remove(socket)
		}
	})

	if _, err := run("zellij", "attach", "--create-background", name); err != nil {
		t.Fatal(err)
	}
	if _, err := run("tmux", "-L", name, "new-session", "-d", "-x", "160", "-y", "45", "zellij attach "+name); err != nil {
		t.Fatal(err)
	}

	out, err := run("tmux", "-L", name, "display-message", "-p", "#{socket_path}")
	if err != nil {
		t.Fatal(err)
	}
	socket = strings.TrimSpace(out)

	time.Sleep(2 * time.Second)
	if _, err := run("tmux", "-L", name, "send-keys", "Escape"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	return name
}

// liveBackend acts as if run inside session: implicit actions get its
// --session, everything else (other sessions, list/attach/delete) passes as is.
func liveBackend(session string) Backend {
	runner := RunnerFunc(func(args ...string) (string, error) {
		if len(args) > 0 && args[0] == "action" {
			args = append([]string{"--session", session}, args...)
		}
		return DefaultRunner.Run(args...)
	})
	vars := map[string]string{"ZELLIJ": "0", "ZELLIJ_SESSION_NAME": session}
	return Backend{Runner: runner, Getenv: func(k string) string { return vars[k] }}
}

func eventually(t *testing.T, what string, cond func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok, err := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: timed out (last err: %v)", what, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestLiveLoop(t *testing.T) {
	home := startScratchSession(t)
	b := liveBackend(home)

	dir := t.TempDir()
	running := liveRunningTab(t, b, dir)
	held := liveHeldTab(t, b)

	panes, err := b.ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	for _, p := range panes {
		if p.SessionID == home {
			t.Logf("pane %+v", p)
		}
	}

	for _, tab := range []mux.Target{running, held} {
		if err := b.Kill(tab, mux.ModeWindow); err != nil {
			t.Fatalf("Kill %s: %v", tab, err)
		}
	}
	eventually(t, "tabs closed", func() (bool, error) {
		_, err1 := b.WindowProcessAlive(running)
		_, err2 := b.WindowProcessAlive(held)
		return errors.Is(err1, mux.ErrWindowNotFound) && errors.Is(err2, mux.ErrWindowNotFound), nil
	})
	if _, err := b.FindWindowByDir(dir); !errors.Is(err, mux.ErrWindowNotFound) {
		t.Fatalf("FindWindowByDir after Kill err = %v, want ErrWindowNotFound", err)
	}
}

// TestLiveSessionMode launches a worktree session from scratch session A,
// reads it from A, moves A's client there and back, then kills it.
func TestLiveSessionMode(t *testing.T) {
	home := startScratchSession(t)
	b := liveBackend(home)

	want := home + "-wt"
	t.Cleanup(func() { _, _ = run("zellij", "delete-session", "--force", want) })

	dir := t.TempDir()
	target, err := b.EnsureWindow(dir, home+"/wt", []string{"sh", "-c", "pwd; echo COGSESSION; sleep 60"}, mux.ModeSession)
	if err != nil {
		t.Fatalf("EnsureWindow: %v", err)
	}
	session, _, err := splitAddress(string(target))
	if err != nil || session != want {
		t.Fatalf("EnsureWindow = %q, want a target in session %s", target, want)
	}
	t.Logf("EnsureWindow -> %s; sessions:\n%s", target, mustRun(t, "zellij", "list-sessions", "-n"))

	found, err := b.FindWindowByDir(dir)
	if err != nil || found != target {
		t.Fatalf("FindWindowByDir = %q, %v; want %q", found, err, target)
	}

	var text string
	eventually(t, "Capture COGSESSION", func() (bool, error) {
		text, err = b.Capture(string(target), 20)
		return strings.Contains(text, "COGSESSION"), err
	})
	t.Logf("Capture(%s) = %q", target, text)
	if !strings.Contains(text, canonical(dir)) {
		t.Fatalf("harness cwd: capture %q lacks %s", text, canonical(dir))
	}

	liveSwitch(t, b, target, home, session)

	if err := b.Kill(target, mux.ModeSession); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	eventually(t, "session deleted", func() (bool, error) {
		sessions, err := b.listSessions()
		return err == nil && !slices.ContainsFunc(sessions, func(s sessionInfo) bool { return s.name == session }), err
	})
	t.Logf("after Kill; sessions:\n%s", mustRun(t, "zellij", "list-sessions", "-n"))
}

// liveSwitch moves home's client into target's session with Select, then
// back to home's first pane with SelectPane, checking list-clients each time.
// A keypress precedes each move: zellij ignores outside switches until the
// client has had input in its session.
func liveSwitch(t *testing.T, b Backend, target mux.Target, home, session string) {
	t.Helper()
	keypress(t, home)
	if err := b.Select(target, mux.ModeSession, ""); err != nil {
		t.Fatalf("Select: %v", err)
	}
	eventually(t, "client in "+session, func() (bool, error) { return hasClient(session) })
	clients := mustRun(t, "zellij", "--session", session, "action", "list-clients")
	t.Logf("list-clients %s:\n%s", session, clients)
	if !strings.Contains(clients, "COGSESSION") {
		t.Fatalf("client in %s is not on the harness pane", session)
	}

	panes, err := b.ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	back := slices.IndexFunc(panes, func(p mux.Pane) bool { return p.SessionID == home })
	if back < 0 {
		t.Fatalf("no pane of %s in %+v", home, panes)
	}

	keypress(t, home)
	if err := b.SelectPane(panes[back].PaneID, session); err != nil {
		t.Fatalf("SelectPane back: %v", err)
	}
	eventually(t, "client back in "+home, func() (bool, error) { return hasClient(home) })
}

// keypress gives the client hosted by tmux server sock some input and lets
// zellij register it.
func keypress(t *testing.T, sock string) {
	t.Helper()
	mustRun(t, "tmux", "-L", sock, "send-keys", "Escape")
	time.Sleep(500 * time.Millisecond)
}

func hasClient(session string) (bool, error) {
	out, err := run("zellij", "--session", session, "action", "list-clients")
	if err != nil {
		return false, err
	}
	return len(strings.Split(strings.TrimSpace(out), "\n")) > 1, nil
}

func mustRun(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, err := run(bin, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// liveRunningTab opens a tab in dir and checks find, dedup, liveness, capture
// and select.
func liveRunningTab(t *testing.T, b Backend, dir string) mux.Target {
	t.Helper()
	argv := []string{"sh", "-c", "echo COGLIVE; sleep 60"}

	target, err := b.EnsureWindow(dir, "cog/live", argv, mux.ModeWindow)
	if err != nil {
		t.Fatalf("EnsureWindow: %v", err)
	}
	t.Logf("EnsureWindow -> tab %s", target)

	eventually(t, "FindWindowByDir", func() (bool, error) {
		found, err := b.FindWindowByDir(dir)
		return found == target, err
	})

	again, err := b.EnsureWindow(dir, "cog/live", argv, mux.ModeWindow)
	if err != nil || again != target {
		t.Fatalf("EnsureWindow dedup = %q, %v; want %q", again, err, target)
	}

	alive, err := b.WindowProcessAlive(target)
	if err != nil || !alive {
		t.Fatalf("WindowProcessAlive = %v, %v; want true", alive, err)
	}

	var text string
	eventually(t, "Capture COGLIVE", func() (bool, error) {
		text, err = b.Capture(string(target), 20)
		return strings.Contains(text, "COGLIVE"), err
	})
	t.Logf("Capture(%s) = %q", target, text)

	if err := b.Select(target, mux.ModeWindow, ""); err != nil {
		t.Fatalf("Select: %v", err)
	}
	return target
}

// liveHeldTab opens a tab whose command exits, then checks it reports dead,
// is still found by dir, and is revived in place in that dir.
func liveHeldTab(t *testing.T, b Backend) mux.Target {
	t.Helper()
	dir := t.TempDir()

	held, err := b.EnsureWindow(dir, "cog/held", []string{"sh", "-c", "exit 3"}, mux.ModeWindow)
	if err != nil {
		t.Fatalf("EnsureWindow held: %v", err)
	}
	eventually(t, "held pane reports not alive", func() (bool, error) {
		alive, err := b.WindowProcessAlive(held)
		return err == nil && !alive, err
	})

	found, err := b.FindWindowByDir(dir)
	if err != nil || found != held {
		t.Fatalf("FindWindowByDir(held) = %q, %v; want %q", found, err, held)
	}

	if err := b.RelaunchInWindow(held, []string{"sh", "-c", "pwd; echo REVIVED; sleep 60"}); err != nil {
		t.Fatalf("RelaunchInWindow: %v", err)
	}
	eventually(t, "relaunched pane alive", func() (bool, error) {
		return b.WindowProcessAlive(held)
	})

	var text string
	eventually(t, "relaunched pane output", func() (bool, error) {
		text, err = b.Capture(string(held), 20)
		return strings.Contains(text, "REVIVED"), err
	})
	t.Logf("Capture(%s) after relaunch = %q", held, text)
	if !strings.Contains(text, canonical(dir)) {
		t.Fatalf("relaunched pane cwd: capture %q lacks %s", text, canonical(dir))
	}
	return held
}
