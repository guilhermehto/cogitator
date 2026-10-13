// Package tmuxinstall wires cogitator's tmux key bindings into the user's tmux
// config on explicit request. It writes a cogitator-owned bindings file and
// adds one source-file line to the user's tmux.conf; both are reversible and
// nothing else in the user's config (formats, themes) is ever touched.
//
// No import of bubbletea or internal/ui is permitted in this package.
package tmuxinstall

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const (
	DefaultNextKey = "Space"
	DefaultPickKey = "a"

	ownedFileHeader = "# managed by cogitator — regenerate with `cogitator tmux install`"
	prefixKeyTable  = "prefix"
)

var boundKeyLine = regexp.MustCompile(`^bind(?:-key)?\s+(\S+)\s`)

// Options are the user-facing flags shared by Install and Uninstall.
type Options struct {
	NextKey    string
	PickKey    string
	ConfigPath string
	Binary     string
	DryRun     bool
	NoReload   bool
}

// Installer holds the environment seams so tests never read the real home,
// PATH, or tmux server.
type Installer struct {
	Home          string
	XDGConfigHome string
	OwnedPath     string
	LookPath      func(file string) (string, error)
	Executable    func() (string, error)
	Tmux          tmuxctl.Runner
	Out           io.Writer
	Warn          io.Writer
}

// Install writes the owned bindings file, links it from the user's tmux.conf,
// and applies it to the running tmux server.
func (i Installer) Install(opts Options) error {
	if opts.NextKey == "" {
		opts.NextKey = DefaultNextKey
	}
	if opts.PickKey == "" {
		opts.PickKey = DefaultPickKey
	}

	for _, key := range []string{opts.NextKey, opts.PickKey} {
		if err := validateKey(key); err != nil {
			return err
		}
	}

	binary, err := i.resolveBinary(opts.Binary)
	if err != nil {
		return err
	}

	owned := ownedFileContent(binary, opts.NextKey, opts.PickKey)
	sourceLine := sourceFileLine(i.OwnedPath)

	confPath, err := i.resolveUserConfig(opts.ConfigPath)
	if err != nil {
		return err
	}

	existing, mode, err := readConfig(confPath)
	if err != nil {
		return err
	}

	updated, linked := withSourceLine(existing, i.OwnedPath, sourceLine)

	if opts.DryRun {
		fmt.Fprintf(i.Out, "would write %s:\n%s", i.OwnedPath, owned)
		if linked {
			fmt.Fprintf(i.Out, "would append to %s: %s\n", confPath, sourceLine)
		} else {
			fmt.Fprintf(i.Out, "%s already sources %s\n", confPath, i.OwnedPath)
		}
		return nil
	}

	if err := writeFileAtomic(i.OwnedPath, []byte(owned), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", i.OwnedPath, err)
	}
	fmt.Fprintf(i.Out, "wrote %s (next: prefix+%s, pick: prefix+%s)\n", i.OwnedPath, opts.NextKey, opts.PickKey)

	if linked {
		if err := writeFileAtomic(confPath, []byte(updated), mode); err != nil {
			return fmt.Errorf("write %s: %w", confPath, err)
		}
		fmt.Fprintf(i.Out, "added %q to %s\n", sourceLine, confPath)
	} else {
		fmt.Fprintf(i.Out, "%s already sources %s\n", confPath, i.OwnedPath)
	}

	if opts.NoReload {
		return nil
	}

	return i.reload(opts)
}

// Uninstall removes the source-file line and the owned file, and unbinds the
// keys in the running server when they still run cogitator.
func (i Installer) Uninstall(opts Options) error {
	confPath, err := i.resolveUserConfig(opts.ConfigPath)
	if err != nil {
		return err
	}

	existing, mode, err := readConfig(confPath)
	if err != nil {
		return err
	}

	stripped, removedLines := withoutSourceLines(existing, i.OwnedPath)
	keys := i.ownedBoundKeys()

	if opts.DryRun {
		fmt.Fprintf(i.Out, "would remove %d source line(s) from %s\n", removedLines, confPath)
		fmt.Fprintf(i.Out, "would delete %s\n", i.OwnedPath)
		return nil
	}

	if removedLines > 0 {
		if err := writeFileAtomic(confPath, []byte(stripped), mode); err != nil {
			return fmt.Errorf("write %s: %w", confPath, err)
		}
		fmt.Fprintf(i.Out, "removed %d source line(s) from %s\n", removedLines, confPath)
	}

	if err := os.Remove(i.OwnedPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", i.OwnedPath, err)
	}
	fmt.Fprintf(i.Out, "deleted %s\n", i.OwnedPath)

	if opts.NoReload {
		return nil
	}

	i.unbindCogitatorKeys(keys)

	return nil
}

func (i Installer) reload(opts Options) error {
	if !i.serverRunning() {
		fmt.Fprintln(i.Out, "no running tmux server; bindings apply on next tmux start")
		return nil
	}

	i.warnOnConflicts([]string{opts.NextKey, opts.PickKey})

	if _, err := i.Tmux.Run("source-file", i.OwnedPath); err != nil {
		return fmt.Errorf("apply bindings to running tmux: %w", err)
	}
	fmt.Fprintln(i.Out, "applied bindings to running tmux server")

	return nil
}

func (i Installer) serverRunning() bool {
	if i.Tmux == nil {
		return false
	}
	_, err := i.Tmux.Run("list-keys", "-T", prefixKeyTable, "Space")

	return err == nil
}

func (i Installer) warnOnConflicts(keys []string) {
	for _, key := range keys {
		binding := i.currentBinding(key)
		if binding != "" && !runsCogitator(binding) {
			fmt.Fprintf(i.Warn, "warning: prefix+%s was bound to %q; replacing it\n", key, strings.TrimSpace(binding))
		}
	}
}

func (i Installer) unbindCogitatorKeys(keys []string) {
	if !i.serverRunning() {
		return
	}

	for _, key := range keys {
		if !runsCogitator(i.currentBinding(key)) {
			continue
		}
		if _, err := i.Tmux.Run("unbind-key", "-T", prefixKeyTable, key); err != nil {
			fmt.Fprintf(i.Warn, "warning: could not unbind prefix+%s: %v\n", key, err)
			continue
		}
		fmt.Fprintf(i.Out, "unbound prefix+%s in running tmux server\n", key)
	}
}

func (i Installer) currentBinding(key string) string {
	out, err := i.Tmux.Run("list-keys", "-T", prefixKeyTable, key)
	if err != nil {
		return ""
	}

	return out
}

// ownedBoundKeys reads the keys from the owned file so uninstall honours
// custom --next-key/--pick-key without any extra persisted state.
func (i Installer) ownedBoundKeys() []string {
	content, err := os.ReadFile(i.OwnedPath)
	if err != nil {
		return nil
	}

	var keys []string
	for _, line := range strings.Split(string(content), "\n") {
		if match := boundKeyLine.FindStringSubmatch(line); match != nil {
			keys = append(keys, match[1])
		}
	}

	return keys
}

func runsCogitator(binding string) bool {
	return strings.Contains(binding, " next --client") || strings.Contains(binding, " pick --client")
}

func validateKey(key string) error {
	if strings.ContainsAny(key, " \t\r\n\"'\\;$") {
		return fmt.Errorf("invalid tmux key %q", key)
	}

	return nil
}

func (i Installer) resolveBinary(flagValue string) (string, error) {
	path := flagValue

	if path == "" {
		if found, err := i.LookPath("cogitator"); err == nil {
			path = found
		}
	}

	if path == "" {
		exe, err := i.Executable()
		if err != nil {
			return "", fmt.Errorf("locate cogitator binary: %w", err)
		}
		path = exe
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve binary path %q: %w", path, err)
	}

	if isGoRunBinary(abs) {
		return "", fmt.Errorf("%s is a temporary `go run` binary; install cogitator first or pass --binary", abs)
	}

	return abs, nil
}

func isGoRunBinary(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if strings.HasPrefix(part, "go-build") {
			return true
		}
	}

	return false
}

// resolveUserConfig follows tmux's own lookup order and resolves symlinks so
// the atomic write replaces the dotfiles target, not the link.
func (i Installer) resolveUserConfig(explicit string) (string, error) {
	path := explicit

	if path == "" {
		path = i.firstExistingDefaultConfig()
	}

	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}

	return resolved, nil
}

func (i Installer) firstExistingDefaultConfig() string {
	homeConf := filepath.Join(i.Home, ".tmux.conf")
	candidates := []string{homeConf}

	if i.XDGConfigHome != "" {
		candidates = append(candidates, filepath.Join(i.XDGConfigHome, "tmux", "tmux.conf"))
	}
	candidates = append(candidates, filepath.Join(i.Home, ".config", "tmux", "tmux.conf"))

	for _, candidate := range candidates {
		if _, err := os.Lstat(candidate); err == nil {
			return candidate
		}
	}

	return homeConf
}

func readConfig(path string) (content string, mode fs.FileMode, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0o644, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", path, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}

	return string(data), info.Mode().Perm(), nil
}

// withSourceLine appends the source line unless a line already references the
// owned path. The bool reports whether the content changed.
func withSourceLine(content, ownedPath, sourceLine string) (string, bool) {
	if referencingLines(content, ownedPath) > 0 {
		return content, false
	}

	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	return content + sourceLine + "\n", true
}

func withoutSourceLines(content, ownedPath string) (string, int) {
	lines := strings.SplitAfter(content, "\n")
	kept := lines[:0]
	removed := 0

	for _, line := range lines {
		if referencesPath(line, ownedPath) {
			removed++
			continue
		}
		kept = append(kept, line)
	}

	return strings.Join(kept, ""), removed
}

func referencingLines(content, ownedPath string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if referencesPath(line, ownedPath) {
			count++
		}
	}

	return count
}

func referencesPath(line, ownedPath string) bool {
	trimmed := strings.TrimSpace(line)

	return !strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, ownedPath)
}

func sourceFileLine(ownedPath string) string {
	return "source-file -q " + tmuxQuote(ownedPath)
}

// ownedFileContent renders the bindings. The command string passes through
// tmux's config parser (tmuxQuote) and then sh (shellQuote); the #{...}
// formats stay literal so tmux expands them when the key fires.
func ownedFileContent(binary, nextKey, pickKey string) string {
	quotedBinary := shellQuote(escapeTmuxFormat(binary))
	nextCommand := quotedBinary + ` next --client '#{client_name}' --window '#{window_id}' --session '#{session_id}'`
	pickCommand := quotedBinary + ` pick --client '#{client_name}'`

	return strings.Join([]string{
		ownedFileHeader,
		fmt.Sprintf("bind %s run-shell -b %s", nextKey, tmuxQuote(nextCommand)),
		fmt.Sprintf("bind %s display-popup -E -w 70%% -h 60%% -b rounded -T ' agents ' %s", pickKey, tmuxQuote(pickCommand)),
		"",
	}, "\n")
}

func escapeTmuxFormat(s string) string {
	return strings.ReplaceAll(s, "#", "##")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var plainTmuxWord = regexp.MustCompile(`^[A-Za-z0-9/._+=:@%,~-]+$`)

func tmuxQuote(s string) string {
	if plainTmuxWord.MatchString(s) {
		return s
	}

	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s)

	return `"` + escaped + `"`
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
