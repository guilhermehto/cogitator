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

	"github.com/guilhermehto/cogitator/internal/installkit"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const (
	DefaultNextKey = "Space"
	DefaultPickKey = "a"

	ownedFileHeader = "# managed by cogitator — regenerate with `cogitator tmux install`"
	prefixKeyTable  = "prefix"
)

var (
	boundKeyLine     = regexp.MustCompile(`^bind(?:-key)?\s+(\S+)\s`)
	bindCommandLine  = regexp.MustCompile(`^bind(?:-key)?\s`)
	sourceCommand    = regexp.MustCompile(`^source(?:-file)?\s+(.*)$`)
	cogitatorCommand = regexp.MustCompile(`cogitator\S*\s+(?:pick|next)\b`)
)

// Options are the user-facing flags shared by Install and Uninstall.
type Options struct {
	NextKey    string
	PickKey    string
	ConfigPath string
	Binary     string
	DryRun     bool
	NoReload   bool
	Force      bool
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

// plan is the resolved environment shared by Check and Install so both see
// the same binary, config path, and status.
type plan struct {
	opts       Options
	owned      string
	confPath   string
	existing   string
	mode       fs.FileMode
	sourceLine string
	status     installkit.Status
}

// Check reports how the user's tmux wiring compares with what Install would
// write now. It never writes anything.
func (i Installer) Check(opts Options) (installkit.Status, error) {
	p, err := i.plan(opts)
	if err != nil {
		return installkit.Status{}, err
	}

	return p.status, nil
}

// Install writes the owned bindings file, links it from the user's tmux.conf,
// and applies it to the running tmux server.
func (i Installer) Install(opts Options) error {
	p, err := i.plan(opts)
	if err != nil {
		return err
	}

	if p.status.State == installkit.Foreign && !opts.Force {
		return &installkit.ForeignError{Status: p.status}
	}

	if p.status.State == installkit.Current {
		fmt.Fprintf(i.Out, "%s already set up (next: prefix+%s, pick: prefix+%s)\n", i.OwnedPath, p.opts.NextKey, p.opts.PickKey)
		if opts.DryRun || opts.NoReload {
			return nil
		}
		return i.reload(p.opts)
	}

	if opts.DryRun {
		i.printDryRun(p)
		return nil
	}

	if err := i.write(p); err != nil {
		return err
	}

	if opts.NoReload {
		return nil
	}

	return i.reload(p.opts)
}

func (i Installer) plan(opts Options) (plan, error) {
	if opts.NextKey == "" {
		opts.NextKey = DefaultNextKey
	}
	if opts.PickKey == "" {
		opts.PickKey = DefaultPickKey
	}

	for _, key := range []string{opts.NextKey, opts.PickKey} {
		if err := validateKey(key); err != nil {
			return plan{}, err
		}
	}

	binary, err := installkit.ResolveBinary(opts.Binary, i.LookPath, i.Executable)
	if err != nil {
		return plan{}, err
	}

	confPath, err := i.resolveUserConfig(opts.ConfigPath)
	if err != nil {
		return plan{}, err
	}

	existing, mode, err := installkit.ReadConfig(confPath)
	if err != nil {
		return plan{}, err
	}

	p := plan{
		opts:       opts,
		owned:      ownedFileContent(binary, opts.NextKey, opts.PickKey),
		confPath:   confPath,
		existing:   existing,
		mode:       mode,
		sourceLine: sourceFileLine(i.OwnedPath),
	}

	if foreign, found := i.findForeignBinding(confPath, existing); found {
		p.status = foreign
		return p, nil
	}

	p.status, err = i.managedStatus(p)
	if err != nil {
		return plan{}, err
	}

	return p, nil
}

func (i Installer) managedStatus(p plan) (installkit.Status, error) {
	current, err := os.ReadFile(i.OwnedPath)
	ownedExists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return installkit.Status{}, fmt.Errorf("read %s: %w", i.OwnedPath, err)
	}

	linked := referencingLines(p.existing, i.OwnedPath) > 0
	status := installkit.Status{Path: i.OwnedPath}

	switch {
	case !ownedExists && !linked:
		status.State = installkit.Missing
	case !ownedExists:
		status.State = installkit.Stale
		status.Detail = p.confPath + " sources it but the file is missing"
	case !linked:
		status.State = installkit.Stale
		status.Detail = p.confPath + " does not source it"
	case string(current) != p.owned:
		status.State = installkit.Stale
		status.Detail = "bindings differ from what install would write"
	default:
		status.State = installkit.Current
	}

	return status, nil
}

// findForeignBinding walks the user's tmux.conf and every file it sources
// (except the owned file) for a hand-written bind that runs cogitator.
func (i Installer) findForeignBinding(confPath, content string) (installkit.Status, bool) {
	visited := map[string]bool{filepath.Clean(i.OwnedPath): true}

	return i.scanForForeign(confPath, content, visited)
}

func (i Installer) scanForForeign(path, content string, visited map[string]bool) (installkit.Status, bool) {
	visited[filepath.Clean(path)] = true

	for n, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if bindCommandLine.MatchString(trimmed) && cogitatorCommand.MatchString(trimmed) {
			return installkit.Status{State: installkit.Foreign, Path: path, Line: n + 1, Detail: trimmed}, true
		}

		if status, found := i.scanSourced(trimmed, visited); found {
			return status, true
		}
	}

	return installkit.Status{}, false
}

func (i Installer) scanSourced(line string, visited map[string]bool) (installkit.Status, bool) {
	if referencesPath(line, i.OwnedPath) {
		return installkit.Status{}, false
	}

	for _, sourced := range i.sourcedFiles(line) {
		if visited[filepath.Clean(sourced)] {
			continue
		}

		data, err := os.ReadFile(sourced)
		if err != nil {
			visited[filepath.Clean(sourced)] = true
			continue
		}

		if status, found := i.scanForForeign(sourced, string(data), visited); found {
			return status, true
		}
	}

	return installkit.Status{}, false
}

// sourcedFiles expands the paths of a source/source-file line; flags are
// ignored and relative paths skipped because their base is ambiguous.
func (i Installer) sourcedFiles(line string) []string {
	match := sourceCommand.FindStringSubmatch(line)
	if match == nil {
		return nil
	}

	var files []string
	for _, arg := range strings.Fields(match[1]) {
		if strings.HasPrefix(arg, "-") {
			continue
		}

		path := strings.Trim(arg, `'"`)
		if strings.HasPrefix(path, "~/") {
			path = filepath.Join(i.Home, path[2:])
		}
		if !filepath.IsAbs(path) {
			continue
		}

		matches, err := filepath.Glob(path)
		if err != nil {
			continue
		}
		files = append(files, matches...)
	}

	return files
}

func (i Installer) printDryRun(p plan) {
	_, linked := withSourceLine(p.existing, i.OwnedPath, p.sourceLine)

	fmt.Fprintf(i.Out, "would write %s:\n%s", i.OwnedPath, p.owned)
	if linked {
		fmt.Fprintf(i.Out, "would append to %s: %s\n", p.confPath, p.sourceLine)
	} else {
		fmt.Fprintf(i.Out, "%s already sources %s\n", p.confPath, i.OwnedPath)
	}
}

func (i Installer) write(p plan) error {
	if err := installkit.WriteFileAtomic(i.OwnedPath, []byte(p.owned), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", i.OwnedPath, err)
	}
	fmt.Fprintf(i.Out, "wrote %s (next: prefix+%s, pick: prefix+%s)\n", i.OwnedPath, p.opts.NextKey, p.opts.PickKey)

	updated, linked := withSourceLine(p.existing, i.OwnedPath, p.sourceLine)
	if !linked {
		fmt.Fprintf(i.Out, "%s already sources %s\n", p.confPath, i.OwnedPath)
		return nil
	}

	if err := installkit.WriteFileAtomic(p.confPath, []byte(updated), p.mode); err != nil {
		return fmt.Errorf("write %s: %w", p.confPath, err)
	}
	fmt.Fprintf(i.Out, "added %q to %s\n", p.sourceLine, p.confPath)

	return nil
}

// Uninstall removes the source-file line and the owned file, and unbinds the
// keys in the running server when they still run cogitator.
func (i Installer) Uninstall(opts Options) error {
	confPath, err := i.resolveUserConfig(opts.ConfigPath)
	if err != nil {
		return err
	}

	existing, mode, err := installkit.ReadConfig(confPath)
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
		if err := installkit.WriteFileAtomic(confPath, []byte(stripped), mode); err != nil {
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

// resolveUserConfig follows tmux's own lookup order and resolves symlinks so
// the atomic write replaces the dotfiles target, not the link.
func (i Installer) resolveUserConfig(explicit string) (string, error) {
	path := explicit

	if path == "" {
		path = i.firstExistingDefaultConfig()
	}

	return installkit.ResolveSymlinks(path)
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
