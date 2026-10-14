package claudecode

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/guilhermehto/cogitator/internal/installkit"
)

// HookSubcommand is the cogitator subcommand Claude Code's hooks run.
const HookSubcommand = "claude-hook"

// HookEvents are the Claude Code lifecycle events cogitator listens to.
var HookEvents = []installkit.HookEvent{
	{Name: "SessionStart"},
	{Name: "UserPromptSubmit"},
	{Name: "PreToolUse", Matcher: "*"},
	{Name: "PostToolUse", Matcher: "*"},
	{Name: "Stop"},
	{Name: "Notification"},
	{Name: "SessionEnd"},
}

// HomeDir is the Claude home directory: claudeHome when set, else ~/.claude.
func HomeDir(claudeHome string) (string, error) {
	if claudeHome != "" {
		return claudeHome, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude code: resolve home directory: %w", err)
	}

	return filepath.Join(home, ".claude"), nil
}

// CheckHooks reports how cogitator's hooks in <claude home>/settings.json
// compare with what InstallHooks would write for cogitatorBin. It never
// writes. claudeHome "" means ~/.claude.
func CheckHooks(claudeHome, cogitatorBin string) (installkit.Status, error) {
	spec, err := hookSpec(claudeHome)
	if err != nil {
		return installkit.Status{}, err
	}

	return installkit.CheckHooks(spec, cogitatorBin)
}

// InstallHooks merges cogitator's hooks into <claude home>/settings.json,
// creating it if missing. It returns the status found before writing:
// Current writes nothing; Foreign is refused with *installkit.ForeignError
// unless force is set.
func InstallHooks(claudeHome, cogitatorBin string, force bool) (installkit.Status, error) {
	spec, err := hookSpec(claudeHome)
	if err != nil {
		return installkit.Status{}, err
	}

	return installkit.InstallHooks(spec, cogitatorBin, force)
}

func hookSpec(claudeHome string) (installkit.HookSpec, error) {
	dir, err := HomeDir(claudeHome)
	if err != nil {
		return installkit.HookSpec{}, err
	}

	home, _ := os.UserHomeDir()

	return installkit.HookSpec{
		Path:       filepath.Join(dir, "settings.json"),
		Subcommand: HookSubcommand,
		Events:     HookEvents,
		Home:       home,
	}, nil
}
