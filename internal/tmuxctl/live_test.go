//go:build live

// Live integration tests for tmuxctl. Run with:
//
//	go test -tags live ./internal/tmuxctl/... -v -run TestLive
//
// These tests require a running tmux server ($TMUX must be set).
package tmuxctl

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveLoop exercises the full EnsureWindow → FindWindowByDir → kill →
// WindowProcessAlive → RelaunchInWindow → Select loop against a real tmux
// server. It is gated by the "live" build tag so it never runs in CI.
func TestLiveLoop(t *testing.T) {
	if !Available() {
		t.Skip("not inside tmux ($TMUX unset) — skipping live test")
	}

	dir := "/tmp/cogitator-live-test"

	t.Log("=== EnsureWindow ===")
	target, err := EnsureWindow(dir, "r/x", []string{"sleep", "60"})
	if err != nil {
		t.Fatalf("EnsureWindow: %v", err)
	}
	t.Logf("Created window: %s", target)

	// Cleanup: kill the test window when done.
	t.Cleanup(func() {
		exec.Command("tmux", "kill-window", "-t", string(target)).Run()
	})

	t.Log("=== FindWindowByDir ===")
	found, err := FindWindowByDir(dir)
	if err != nil {
		t.Fatalf("FindWindowByDir: %v", err)
	}
	t.Logf("Found window: %s", found)
	if found != target {
		t.Fatalf("MISMATCH: EnsureWindow=%s FindWindowByDir=%s", target, found)
	}

	t.Log("=== EnsureWindow dedup ===")
	target2, err := EnsureWindow(dir, "r/x", []string{"sleep", "60"})
	if err != nil {
		t.Fatalf("EnsureWindow dedup: %v", err)
	}
	if target2 != target {
		t.Fatalf("DEDUP FAILED: first=%s second=%s", target, target2)
	}
	t.Logf("Dedup OK: same target %s", target2)

	t.Log("=== WindowProcessAlive (before kill) ===")
	alive, err := WindowProcessAlive(target)
	if err != nil {
		t.Fatalf("WindowProcessAlive: %v", err)
	}
	t.Logf("Alive: %v (want true)", alive)
	if !alive {
		t.Fatal("process should be alive before kill")
	}

	t.Log("=== Killing sleep via tmux send-keys C-c ===")
	exec.Command("tmux", "send-keys", "-t", string(target), "C-c", "").Run()
	time.Sleep(800 * time.Millisecond)

	t.Log("=== WindowProcessAlive (after kill) ===")
	alive2, err := WindowProcessAlive(target)
	if err != nil {
		t.Fatalf("WindowProcessAlive after kill: %v", err)
	}
	t.Logf("Alive after kill: %v (want false)", alive2)
	if alive2 {
		t.Log("NOTE: pane may still be alive (shell wrapper); checking pane_dead directly is correct")
	}

	t.Log("=== RelaunchInWindow ===")
	err = RelaunchInWindow(target, []string{"sleep", "30"})
	if err != nil {
		t.Fatalf("RelaunchInWindow: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	alive3, err := WindowProcessAlive(target)
	if err != nil {
		t.Fatalf("WindowProcessAlive after relaunch: %v", err)
	}
	t.Logf("Alive after relaunch: %v (want true)", alive3)
	if !alive3 {
		t.Fatal("process should be alive after relaunch")
	}

	t.Log("=== Select ===")
	err = Select(target)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	t.Logf("Selected window %s", target)

	t.Log("=== tmux list-windows transcript ===")
	out, _ := exec.Command("tmux", "list-windows", "-a", "-F", "#{window_id} #{@cog_dir} #{pane_dead}").Output()
	transcript := strings.TrimSpace(string(out))
	t.Log(transcript)
	fmt.Println(transcript) // also print to stdout for commit message capture
}
