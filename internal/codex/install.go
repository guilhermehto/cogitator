package codex

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/guilhermehto/cogitator/internal/installkit"
)

// HookSubcommand is the cogitator subcommand Codex's hooks run.
const HookSubcommand = "codex-hook"

// HookEvents are the Codex lifecycle events cogitator listens to.
var HookEvents = []installkit.HookEvent{
	{Name: "SessionStart"},
	{Name: "UserPromptSubmit"},
	{Name: "PreToolUse", Matcher: "*"},
	{Name: "PostToolUse", Matcher: "*"},
	{Name: "PermissionRequest", Matcher: "*"},
	{Name: "Stop"},
}

// HomeDir is the Codex home directory: codexHome when set, else ~/.codex.
func HomeDir(codexHome string) (string, error) {
	if codexHome != "" {
		return codexHome, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("codex: resolve home directory: %w", err)
	}

	return filepath.Join(home, ".codex"), nil
}

// CheckHooks reports how cogitator's hooks in <codex home>/hooks.json compare
// with what InstallHooks would write for cogitatorBin. Hooks wired inline in
// config.toml count as Foreign. It never writes. codexHome "" means ~/.codex.
func CheckHooks(codexHome, cogitatorBin string) (installkit.Status, error) {
	spec, err := hookSpec(codexHome)
	if err != nil {
		return installkit.Status{}, err
	}

	foreign, err := findTOMLHook(filepath.Join(filepath.Dir(spec.Path), "config.toml"))
	if err != nil {
		return installkit.Status{}, err
	}

	if foreign != nil {
		return *foreign, nil
	}

	return installkit.CheckHooks(spec, cogitatorBin)
}

// InstallHooks merges cogitator's hooks into <codex home>/hooks.json,
// creating it if missing. It returns the status found before writing:
// Current writes nothing; Foreign is refused with *installkit.ForeignError
// unless force is set.
func InstallHooks(codexHome, cogitatorBin string, force bool) (installkit.Status, error) {
	spec, err := hookSpec(codexHome)
	if err != nil {
		return installkit.Status{}, err
	}

	foreign, err := findTOMLHook(filepath.Join(filepath.Dir(spec.Path), "config.toml"))
	if err != nil {
		return installkit.Status{}, err
	}

	if foreign != nil && !force {
		return *foreign, &installkit.ForeignError{Status: *foreign}
	}

	return installkit.InstallHooks(spec, cogitatorBin, force)
}

func hookSpec(codexHome string) (installkit.HookSpec, error) {
	dir, err := HomeDir(codexHome)
	if err != nil {
		return installkit.HookSpec{}, err
	}

	home, _ := os.UserHomeDir()

	return installkit.HookSpec{
		Path:       filepath.Join(dir, "hooks.json"),
		Subcommand: HookSubcommand,
		Events:     HookEvents,
		Home:       home,
	}, nil
}

// findTOMLHook returns a Foreign status for the first uncommented line of
// config.toml that runs codex-hook: the inline-TOML form of the same hooks.
func findTOMLHook(path string) (*installkit.Status, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("codex: read %s: %w", path, err)
	}

	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, HookSubcommand) {
			continue
		}

		return &installkit.Status{
			State:  installkit.Foreign,
			Path:   path,
			Line:   i + 1,
			Detail: trimmed,
		}, nil
	}

	return nil, nil
}
