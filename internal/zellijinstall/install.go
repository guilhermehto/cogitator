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
	"errors"
	"fmt"
	"io"
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
	layoutHeader = "// managed by cogitator — regenerate with `cogitator zellij install --plugin`"
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

// Options are the user-facing flags shared by Install and Uninstall.
type Options struct {
	NextKey    string
	PickKey    string
	Plugin     string
	SidebarKey string
	ConfigPath string
	Binary     string
	DryRun     bool
}

// Installer holds the environment seams so tests never read the real home,
// PATH, or zellij config.
type Installer struct {
	Home       string
	ConfigFile string
	ConfigDir  string
	LookPath   func(file string) (string, error)
	Executable func() (string, error)
	Out        io.Writer
}

// Install writes (or replaces) the owned blocks in the user's config.kdl and,
// with a plugin, the sidebar layout. zellij hot-reloads the file, so running
// sessions pick up the keybinds; the layout applies to sessions created
// afterwards.
func (i Installer) Install(opts Options) error {
	requested := opts
	if err := normalizeKeys(&opts); err != nil {
		return err
	}

	plugin, err := resolvePlugin(opts.Plugin)
	if err != nil {
		return err
	}

	binary, err := installkit.ResolveBinary(opts.Binary, i.LookPath, i.Executable)
	if err != nil {
		return err
	}

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

	spec := bindSpec{binary: binary, nextKey: opts.NextKey, pickKey: opts.PickKey, plugin: plugin, sidebarKey: opts.SidebarKey}
	if plugin != "" {
		spec.layoutPath = i.layoutFile(opts.ConfigPath)
	}
	updated, block, err := withOwnedBlock(stripped, &spec)
	if err != nil {
		return fmt.Errorf("%s: %w", confPath, err)
	}

	if err := checkDefaultKeysFree(stripped, spec.mode.name, defaultedKeys(requested, opts)); err != nil {
		return fmt.Errorf("%s: %w", confPath, err)
	}

	layout := sidebarLayout(spec)
	if opts.DryRun {
		i.printDryRun(confPath, block, spec.layoutPath, layout)
		return nil
	}

	if updated == existing && fileHolds(spec.layoutPath, layout) {
		fmt.Fprintf(i.Out, "%s already has the cogitator keybinds\n", confPath)
		return nil
	}

	if err := writeLayout(spec.layoutPath, layout); err != nil {
		return err
	}

	if err := installkit.WriteFileAtomic(confPath, []byte(updated), mode); err != nil {
		return fmt.Errorf("write %s: %w", confPath, err)
	}
	fmt.Fprintln(i.Out, installSummary(confPath, hadBlock, spec))

	return nil
}

func (i Installer) printDryRun(confPath, block, layoutPath, layout string) {
	fmt.Fprintf(i.Out, "would write to %s:\n%s", confPath, block)
	if layoutPath != "" {
		fmt.Fprintf(i.Out, "would write %s:\n%s", layoutPath, layout)
	}
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

	return summary + fmt.Sprintf(", sidebar %s)\nnew zellij sessions open with the cogitator sidebar (%s); restart existing sessions to get it", spec.sidebarKey, spec.layoutPath)
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

	return nil
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

	return err == nil && strings.HasPrefix(string(content), layoutHeader)
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
	if opts.Plugin != "" {
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

	if opts.Plugin != "" && (opts.SidebarKey == opts.NextKey || opts.SidebarKey == opts.PickKey) {
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
	if opts.Plugin != "" && requested.SidebarKey == "" {
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
		return "", "", fmt.Errorf("line %d: default_layout is already set; remove it so cogitator can set its sidebar layout, or install without --plugin", line)
	}

	layoutBlock := beginLine + "\n" +
		"default_layout " + kdlQuote(spec.layoutPath) + "\n" +
		endMarker + "\n"
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}

	return updated + layoutBlock, block + layoutBlock, nil
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
