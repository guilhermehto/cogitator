// Package zellijinstall wires cogitator's next/pick keybinds into the user's
// zellij config on explicit request. KDL has no include directive, so it
// edits config.kdl in place with cogitator-owned, marker-delimited blocks;
// uninstall removes exactly those blocks and nothing else.
//
// The keybinds block goes inside the first top-level keybinds node: zellij
// ignores a second top-level keybinds node, so appending one would never
// fire. With a plugin, a top-level block also points default_layout at a
// cogitator-owned layout that docks the plugin as a sidebar in every tab.
//
// No import of bubbletea or internal/ui is permitted in this package.
package zellijinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/guilhermehto/cogitator/internal/installkit"
)

const (
	DefaultNextKey    = "Space"
	DefaultPickKey    = "a"
	DefaultSidebarKey = "g"

	sidebarWidth = 42
	focusPipe    = "cogitator_focus"
	layoutName   = "cogitator.kdl"
	layoutOwner  = "// managed by cogitator"
	layoutHeader = layoutOwner + " — regenerate with `cogitator zellij install`"
	wasmPrefix   = "cogitator-zellij-"
	wasmSuffix   = ".wasm"
	beginMarker  = "// BEGIN cogitator"
	endMarker    = "// END cogitator"
	beginLine    = beginMarker + " (managed by cogitator — regenerate with `cogitator zellij install`)"
	indentUnit   = "    "
)

const (
	kdlStringPattern   = `"(?:[^"\\]|\\.)*"`
	kdlPropertyPattern = `[\w-]+=(?:` + kdlStringPattern + `|[^\s"{;]+)`
)

var (
	keybindsNode      = regexp.MustCompile(`^(\s*)keybinds\b`)
	defaultModeNode   = regexp.MustCompile(`^\s*default_mode\s+"([^"]*)"`)
	defaultLayoutNode = regexp.MustCompile(`^\s*default_layout\b`)
	sectionNode       = regexp.MustCompile(`^\s*([A-Za-z_][\w-]*)((?:\s+(?:` + kdlPropertyPattern + `|` + kdlStringPattern + `))*)\s*\{`)
	kdlProperty       = regexp.MustCompile(kdlPropertyPattern)
	bindNode          = regexp.MustCompile(`^\s*bind((?:\s+` + kdlStringPattern + `)+)`)
	quotedArg         = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	defaultLayoutArg  = regexp.MustCompile(`^\s*default_layout\s+(` + kdlStringPattern + `)`)
	fileNameVersion   = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]*$`)
)

const (
	cogitatorStringPattern = `"(?:[^"\\]|\\.)*cogitator(?:[^"\\]|\\.)*"`
	cogitatorWasmPattern   = `"file:(?:[^"\\]|\\.)*cogitator(?:[^"\\]|\\.)*\.wasm"`
)

var (
	foreignRunAction    = regexp.MustCompile(`\bRun\s+` + cogitatorStringPattern + `\s+"(?:pick|next|agents)"`)
	foreignPluginAction = regexp.MustCompile(`\b(?:MessagePlugin|LaunchOrFocusPlugin|LaunchPlugin)\s+` + cogitatorWasmPattern)
	cogitatorWasm       = regexp.MustCompile(cogitatorWasmPattern)
)

// bindMode is the zellij input mode the binds live in and the mode each
// bind returns to after launching cogitator.
type bindMode struct {
	name     string
	returnTo string
}

// prefixMode suits tmux-style configs (default_mode "locked", a prefix key
// entering normal); tmuxMode is stock zellij's own prefix mode (Ctrl b),
// where binding plain keys in normal would hijack typing.
var (
	prefixMode = bindMode{name: "normal", returnTo: "locked"}
	tmuxMode   = bindMode{name: "tmux", returnTo: "normal"}
)

type bindSpec struct {
	binary     string
	nextKey    string
	pickKey    string
	plugin     string
	sidebarKey string
	layoutPath string
	mode       bindMode
}

// keyChoice is a key the user left at its default, with the flag that
// overrides it.
type keyChoice struct {
	flag string
	key  string
}

// Options are the user-facing flags shared by Install, Check and Uninstall.
// Force installs even when the user already wired cogitator by hand. Plugin
// is a wasm path to dock as-is; without it, Bundled is written to
// cogitator's data directory and that copy is docked.
type Options struct {
	NextKey    string
	PickKey    string
	Plugin     string
	Bundled    *BundledPlugin
	SidebarKey string
	ConfigPath string
	Binary     string
	DryRun     bool
	Force      bool
}

// BundledPlugin is the plugin wasm embedded in the cogitator binary.
// Version names the installed copy, so an upgrade docks a new file that
// zellij has never cached; "dev" or an unusable version falls back to a
// hash of the wasm.
type BundledPlugin struct {
	Wasm    []byte
	Version string
}

func (b BundledPlugin) fileName() string {
	version := b.Version
	if version == "dev" || !fileNameVersion.MatchString(version) {
		sum := sha256.Sum256(b.Wasm)
		version = hex.EncodeToString(sum[:6])
	}

	return wasmPrefix + version + wasmSuffix
}

func (o Options) hasPlugin() bool {
	return o.Plugin != "" || o.Bundled != nil
}

// Installer holds the environment seams so tests never read the real home,
// PATH, or zellij config. DataHome is $XDG_DATA_HOME.
type Installer struct {
	Home       string
	DataHome   string
	ConfigFile string
	ConfigDir  string
	LookPath   func(file string) (string, error)
	Executable func() (string, error)
	Out        io.Writer
}

// Report is how the config's cogitator wiring compares with what Install
// would write for the same options.
type Report struct {
	Keybinds installkit.Status
	Sidebar  installkit.Status
}

// target is what Install and Check derive from the options and the current
// config before deciding anything.
type target struct {
	requested  Options
	opts       Options
	confPath   string
	layoutsDir string
	existing   string
	mode       fs.FileMode
	stripped   string
	hadBlock   bool
	spec       bindSpec
	wasm       []byte
}

func (i Installer) load(opts Options) (target, error) {
	t := target{requested: opts, opts: opts}
	if err := normalizeKeys(&t.opts); err != nil {
		return target{}, err
	}

	plugin, err := i.pluginPath(t.opts)
	if err != nil {
		return target{}, err
	}
	if t.opts.Plugin == "" && t.opts.Bundled != nil {
		t.wasm = t.opts.Bundled.Wasm
	}

	binary, err := installkit.ResolveBinary(t.opts.Binary, i.LookPath, i.Executable)
	if err != nil {
		return target{}, err
	}

	t.confPath, err = installkit.ResolveSymlinks(i.configPath(opts.ConfigPath))
	if err != nil {
		return target{}, err
	}

	t.existing, t.mode, err = installkit.ReadConfig(t.confPath)
	if err != nil {
		return target{}, err
	}

	t.stripped, t.hadBlock, err = withoutOwnedBlock(t.existing)
	if err != nil {
		return target{}, fmt.Errorf("%s: %w", t.confPath, err)
	}

	layoutPath := i.layoutFile(opts.ConfigPath)
	t.layoutsDir = filepath.Dir(layoutPath)
	t.spec = bindSpec{binary: binary, nextKey: t.opts.NextKey, pickKey: t.opts.PickKey, plugin: plugin, sidebarKey: t.opts.SidebarKey}
	if plugin != "" {
		t.spec.layoutPath = layoutPath
	}

	return t, nil
}

// Check reports, without writing anything, whether the keybinds and the
// sidebar are missing, current, stale, or wired by hand outside cogitator's
// blocks.
func (i Installer) Check(opts Options) (Report, error) {
	t, err := i.load(opts)
	if err != nil {
		return Report{}, err
	}

	keybinds, err := t.keybindsStatus()
	if err != nil {
		return Report{}, fmt.Errorf("%s: %w", t.confPath, err)
	}

	return Report{Keybinds: keybinds, Sidebar: t.sidebarStatus()}, nil
}

func (t target) keybindsStatus() (installkit.Status, error) {
	if status, ok := foreignKeybinds(t.confPath, t.existing); ok {
		return status, nil
	}

	found, _ := ownedBlockTexts(t.existing)
	if found == "" {
		return installkit.Status{State: installkit.Missing, Path: t.confPath}, nil
	}

	spec := t.spec
	_, want, err := withKeybindsBlock(t.stripped, &spec)
	if err != nil {
		return installkit.Status{}, err
	}

	return managedStatus(t.confPath, found == want), nil
}

func (t target) sidebarStatus() installkit.Status {
	if t.spec.plugin == "" {
		return installkit.Status{State: installkit.Unavailable, Detail: "no plugin requested"}
	}

	if status, ok := foreignSidebar(t.confPath, t.existing, t.layoutsDir); ok {
		return status
	}

	_, found := ownedBlockTexts(t.existing)
	if found == "" {
		return installkit.Status{State: installkit.Missing, Path: t.confPath}
	}

	current := found == layoutBlock(t.spec.layoutPath) && fileHolds(t.spec.layoutPath, sidebarLayout(t.spec)) && t.wasmCurrent()

	return managedStatus(t.confPath, current)
}

func managedStatus(path string, current bool) installkit.Status {
	if current {
		return installkit.Status{State: installkit.Current, Path: path}
	}

	return installkit.Status{State: installkit.Stale, Path: path}
}

// refuseForeign stops an install that would duplicate cogitator wiring the
// user wrote by hand.
func (t target) refuseForeign() error {
	if status, ok := foreignKeybinds(t.confPath, t.existing); ok {
		return &installkit.ForeignError{Status: status}
	}

	if t.spec.plugin == "" {
		return nil
	}

	if status, ok := foreignSidebar(t.confPath, t.existing, t.layoutsDir); ok {
		return &installkit.ForeignError{Status: status}
	}

	return nil
}

// Install writes (or replaces) the owned blocks in the user's config.kdl and,
// with a plugin, the sidebar layout. zellij hot-reloads the file, so running
// sessions pick up the keybinds; the layout applies to sessions created
// afterwards.
func (i Installer) Install(opts Options) error {
	t, err := i.load(opts)
	if err != nil {
		return err
	}

	if !opts.Force {
		if err := t.refuseForeign(); err != nil {
			return err
		}
	}

	updated, block, err := withOwnedBlock(t.stripped, &t.spec)
	if err != nil {
		return fmt.Errorf("%s: %w", t.confPath, err)
	}

	if err := checkDefaultKeysFree(t.stripped, t.spec.mode.name, defaultedKeys(t.requested, t.opts)); err != nil {
		return fmt.Errorf("%s: %w", t.confPath, err)
	}

	layout := sidebarLayout(t.spec)
	if opts.DryRun {
		i.printDryRun(t, block, layout)
		return nil
	}

	if updated == t.existing && fileHolds(t.spec.layoutPath, layout) && t.wasmCurrent() {
		fmt.Fprintf(i.Out, "cogitator keybinds already set up in %s\n", t.confPath)
		return nil
	}

	removed, err := i.write(t, updated, layout)
	if err != nil {
		return err
	}
	fmt.Fprintln(i.Out, installSummary(t.confPath, t.hadBlock, t.spec))
	printPaths(i.Out, "removed the unused plugin copy ", removed)

	return nil
}

// write lands the plugin copy before the layout that docks it, and the
// layout before the config that selects it, then drops the plugin copies
// the layout no longer points at.
func (i Installer) write(t target, updated, layout string) ([]string, error) {
	if err := t.writeWasm(); err != nil {
		return nil, err
	}

	if err := writeLayout(t.spec.layoutPath, layout); err != nil {
		return nil, err
	}

	if err := installkit.WriteFileAtomic(t.confPath, []byte(updated), t.mode); err != nil {
		return nil, fmt.Errorf("write %s: %w", t.confPath, err)
	}

	if t.spec.layoutPath == "" {
		return nil, nil
	}

	return removeFiles(i.unusedWasm(t.spec.plugin))
}

func (i Installer) printDryRun(t target, block, layout string) {
	fmt.Fprintf(i.Out, "would write to %s:\n%s", t.confPath, block)
	if t.wasm != nil {
		fmt.Fprintf(i.Out, "would write the bundled plugin (%d bytes) to %s\n", len(t.wasm), t.spec.plugin)
	}
	if t.spec.layoutPath == "" {
		return
	}

	fmt.Fprintf(i.Out, "would write %s:\n%s", t.spec.layoutPath, layout)
	printPaths(i.Out, "would remove the unused plugin copy ", i.unusedWasm(t.spec.plugin))
}

// wasmCurrent reports whether the bundled plugin copy already holds this
// binary's wasm; an explicit --plugin is the user's file and always counts.
func (t target) wasmCurrent() bool {
	if t.wasm == nil {
		return true
	}

	existing, err := os.ReadFile(t.spec.plugin)

	return err == nil && bytes.Equal(existing, t.wasm)
}

func (t target) writeWasm() error {
	if t.wasmCurrent() {
		return nil
	}

	if err := installkit.WriteFileAtomic(t.spec.plugin, t.wasm, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", t.spec.plugin, err)
	}

	return nil
}

func installSummary(confPath string, hadBlock bool, spec bindSpec) string {
	verb := "added"
	if hadBlock {
		verb = "updated"
	}

	summary := fmt.Sprintf("%s cogitator keybinds in %s (mode: %s → returns to %s; next %s, pick %s", verb, confPath, spec.mode.name, spec.mode.returnTo, spec.nextKey, spec.pickKey)
	if spec.plugin == "" {
		return summary + ")"
	}

	return summary + fmt.Sprintf(", sidebar %s)\nnew zellij sessions open with the cogitator sidebar (%s, plugin %s); restart existing sessions to get it", spec.sidebarKey, spec.layoutPath, spec.plugin)
}

// Uninstall removes the owned blocks and the sidebar layout, leaving the rest
// of the file untouched.
func (i Installer) Uninstall(opts Options) error {
	confPath, err := installkit.ResolveSymlinks(i.configPath(opts.ConfigPath))
	if err != nil {
		return err
	}

	existing, mode, err := installkit.ReadConfig(confPath)
	if err != nil {
		return err
	}

	stripped, hadBlock, err := withoutOwnedBlock(existing)
	if err != nil {
		return fmt.Errorf("%s: %w", confPath, err)
	}

	if !hadBlock {
		fmt.Fprintf(i.Out, "no cogitator keybinds in %s\n", confPath)
		return nil
	}

	layoutPath := i.layoutFile(opts.ConfigPath)
	if opts.DryRun {
		fmt.Fprintf(i.Out, "would remove cogitator keybinds from %s%s\n", confPath, layoutNote(layoutPath, ownsLayout(layoutPath)))
		if ownsLayout(layoutPath) {
			printPaths(i.Out, "would remove the plugin copy ", i.unusedWasm(""))
		}
		return nil
	}

	if err := installkit.WriteFileAtomic(confPath, []byte(stripped), mode); err != nil {
		return fmt.Errorf("write %s: %w", confPath, err)
	}

	removed, err := removeLayout(layoutPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(i.Out, "removed cogitator keybinds from %s%s\n", confPath, layoutNote(layoutPath, removed))
	if !removed {
		return nil
	}

	copies, err := removeFiles(i.unusedWasm(""))
	printPaths(i.Out, "removed the plugin copy ", copies)

	return err
}

func layoutNote(layoutPath string, owned bool) string {
	if !owned {
		return ""
	}

	return " and the sidebar layout " + layoutPath
}

// layoutFile sits in zellij's default layout directory beside the config, so
// `zellij --layout cogitator` finds it too.
func (i Installer) layoutFile(explicitConfig string) string {
	return filepath.Join(filepath.Dir(i.configPath(explicitConfig)), "layouts", layoutName)
}

// dataDir is cogitator's XDG data directory, where the bundled plugin copy
// lives.
func (i Installer) dataDir() string {
	if i.DataHome != "" {
		return filepath.Join(i.DataHome, "cogitator")
	}

	return filepath.Join(i.Home, ".local", "share", "cogitator")
}

// pluginPath is the wasm the sidebar docks: the explicit --plugin, else
// where the bundled copy is (or will be) written, else "" for no sidebar.
func (i Installer) pluginPath(opts Options) (string, error) {
	if opts.Plugin != "" || opts.Bundled == nil {
		return resolvePlugin(opts.Plugin)
	}

	if len(opts.Bundled.Wasm) == 0 {
		return "", errors.New("bundled plugin is empty")
	}

	return filepath.Join(i.dataDir(), opts.Bundled.fileName()), nil
}

// unusedWasm lists the plugin copies cogitator wrote to its data directory
// other than keep, which the layout docks; keep "" lists them all.
func (i Installer) unusedWasm(keep string) []string {
	paths, _ := filepath.Glob(filepath.Join(i.dataDir(), wasmPrefix+"*"+wasmSuffix))
	kept, keepErr := os.Stat(keep)

	var unused []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil && keepErr == nil && os.SameFile(info, kept) {
			continue
		}
		unused = append(unused, path)
	}

	return unused
}

func removeFiles(paths []string) ([]string, error) {
	var removed []string
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("remove %s: %w", path, err)
		}
		removed = append(removed, path)
	}

	return removed, nil
}

func printPaths(out io.Writer, prefix string, paths []string) {
	for _, path := range paths {
		fmt.Fprintln(out, prefix+path)
	}
}

// sidebarLayout mirrors zellij's default layout (tab bar, one pane, status
// bar) with the plugin docked on the left. A layout without tabs is also the
// template for every new tab, so `new-tab` gets the sidebar too. The plugin
// configuration must match the sidebar keybind's exactly, since zellij routes
// the keybind's message only to instances with identical configuration.
func sidebarLayout(spec bindSpec) string {
	if spec.layoutPath == "" {
		return ""
	}

	return layoutHeader + "\n" +
		"layout {\n" +
		"    pane size=1 borderless=true {\n" +
		"        plugin location=\"zellij:tab-bar\"\n" +
		"    }\n" +
		"    pane split_direction=\"vertical\" {\n" +
		"        pane size=" + strconv.Itoa(sidebarWidth) + " {\n" +
		"            plugin location=" + kdlQuote("file:"+spec.plugin) + " {\n" +
		"                cogitator " + kdlQuote(spec.binary) + "\n" +
		"            }\n" +
		"        }\n" +
		"        pane focus=true\n" +
		"    }\n" +
		"    pane size=2 borderless=true {\n" +
		"        plugin location=\"zellij:status-bar\"\n" +
		"    }\n" +
		"}\n"
}

func fileHolds(path, content string) bool {
	if path == "" {
		return true
	}

	existing, err := os.ReadFile(path)

	return err == nil && string(existing) == content
}

func writeLayout(path, layout string) error {
	if path == "" {
		return nil
	}

	if err := installkit.WriteFileAtomic(path, []byte(layout), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func ownsLayout(path string) bool {
	content, err := os.ReadFile(path)

	return err == nil && strings.HasPrefix(string(content), layoutOwner)
}

// removeLayout deletes the sidebar layout only when cogitator wrote it, so a
// user's own layout of the same name survives.
func removeLayout(path string) (bool, error) {
	if !ownsLayout(path) {
		return false, nil
	}

	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("remove %s: %w", path, err)
	}

	return true, nil
}

// configPath follows zellij's own lookup order.
func (i Installer) configPath(explicit string) string {
	switch {
	case explicit != "":
		return explicit
	case i.ConfigFile != "":
		return i.ConfigFile
	case i.ConfigDir != "":
		return filepath.Join(i.ConfigDir, "config.kdl")
	default:
		return filepath.Join(i.Home, ".config", "zellij", "config.kdl")
	}
}

// normalizeKeys fills in default keys and validates them; the sidebar key
// only matters when a plugin is bound.
func normalizeKeys(opts *Options) error {
	if opts.NextKey == "" {
		opts.NextKey = DefaultNextKey
	}
	if opts.PickKey == "" {
		opts.PickKey = DefaultPickKey
	}

	keys := []string{opts.NextKey, opts.PickKey}
	if opts.hasPlugin() {
		if opts.SidebarKey == "" {
			opts.SidebarKey = DefaultSidebarKey
		}
		keys = append(keys, opts.SidebarKey)
	}

	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return err
		}
	}

	if opts.hasPlugin() && (opts.SidebarKey == opts.NextKey || opts.SidebarKey == opts.PickKey) {
		return fmt.Errorf("sidebar key %q collides with the next/pick keys", opts.SidebarKey)
	}

	return nil
}

// defaultedKeys lists the keys the user did not pass explicitly; only those
// are checked against existing binds, since an explicit key is a choice.
func defaultedKeys(requested, opts Options) []keyChoice {
	var keys []keyChoice
	if requested.NextKey == "" {
		keys = append(keys, keyChoice{flag: "--next-key", key: opts.NextKey})
	}
	if requested.PickKey == "" {
		keys = append(keys, keyChoice{flag: "--pick-key", key: opts.PickKey})
	}
	if opts.hasPlugin() && requested.SidebarKey == "" {
		keys = append(keys, keyChoice{flag: "--sidebar-key", key: opts.SidebarKey})
	}

	return keys
}

// checkDefaultKeysFree refuses a defaulted key that the config already binds
// in mode, so the install never silently shadows (or is shadowed by) a
// user bind.
func checkDefaultKeysFree(content, mode string, keys []keyChoice) error {
	if len(keys) == 0 {
		return nil
	}

	bound := boundKeys(content, mode)
	for _, choice := range keys {
		if bound[canonicalKey(choice.key)] {
			return fmt.Errorf("default key %q (%s) is already bound in zellij's %s mode; pass %s with a free key, or %s %q to override that bind", choice.key, choice.flag, mode, choice.flag, choice.flag, choice.key)
		}
	}

	return nil
}

// boundKeys collects the keys the first top-level keybinds node binds in
// mode, counting shared, shared_among and shared_except sections that
// include it. zellij ignores later keybinds nodes.
func boundKeys(content, mode string) map[string]bool {
	bound := map[string]bool{}
	lines := strings.SplitAfter(content, "\n")

	open, _, err := keybindsOpeningLine(lines)
	if err != nil || open < 0 {
		return bound
	}

	depth := 0
	inComment := false
	applies := false
	for _, line := range lines[open:] {
		if !inComment {
			switch {
			case depth == 1:
				applies = sectionApplies(line, mode)
			case depth == 2 && applies:
				recordBinds(bound, line)
			}
		}

		var delta int
		delta, inComment = braceDelta(line, inComment)
		depth += delta
		if depth <= 0 {
			break
		}
	}

	return bound
}

func sectionApplies(line, mode string) bool {
	match := sectionNode.FindStringSubmatch(line)
	if match == nil {
		return false
	}

	positionalArgs := kdlProperty.ReplaceAllString(match[2], "")
	listed := false
	for _, name := range quotedArgs(positionalArgs) {
		if strings.EqualFold(name, mode) {
			listed = true
		}
	}

	switch match[1] {
	case "shared":
		return true
	case "shared_among":
		return listed
	case "shared_except":
		return !listed
	default:
		return strings.EqualFold(match[1], mode)
	}
}

func recordBinds(bound map[string]bool, line string) {
	match := bindNode.FindStringSubmatch(line)
	if match == nil {
		return
	}

	for _, key := range quotedArgs(match[1]) {
		bound[canonicalKey(key)] = true
	}
}

func quotedArgs(s string) []string {
	var args []string
	for _, match := range quotedArg.FindAllStringSubmatch(s, -1) {
		arg, err := strconv.Unquote(`"` + match[1] + `"`)
		if err != nil {
			arg = match[1]
		}
		args = append(args, arg)
	}

	return args
}

// canonicalKey makes modifier and named-key spellings comparable ("Ctrl a"
// and "ctrl a", "Esc" and "esc") while keeping single characters
// case-sensitive, since "H" and "h" are different keys.
func canonicalKey(key string) string {
	parts := strings.Fields(key)
	for idx, part := range parts {
		if len([]rune(part)) > 1 {
			parts[idx] = strings.ToLower(part)
		}
	}

	return strings.Join(parts, " ")
}

// resolvePlugin returns the absolute, symlink-resolved wasm path, or "" when
// no plugin was requested.
func resolvePlugin(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("plugin %s: %w", path, err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("plugin %s: %w", path, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("plugin %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("plugin %s is not a regular file", path)
	}

	return resolved, nil
}

func validateKey(key string) error {
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\t\r\n\"\\{};") {
		return fmt.Errorf("invalid zellij key %q", key)
	}

	return nil
}

// withoutOwnedBlock drops every marker-delimited block, reporting whether
// any was present.
func withoutOwnedBlock(content string) (string, bool, error) {
	lines := strings.SplitAfter(content, "\n")
	kept := make([]string, 0, len(lines))
	inBlock, found := false, false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case !inBlock && strings.HasPrefix(trimmed, beginMarker):
			inBlock, found = true, true
		case inBlock && trimmed == endMarker:
			inBlock = false
		case !inBlock:
			kept = append(kept, line)
		}
	}

	if inBlock {
		return "", false, errors.New("found the cogitator BEGIN marker without its END marker; fix the file by hand")
	}
	if !found {
		return content, false, nil
	}

	return strings.Join(kept, ""), true, nil
}

// withOwnedBlock adds the keybinds block and, with a plugin, a top-level
// block pointing default_layout at the sidebar layout. It sets spec.mode
// from the config and returns the new content and the inserted text.
func withOwnedBlock(content string, spec *bindSpec) (string, string, error) {
	updated, block, err := withKeybindsBlock(content, spec)
	if err != nil || spec.layoutPath == "" {
		return updated, block, err
	}

	if line := userDefaultLayoutLine(content); line > 0 {
		return "", "", fmt.Errorf("line %d: default_layout is already set; remove it so cogitator can set its sidebar layout, or install with --no-sidebar", line)
	}

	layout := layoutBlock(spec.layoutPath)
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}

	return updated + layout, block + layout, nil
}

func layoutBlock(layoutPath string) string {
	return beginLine + "\n" +
		"default_layout " + kdlQuote(layoutPath) + "\n" +
		endMarker + "\n"
}

// ownedBlockTexts returns the first owned keybinds block and the first owned
// default_layout block, each "" when absent. The layout block is the one
// holding a default_layout line.
func ownedBlockTexts(content string) (string, string) {
	keybinds, layout := "", ""
	for _, block := range ownedBlocks(content) {
		switch {
		case hasDefaultLayoutLine(block):
			if layout == "" {
				layout = block
			}
		case keybinds == "":
			keybinds = block
		}
	}

	return keybinds, layout
}

func hasDefaultLayoutLine(block string) bool {
	for _, line := range strings.SplitAfter(block, "\n") {
		if defaultLayoutNode.MatchString(line) {
			return true
		}
	}

	return false
}

// ownedBlocks returns the full text of each marker-delimited block,
// markers included.
func ownedBlocks(content string) []string {
	var blocks []string
	var block strings.Builder
	inBlock := false

	for _, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inBlock && !strings.HasPrefix(trimmed, beginMarker) {
			continue
		}

		inBlock = true
		block.WriteString(line)
		if trimmed == endMarker {
			blocks = append(blocks, block.String())
			block.Reset()
			inBlock = false
		}
	}

	return blocks
}

// foreignKeybinds finds a line outside cogitator's blocks and comments that
// runs cogitator or messages its plugin.
func foreignKeybinds(confPath, content string) (installkit.Status, bool) {
	inBlock, inComment := false, false

	for idx, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimSpace(line)
		commented := inComment || strings.HasPrefix(trimmed, "//")
		_, inComment = braceDelta(line, inComment)

		switch {
		case !inBlock && strings.HasPrefix(trimmed, beginMarker):
			inBlock = true
		case inBlock:
			inBlock = trimmed != endMarker
		case commented:
		case foreignRunAction.MatchString(line) || foreignPluginAction.MatchString(line):
			return installkit.Status{State: installkit.Foreign, Path: confPath, Line: idx + 1, Detail: trimmed}, true
		}
	}

	return installkit.Status{}, false
}

// foreignSidebar finds a top-level default_layout outside cogitator's blocks
// whose layout file loads a cogitator plugin.
func foreignSidebar(confPath, content, layoutsDir string) (installkit.Status, bool) {
	var found installkit.Status
	inBlock := false

	eachTopLevelLine(strings.SplitAfter(content, "\n"), func(idx int, line string) bool {
		trimmed := strings.TrimSpace(line)
		switch {
		case !inBlock && strings.HasPrefix(trimmed, beginMarker):
			inBlock = true
		case inBlock:
			inBlock = trimmed != endMarker
		default:
			if file, ok := cogitatorLayout(line, layoutsDir); ok {
				detail := fmt.Sprintf("%s loads the cogitator plugin from %s", trimmed, file)
				found = installkit.Status{State: installkit.Foreign, Path: confPath, Line: idx + 1, Detail: detail}
				return false
			}
		}
		return true
	})

	return found, found.State == installkit.Foreign
}

// cogitatorLayout resolves a default_layout line's value the way zellij does
// and reports whether that layout file loads a cogitator plugin.
func cogitatorLayout(line, layoutsDir string) (string, bool) {
	match := defaultLayoutArg.FindStringSubmatch(line)
	if match == nil {
		return "", false
	}

	file := quotedArgs(match[1])[0]
	if !filepath.IsAbs(file) {
		if filepath.Ext(file) == "" {
			file += ".kdl"
		}
		file = filepath.Join(layoutsDir, file)
	}

	content, err := os.ReadFile(file)

	return file, err == nil && cogitatorWasm.Match(content)
}

// withKeybindsBlock inserts the block right after the opening line of the
// first top-level keybinds node, or appends a whole keybinds node when there
// is none.
func withKeybindsBlock(content string, spec *bindSpec) (string, string, error) {
	lines := strings.SplitAfter(content, "\n")
	spec.mode = chooseBindMode(lines)

	open, indent, err := keybindsOpeningLine(lines)
	if err != nil {
		return "", "", err
	}

	if open < 0 {
		block := appendedNode(*spec)
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + block, block, nil
	}

	block := ownedBlock(indent+indentUnit, *spec)
	if !strings.HasSuffix(lines[open], "\n") {
		lines[open] += "\n"
	}
	head := strings.Join(lines[:open+1], "")
	tail := strings.Join(lines[open+1:], "")

	return head + block + tail, block, nil
}

// userDefaultLayoutLine is the 1-based line of a top-level default_layout
// outside cogitator's blocks, or 0.
func userDefaultLayoutLine(content string) int {
	found := 0
	eachTopLevelLine(strings.SplitAfter(content, "\n"), func(idx int, line string) bool {
		if defaultLayoutNode.MatchString(line) {
			found = idx + 1
			return false
		}
		return true
	})

	return found
}

// chooseBindMode uses the normal-mode prefix table only when the config's
// top-level default_mode is "locked"; the last such node wins, as in zellij.
func chooseBindMode(lines []string) bindMode {
	mode := tmuxMode

	eachTopLevelLine(lines, func(_ int, line string) bool {
		if match := defaultModeNode.FindStringSubmatch(line); match != nil {
			mode = tmuxMode
			if match[1] == "locked" {
				mode = prefixMode
			}
		}
		return true
	})

	return mode
}

// keybindsOpeningLine finds the first keybinds node at brace depth zero. It
// requires the node's `{` to end that line so the block can follow it.
func keybindsOpeningLine(lines []string) (int, string, error) {
	open, indent := -1, ""
	var err error

	eachTopLevelLine(lines, func(idx int, line string) bool {
		match := keybindsNode.FindStringSubmatch(line)
		if match == nil {
			return true
		}

		if !strings.HasSuffix(stripLineComment(line), "{") {
			err = fmt.Errorf("line %d: keybinds node must end its line with `{` for cogitator to insert its block", idx+1)
		} else {
			open, indent = idx, match[1]
		}
		return false
	})

	return open, indent, err
}

// eachTopLevelLine calls visit for every line that starts at brace depth zero
// outside a block comment, until visit returns false.
func eachTopLevelLine(lines []string, visit func(idx int, line string) bool) {
	depth := 0
	inComment := false

	for idx, line := range lines {
		if depth == 0 && !inComment && !visit(idx, line) {
			return
		}

		var delta int
		delta, inComment = braceDelta(line, inComment)
		depth += delta
	}
}

func stripLineComment(line string) string {
	code := line
	if idx := strings.Index(code, "//"); idx >= 0 {
		code = code[:idx]
	}

	return strings.TrimSpace(code)
}

// braceDelta counts KDL braces on one line, skipping strings, line comments
// and block comments; inComment carries a block comment across lines.
func braceDelta(line string, inComment bool) (int, bool) {
	delta := 0
	inString := false

	for pos := 0; pos < len(line); pos++ {
		rest := line[pos:]

		switch {
		case inComment:
			if strings.HasPrefix(rest, "*/") {
				inComment = false
				pos++
			}
		case inString:
			if rest[0] == '\\' {
				pos++
			} else if rest[0] == '"' {
				inString = false
			}
		case rest[0] == '"':
			inString = true
		case strings.HasPrefix(rest, "//"):
			return delta, false
		case strings.HasPrefix(rest, "/*"):
			inComment = true
			pos++
		case rest[0] == '{':
			delta++
		case rest[0] == '}':
			delta--
		}
	}

	return delta, inComment
}

func appendedNode(spec bindSpec) string {
	return beginLine + "\n" +
		"keybinds {\n" +
		bindings(indentUnit, spec) +
		"}\n" +
		endMarker + "\n"
}

func ownedBlock(indent string, spec bindSpec) string {
	return indent + beginLine + "\n" +
		bindings(indent, spec) +
		indent + endMarker + "\n"
}

func bindings(indent string, spec bindSpec) string {
	bin := kdlQuote(spec.binary)
	inner := indent + indentUnit
	back := "SwitchToMode " + kdlQuote(spec.mode.returnTo) + "; }"

	sidebar := ""
	if spec.plugin != "" {
		sidebar = inner + "bind " + kdlQuote(spec.sidebarKey) + " { MessagePlugin " + kdlQuote("file:"+spec.plugin) + " { name " + kdlQuote(focusPipe) + "; floating true; cogitator " + bin + "; }; " + back + "\n"
	}

	return indent + "shared_among " + kdlQuote(spec.mode.name) + " {\n" +
		inner + "bind " + kdlQuote(spec.nextKey) + " { Run " + bin + ` "next" { floating true; close_on_exit true; }; ` + back + "\n" +
		inner + "bind " + kdlQuote(spec.pickKey) + " { Run " + bin + ` "pick" { floating true; close_on_exit true; width "70%"; height "60%"; x "15%"; y "20%"; }; ` + back + "\n" +
		sidebar +
		indent + "}\n"
}

func kdlQuote(s string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)

	return `"` + escaped + `"`
}
