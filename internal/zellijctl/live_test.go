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

func liveBackend(session string) Backend {
	runner := RunnerFunc(func(args ...string) (string, error) {
		return DefaultRunner.Run(append([]string{"--session", session}, args...)...)
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
	b := liveBackend(startScratchSession(t))

	dir := t.TempDir()
	running := liveRunningTab(t, b, dir)
	held := liveHeldTab(t, b)

	panes, err := b.ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	for _, p := range panes {
		t.Logf("pane %+v", p)
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
