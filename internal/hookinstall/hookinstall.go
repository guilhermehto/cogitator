// Package hookinstall wires cogitator's hooks into every agent harness found
// on this machine in one step, each pointing at the same cogitator binary.
package hookinstall

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/installkit"
	"github.com/guilhermehto/cogitator/internal/omp"
)

// Harness is one agent harness Install can wire. FollowUp is what the user
// still has to do after cogitator writes the harness's hooks.
type Harness struct {
	Name     string
	Detected func() bool
	Install  func(binary string, force bool) (installkit.Status, error)
	FollowUp func(binary string) string
}

// Harnesses are Claude Code, Codex, and omp, detected the way cfg detects
// them, except Claude Code counts as present once its home directory exists:
// its hooks live there before any session has been recorded.
func Harnesses(cfg *config.Config) []Harness {
	return []Harness{
		{
			Name:     "Claude Code",
			Detected: func() bool { return claudeHomeExists(cfg.ClaudeCodeHome) },
			Install: func(binary string, force bool) (installkit.Status, error) {
				return claudecode.InstallHooks(cfg.ClaudeCodeHome, binary, force)
			},
			FollowUp: func(string) string { return "Restart Claude Code for the hooks to take effect." },
		},
		{
			Name:     "Codex",
			Detected: func() bool { return cfg.CodexEnabled },
			Install: func(binary string, force bool) (installkit.Status, error) {
				return codex.InstallHooks(cfg.CodexHome, binary, force)
			},
			FollowUp: func(binary string) string {
				return fmt.Sprintf("Start codex, run /hooks, and trust `%s`.", installkit.HookCommand(binary, codex.HookSubcommand))
			},
		},
		{
			Name:     "omp",
			Detected: func() bool { return cfg.OmpEnabled },
			Install: func(binary string, force bool) (installkit.Status, error) {
				return omp.InstallExtension(cfg.OmpHome, binary, force)
			},
			FollowUp: func(string) string { return "Restart any running omp sessions for the extension to take effect." },
		},
	}
}

func claudeHomeExists(claudeHome string) bool {
	dir, err := claudecode.HomeDir(claudeHome)
	if err != nil {
		return false
	}

	info, err := os.Stat(dir)

	return err == nil && info.IsDir()
}

// Install runs every detected harness's installer with binary, writing one
// line per harness, then opencode's, then the follow-ups for what changed.
// Undetected harnesses are skipped without touching their directories, and
// wiring the user made by hand is reported and left alone unless force is
// set. It keeps going past a failing harness and returns an error naming
// every one that failed.
func Install(harnesses []Harness, binary string, force bool, out io.Writer) error {
	var failed, followUps []string

	for _, harness := range harnesses {
		if !harness.Detected() {
			fmt.Fprintf(out, "%s: not found, skipped\n", harness.Name)
			continue
		}

		status, err := harness.Install(binary, force)
		line, changed := outcome(status, err)
		fmt.Fprintf(out, "%s: %s\n", harness.Name, line)

		if err != nil && !isForeign(err) {
			failed = append(failed, harness.Name)
		}

		if changed {
			followUps = append(followUps, harness.FollowUp(binary))
		}
	}

	fmt.Fprintln(out, "opencode: no hook needed (discovered over mDNS)")

	if len(followUps) > 0 {
		fmt.Fprintln(out, "\nNext:")
		for _, followUp := range followUps {
			fmt.Fprintf(out, "  %s\n", followUp)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("hooks install failed for %s", strings.Join(failed, ", "))
	}

	return nil
}

// outcome is a harness's report line and whether its install wrote anything.
func outcome(status installkit.Status, err error) (string, bool) {
	if isForeign(err) {
		return fmt.Sprintf("yours, left alone: %s (%s)", status.Location(), status.Detail), false
	}

	if err != nil {
		return "error: " + err.Error(), false
	}

	switch status.State {
	case installkit.Current:
		return "already set up: " + status.Path, false
	case installkit.Stale:
		return "updated: " + status.Path, true
	case installkit.Foreign:
		return "installed despite your wiring at " + status.Location(), true
	default:
		return "installed: " + status.Path, true
	}
}

func isForeign(err error) bool {
	var foreign *installkit.ForeignError

	return errors.As(err, &foreign)
}
