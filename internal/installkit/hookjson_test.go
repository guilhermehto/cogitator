package installkit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testHookEvents = []HookEvent{
	{Name: "SessionStart"},
	{Name: "PreToolUse", Matcher: "*"},
	{Name: "Stop"},
}

func testHookSpec(t *testing.T, content string) HookSpec {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if content != "" {
		writeHookFile(t, path, content)
	}

	return HookSpec{Path: path, Subcommand: "claude-hook", Events: testHookEvents, Home: dir}
}

func writeHookFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readHookFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// commandsByEvent decodes the file's hooks into event -> commands in order.
func commandsByEvent(t *testing.T, content string) map[string][]string {
	t.Helper()
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, content)
	}

	out := map[string][]string{}
	for event, groups := range doc.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				out[event] = append(out[event], hook.Command)
			}
		}
	}

	return out
}

func TestInstallHooksKeepsUnrelatedKeysAndHooksInOrder(t *testing.T) {
	spec := testHookSpec(t, `{"model":"x","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo a && b > c"}]}]},"theme":"dark"}`)

	status, err := InstallHooks(spec, "/opt/cog", false)
	if err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	if status.State != Missing {
		t.Errorf("state = %v, want missing", status.State)
	}

	got := readHookFile(t, spec.Path)
	want := `{
  "model": "x",
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo a && b > c"
          }
        ]
      },
      {
        "hooks": [
          {
            "type": "command",
            "command": "/opt/cog claude-hook"
          }
        ]
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/opt/cog claude-hook"
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "/opt/cog claude-hook"
          }
        ]
      }
    ]
  },
  "theme": "dark"
}
`
	if got != want {
		t.Fatalf("merged file:\n%s\nwant:\n%s", got, want)
	}
}

func TestInstallHooksTwiceLeavesFileByteIdentical(t *testing.T) {
	spec := testHookSpec(t, "")
	if _, err := InstallHooks(spec, "/opt/cog", false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first := readHookFile(t, spec.Path)

	status, err := InstallHooks(spec, "/opt/cog", false)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}

	if status.State != Current {
		t.Errorf("second install state = %v, want current", status.State)
	}
	if got := readHookFile(t, spec.Path); got != first {
		t.Fatalf("second install changed the file:\n%s\nwas:\n%s", got, first)
	}
}

func TestInstallHooksRewritesCogitatorEntriesInPlace(t *testing.T) {
	cases := map[string]struct {
		existing string
		binary   string
		force    bool
		want     State
	}{
		"other binary path": {
			existing: "/opt/cog claude-hook",
			binary:   "/new/cog",
			want:     Stale,
		},
		"binary that no longer exists": {
			existing: "/Users/u/src/gone/cogitator claude-hook",
			binary:   "/new/cog",
			want:     Stale,
		},
		"bare name from the manual setup": {
			existing: "cogitator claude-hook",
			binary:   "/new/cog",
			want:     Stale,
		},
		"quoted path with a space": {
			existing: "'/Applications/My Tools/cogitator' claude-hook",
			binary:   "/new/cog",
			want:     Stale,
		},
		"inline shell wrapper when forced": {
			existing: "cogitator claude-hook 2>>/tmp/cog.log",
			binary:   "/new/cog",
			force:    true,
			want:     Foreign,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := testHookSpec(t, allEventsRunning(tc.existing))

			status, err := InstallHooks(spec, tc.binary, tc.force)
			if err != nil {
				t.Fatalf("InstallHooks: %v", err)
			}
			if status.State != tc.want {
				t.Errorf("state = %v, want %v", status.State, tc.want)
			}

			commands := commandsByEvent(t, readHookFile(t, spec.Path))
			for _, event := range testHookEvents {
				if got := commands[event.Name]; len(got) != 1 || got[0] != tc.binary+" claude-hook" {
					t.Errorf("%s commands = %q, want exactly [%q]", event.Name, got, tc.binary+" claude-hook")
				}
			}
		})
	}
}

func TestInstallHooksDropsDuplicateCogitatorEntries(t *testing.T) {
	spec := testHookSpec(t, `{"hooks":{"Stop":[
		{"hooks":[{"type":"command","command":"/old/cog claude-hook"}]},
		{"hooks":[{"type":"command","command":"echo keep"},{"type":"command","command":"/opt/cog claude-hook"}]}
	]}}`)

	if _, err := InstallHooks(spec, "/opt/cog", false); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}

	got := commandsByEvent(t, readHookFile(t, spec.Path))["Stop"]
	want := []string{"/opt/cog claude-hook", "echo keep"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Stop commands = %q, want %q", got, want)
	}
}

func TestInstallHooksRefusesForeignWiringUntouched(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cogitator-hook-debug.sh")
	writeHookFile(t, script, "#!/bin/sh\ntee -a /tmp/hook.log | /usr/local/bin/cogitator claude-hook\n")

	cases := map[string]string{
		"wrapper script by absolute path": script,
		"wrapper script under home":       "~/cogitator-hook-debug.sh",
		"wrapper run through a shell":     "bash $HOME/cogitator-hook-debug.sh",
		"inline shell pipeline":           "cogitator claude-hook | tee /tmp/x",
	}

	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			content := allEventsRunning(command)
			spec := testHookSpec(t, content)
			spec.Home = dir

			status, err := InstallHooks(spec, "/opt/cog", false)

			var foreign *ForeignError
			if !errors.As(err, &foreign) {
				t.Fatalf("err = %v, want *ForeignError", err)
			}
			if status.State != Foreign || status.Line != 1 || !strings.Contains(status.Detail, "SessionStart") {
				t.Errorf("status = %+v, want foreign SessionStart entry on line 1", status)
			}
			if got := readHookFile(t, spec.Path); got != content {
				t.Fatalf("refused install changed the file:\n%s", got)
			}
		})
	}
}

func TestInstallHooksRejectsUnreadableFilesUntouched(t *testing.T) {
	cases := map[string]string{
		"invalid JSON":         "{not json",
		"top level not object": `["hooks"]`,
		"hooks not an object":  `{"hooks":[]}`,
		"event not a list":     `{"hooks":{"Stop":{"hooks":[]}}}`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			spec := testHookSpec(t, content)

			if _, err := InstallHooks(spec, "/opt/cog", false); err == nil {
				t.Fatal("InstallHooks succeeded, want error")
			}
			if got := readHookFile(t, spec.Path); got != content {
				t.Fatalf("file changed to:\n%s", got)
			}
		})
	}
}

func TestInstallHooksKeepsModeIndentAndSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{\n    \"model\": \"x\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	spec := HookSpec{Path: link, Subcommand: "claude-hook", Events: []HookEvent{{Name: "Stop"}}}
	if _, err := InstallHooks(spec, "/opt/cog", false); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}

	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("settings link replaced by a file (err %v)", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if got := readHookFile(t, target); !strings.HasPrefix(got, "{\n    \"model\": \"x\",\n    \"hooks\": {\n        \"Stop\"") {
		t.Errorf("indentation not kept:\n%s", got)
	}
}

func TestCheckHooksStates(t *testing.T) {
	full := `{"hooks":{
		"SessionStart":[{"hooks":[{"type":"command","command":"/opt/cog claude-hook"}]}],
		"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"/opt/cog claude-hook"}]}],
		"Stop":[{"hooks":[{"type":"command","command":"/opt/cog claude-hook"}]}]}}`

	cases := map[string]struct {
		content string
		want    State
	}{
		"no file":             {"", Missing},
		"no cogitator hooks":  {`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`, Missing},
		"every event wired":   {full, Current},
		"some events missing": {`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/cog claude-hook"}]}]}}`, Stale},
		"other binary":        {strings.ReplaceAll(full, "/opt/cog", "/old/cog"), Stale},
		"unrelated subcommand lookalike": {
			`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"tool my-claude-hook-x"}]}]}}`, Missing,
		},
		"inline wrapper": {strings.Replace(full, "/opt/cog claude-hook", "sh -c 'cogitator claude-hook'", 1), Foreign},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := testHookSpec(t, tc.content)

			status, err := CheckHooks(spec, "/opt/cog")
			if err != nil {
				t.Fatalf("CheckHooks: %v", err)
			}
			if status.State != tc.want || status.Path != spec.Path {
				t.Fatalf("status = %+v, want %v at %s", status, tc.want, spec.Path)
			}
		})
	}
}

func TestHookCommandQuotesPathsTheShellWouldSplit(t *testing.T) {
	cases := map[string]string{
		"/opt/cog":                 "/opt/cog claude-hook",
		"/Applications/My Tools/c": "'/Applications/My Tools/c' claude-hook",
		"/opt/it's/cog":            `'/opt/it'\''s/cog' claude-hook`,
	}

	for binary, want := range cases {
		if got := HookCommand(binary, "claude-hook"); got != want {
			t.Errorf("HookCommand(%q) = %q, want %q", binary, got, want)
		}
	}
}

// allEventsRunning is a compact hook file whose every test event runs command.
func allEventsRunning(command string) string {
	quoted, _ := json.Marshal(command)
	hook := `[{"hooks":[{"type":"command","command":` + string(quoted) + `}]}]`

	return `{"hooks":{"SessionStart":` + hook + `,"PreToolUse":` + hook + `,"Stop":` + hook + `}}`
}
