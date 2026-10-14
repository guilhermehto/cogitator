package installkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// HookEvent is one harness lifecycle event cogitator's hook runs on. An empty
// Matcher omits the matcher key from the group install adds.
type HookEvent struct {
	Name    string
	Matcher string
}

// HookSpec describes cogitator's wiring in a harness hook file: a JSON object
// whose "hooks" key maps event names to [{matcher?, hooks: [{type, command}]}].
// Home expands ~ and $HOME when a hook command runs a wrapper script.
type HookSpec struct {
	Path       string
	Subcommand string
	Events     []HookEvent
	Home       string
}

// HookCommand is the command install writes: the binary, shell-quoted when it
// is not a plain word, followed by the hook subcommand.
func HookCommand(binary, subcommand string) string {
	if !plainShellWord.MatchString(binary) {
		binary = "'" + strings.ReplaceAll(binary, "'", `'\''`) + "'"
	}

	return binary + " " + subcommand
}

// CheckHooks reports how the hook file compares with what InstallHooks would
// write for binary. Foreign covers a hook that runs the subcommand in another
// form (inline shell or a wrapper script); Stale covers `<other binary>
// <subcommand>` entries and events not yet wired. It never writes.
func CheckHooks(spec HookSpec, binary string) (Status, error) {
	content, _, err := ReadConfig(spec.Path)
	if err != nil {
		return Status{}, err
	}

	doc, err := parseHookDoc(content)
	if err != nil {
		return Status{}, fmt.Errorf("%s: %w", spec.Path, err)
	}

	status, _, err := spec.classify(doc, content, HookCommand(binary, spec.Subcommand))
	if err != nil {
		return Status{}, fmt.Errorf("%s: %w", spec.Path, err)
	}

	return status, nil
}

// InstallHooks merges cogitator's hook into every event of spec, keeping
// unrelated keys, their order, and other hooks. Stale and (with force)
// Foreign entries are rewritten in place, never duplicated. It returns the
// status found before writing: Current writes nothing, Foreign without force
// returns *ForeignError, and invalid JSON is an error with the file untouched.
func InstallHooks(spec HookSpec, binary string, force bool) (Status, error) {
	target, err := ResolveSymlinks(spec.Path)
	if err != nil {
		return Status{}, err
	}

	content, mode, err := ReadConfig(target)
	if err != nil {
		return Status{}, err
	}

	doc, err := parseHookDoc(content)
	if err != nil {
		return Status{}, fmt.Errorf("%s: %w", spec.Path, err)
	}

	command := HookCommand(binary, spec.Subcommand)
	status, events, err := spec.classify(doc, content, command)
	if err != nil {
		return Status{}, fmt.Errorf("%s: %w", spec.Path, err)
	}

	if status.State == Foreign && !force {
		return status, &ForeignError{Status: status}
	}

	if status.State == Current {
		return status, nil
	}

	merged, err := doc.wire(spec.Events, events, command, detectIndent(content))
	if err != nil {
		return status, fmt.Errorf("%s: %w", spec.Path, err)
	}

	if err := WriteFileAtomic(target, merged, mode); err != nil {
		return status, fmt.Errorf("write %s: %w", spec.Path, err)
	}

	return status, nil
}

var (
	plainShellWord = regexp.MustCompile(`^[A-Za-z0-9_./+@%:,=-]+$`)
	shellWord      = regexp.MustCompile(`^(?:[^\s'"]+|'(?:[^']|'\\'')*')$`)
	errHookShape   = errors.New("unexpected hooks layout")
)

type hookKind int

const (
	otherHook hookKind = iota
	currentHook
	staleHook
	foreignHook
)

// hookRef locates one cogitator hook inside an event's groups.
type hookRef struct {
	group, hook int
	kind        hookKind
	command     string
}

// eventHooks is one event's matcher groups with each group's hooks parsed.
type eventHooks struct {
	groups []jsonObject
	hooks  [][]jsonObject
}

type hookDoc struct {
	top   jsonObject
	hooks jsonObject
}

func parseHookDoc(content string) (hookDoc, error) {
	var doc hookDoc
	if strings.TrimSpace(content) == "" {
		return doc, nil
	}

	if err := json.Unmarshal([]byte(content), &doc.top); err != nil {
		return doc, fmt.Errorf("invalid JSON: %w", err)
	}

	raw, ok := doc.top.get("hooks")
	if !ok {
		return doc, nil
	}

	if err := json.Unmarshal(raw, &doc.hooks); err != nil {
		return doc, fmt.Errorf("%w: \"hooks\" is not an object", errHookShape)
	}

	return doc, nil
}

func (d hookDoc) event(name string) (eventHooks, error) {
	var parsed eventHooks
	raw, ok := d.hooks.get(name)
	if !ok || string(raw) == "null" {
		return parsed, nil
	}

	if err := json.Unmarshal(raw, &parsed.groups); err != nil {
		return parsed, fmt.Errorf("%w: hooks.%s is not a list of groups", errHookShape, name)
	}

	parsed.hooks = make([][]jsonObject, len(parsed.groups))
	for i, group := range parsed.groups {
		raw, ok := group.get("hooks")
		if !ok || string(raw) == "null" {
			continue
		}

		if err := json.Unmarshal(raw, &parsed.hooks[i]); err != nil {
			return parsed, fmt.Errorf("%w: hooks.%s[%d].hooks is not a list of hooks", errHookShape, name, i)
		}
	}

	return parsed, nil
}

// classify returns the file's status and each spec event's cogitator hooks.
func (s HookSpec) classify(doc hookDoc, content, command string) (Status, map[string][]hookRef, error) {
	refs := make(map[string][]hookRef, len(s.Events))
	status := Status{Path: s.Path}
	var foreign []string
	current, missing := 0, 0

	for _, event := range s.Events {
		parsed, err := doc.event(event.Name)
		if err != nil {
			return Status{}, nil, err
		}

		found := s.cogitatorHooks(parsed, command)
		refs[event.Name] = found

		switch eventState(found) {
		case Foreign:
			foreign = append(foreign, event.Name)
			if len(foreign) == 1 {
				status.Line = lineOf(content, foreignCommand(found))
				status.Detail = fmt.Sprintf("%s hook %q runs %s", event.Name, foreignCommand(found), s.Subcommand)
			}
		case Current:
			current++
		case Missing:
			missing++
		}
	}

	switch {
	case len(foreign) > 0:
		status.State = Foreign
		if len(foreign) > 1 {
			status.Detail += fmt.Sprintf(", and %d more events", len(foreign)-1)
		}
	case current == len(s.Events):
		status.State = Current
	case missing == len(s.Events):
		status.State = Missing
	default:
		status.State = Stale
	}

	return status, refs, nil
}

func eventState(refs []hookRef) State {
	if len(refs) == 0 {
		return Missing
	}

	for _, ref := range refs {
		if ref.kind == foreignHook {
			return Foreign
		}
	}

	if len(refs) == 1 && refs[0].kind == currentHook {
		return Current
	}

	return Stale
}

func foreignCommand(refs []hookRef) string {
	for _, ref := range refs {
		if ref.kind == foreignHook {
			return ref.command
		}
	}

	return ""
}

func (s HookSpec) cogitatorHooks(event eventHooks, command string) []hookRef {
	var refs []hookRef
	for g, hooks := range event.hooks {
		for h, hook := range hooks {
			cmd, ok := hook.stringValue("command")
			if !ok {
				continue
			}

			if kind := s.kindOf(cmd, command); kind != otherHook {
				refs = append(refs, hookRef{group: g, hook: h, kind: kind, command: cmd})
			}
		}
	}

	return refs
}

func (s HookSpec) kindOf(cmd, command string) hookKind {
	if cmd == command {
		return currentHook
	}

	if prefix, ok := strings.CutSuffix(cmd, " "+s.Subcommand); ok && shellWord.MatchString(prefix) {
		return staleHook
	}

	if mentions(cmd, s.Subcommand) || s.wrapperCalls(cmd) {
		return foreignHook
	}

	return otherHook
}

// mentions reports whether text names subcommand as a word of its own.
func mentions(text, subcommand string) bool {
	for start := 0; ; {
		i := strings.Index(text[start:], subcommand)
		if i < 0 {
			return false
		}

		i += start
		start = i + len(subcommand)
		if !isWordByte(text, i-1) && !isWordByte(text, start) {
			return true
		}
	}
}

func isWordByte(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return false
	}

	c := text[i]

	return c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// maxWrapperSize bounds the files read as possible wrapper scripts so a hook
// that runs a large binary is never slurped.
const maxWrapperSize = 64 << 10

// wrapperCalls reports whether cmd runs a script file with a line that calls
// cogitator's subcommand.
func (s HookSpec) wrapperCalls(cmd string) bool {
	for _, field := range strings.Fields(cmd) {
		path := s.expandHome(strings.Trim(field, `'";&|()`))
		if !filepath.IsAbs(path) {
			continue
		}

		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxWrapperSize {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue
		}

		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "cogitator") && mentions(line, s.Subcommand) {
				return true
			}
		}
	}

	return false
}

func (s HookSpec) expandHome(path string) string {
	if s.Home == "" {
		return path
	}

	for _, prefix := range []string{"~/", "$HOME/", "${HOME}/"} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			return filepath.Join(s.Home, rest)
		}
	}

	return path
}

// lineOf is the 1-based line holding command as a JSON string, or 0.
func lineOf(content, command string) int {
	quoted, err := encodeJSON(command)
	if err != nil {
		return 0
	}

	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, string(quoted)) {
			return i + 1
		}
	}

	return 0
}

// wire rewrites every spec event that is not Current so it holds exactly one
// cogitator hook running command, and renders the file.
func (d hookDoc) wire(events []HookEvent, refs map[string][]hookRef, command, indent string) ([]byte, error) {
	for _, event := range events {
		found := refs[event.Name]
		if eventState(found) == Current {
			continue
		}

		parsed, err := d.event(event.Name)
		if err != nil {
			return nil, err
		}

		parsed.wire(event, found, command)

		raw, err := parsed.encode()
		if err != nil {
			return nil, err
		}
		d.hooks.set(event.Name, raw)
	}

	raw, err := encodeJSON(d.hooks)
	if err != nil {
		return nil, err
	}
	d.top.set("hooks", raw)

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(d.top); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// wire points the first cogitator hook at command and drops the rest, or adds
// a new group when the event has none.
func (e *eventHooks) wire(event HookEvent, refs []hookRef, command string) {
	if len(refs) == 0 {
		var group jsonObject
		if event.Matcher != "" {
			group.set("matcher", mustEncodeJSON(event.Matcher))
		}

		var hook jsonObject
		hook.set("type", mustEncodeJSON("command"))
		hook.set("command", mustEncodeJSON(command))
		e.groups = append(e.groups, group)
		e.hooks = append(e.hooks, []jsonObject{hook})

		return
	}

	first := refs[0]
	e.hooks[first.group][first.hook].set("command", mustEncodeJSON(command))

	for i := len(refs) - 1; i > 0; i-- {
		g, h := refs[i].group, refs[i].hook
		e.hooks[g] = append(e.hooks[g][:h], e.hooks[g][h+1:]...)
		if len(e.hooks[g]) == 0 {
			e.groups = append(e.groups[:g], e.groups[g+1:]...)
			e.hooks = append(e.hooks[:g], e.hooks[g+1:]...)
		}
	}
}

func (e eventHooks) encode() (json.RawMessage, error) {
	for i := range e.groups {
		if e.hooks[i] == nil {
			continue
		}

		raw, err := encodeJSON(e.hooks[i])
		if err != nil {
			return nil, err
		}
		e.groups[i].set("hooks", raw)
	}

	return encodeJSON(e.groups)
}

// detectIndent reuses the file's indentation unit, defaulting to two spaces.
func detectIndent(content string) string {
	for _, line := range strings.Split(content, "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}

	return "  "
}

// jsonObject is a JSON object that keeps its keys in file order.
type jsonObject []jsonMember

type jsonMember struct {
	key   string
	value json.RawMessage
}

func (o jsonObject) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.key == key {
			return m.value, true
		}
	}

	return nil, false
}

func (o jsonObject) stringValue(key string) (string, bool) {
	raw, ok := o.get(key)
	if !ok {
		return "", false
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}

	return s, true
}

func (o *jsonObject) set(key string, value json.RawMessage) {
	for i := range *o {
		if (*o)[i].key == key {
			(*o)[i].value = value
			return
		}
	}

	*o = append(*o, jsonMember{key: key, value: value})
}

func (o *jsonObject) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}

	if tok == nil {
		*o = nil
		return nil
	}

	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("want an object, got %v", tok)
	}

	members := jsonObject{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}

		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		members = append(members, jsonMember{key: tok.(string), value: value})
	}

	*o = members

	return nil
}

func (o jsonObject) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			out.WriteByte(',')
		}

		key, err := encodeJSON(m.key)
		if err != nil {
			return nil, err
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(m.value)
	}
	out.WriteByte('}')

	return out.Bytes(), nil
}

func encodeJSON(v any) (json.RawMessage, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func mustEncodeJSON(s string) json.RawMessage {
	raw, err := encodeJSON(s)
	if err != nil {
		panic(err)
	}

	return raw
}
