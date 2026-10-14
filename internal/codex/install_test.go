package codex_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/installkit"
)

type hooksFile struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// hookCommands reads hooks.json as event -> [matcher + " " + command].
func hookCommands(t *testing.T, path string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var parsed hooksFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("hooks.json is not JSON: %v\n%s", err, data)
	}

	out := map[string][]string{}
	for event, groups := range parsed.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				out[event] = append(out[event], strings.TrimSpace(group.Matcher+" "+hook.Command))
			}
		}
	}

	return out
}

func assertEveryEventRuns(t *testing.T, path, command string) {
	t.Helper()
	commands := hookCommands(t, path)
	for _, event := range codex.HookEvents {
		want := strings.TrimSpace(event.Matcher + " " + command)
		if got := commands[event.Name]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want exactly [%q]", event.Name, got, want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// everyEventRunning is a hooks.json whose six events each run command.
func everyEventRunning(t *testing.T, command string) string {
	t.Helper()
	hooks := map[string]any{}
	for _, event := range codex.HookEvents {
		group := map[string]any{"hooks": []any{map[string]string{"type": "command", "command": command}}}
		if event.Matcher != "" {
			group["matcher"] = event.Matcher
		}
		hooks[event.Name] = []any{group}
	}

	data, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestHomeDir_DefaultsToDotCodexUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := map[string]struct{ codexHome, want string }{
		"CODEX_HOME unset": {"", filepath.Join(home, ".codex")},
		"CODEX_HOME set":   {"/tmp/x", "/tmp/x"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := codex.HomeDir(tc.codexHome)
			if err != nil || got != tc.want {
				t.Fatalf("HomeDir(%q) = %q, %v; want %q", tc.codexHome, got, err, tc.want)
			}
		})
	}
}

func TestInstallHooks_CreatesHooksFile(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")

	status, err := codex.InstallHooks(codexHome, "/opt/cog", false)
	if err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}

	path := filepath.Join(codexHome, "hooks.json")
	if status.State != installkit.Missing || status.Path != path {
		t.Errorf("status = %+v, want missing at %s", status, path)
	}
	assertEveryEventRuns(t, path, "/opt/cog codex-hook")
}

func TestInstallHooks_RerunIsByteIdenticalAndKeepsOtherHooks(t *testing.T) {
	codexHome := t.TempDir()
	path := filepath.Join(codexHome, "hooks.json")
	writeFile(t, path, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"notify-send done"}]}]}}`)

	if _, err := codex.InstallHooks(codexHome, "/opt/cog", false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first, _ := os.ReadFile(path)

	status, err := codex.InstallHooks(codexHome, "/opt/cog", false)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if again, _ := os.ReadFile(path); string(again) != string(first) || status.State != installkit.Current {
		t.Fatalf("rerun state %v changed the file:\n%s", status.State, again)
	}

	stop := hookCommands(t, path)["Stop"]
	if len(stop) != 2 || stop[0] != "notify-send done" || stop[1] != "/opt/cog codex-hook" {
		t.Errorf("Stop = %q, want the user's hook then cogitator's", stop)
	}
}

func TestInstallHooks_InvalidJSONLeavesFileUntouched(t *testing.T) {
	codexHome := t.TempDir()
	path := filepath.Join(codexHome, "hooks.json")
	writeFile(t, path, "{not json")

	if _, err := codex.InstallHooks(codexHome, "/opt/cog", false); err == nil {
		t.Fatal("InstallHooks succeeded on invalid JSON")
	}
	if got, _ := os.ReadFile(path); string(got) != "{not json" {
		t.Fatalf("file changed to %q", got)
	}
}

func TestInstallHooks_ReplacesMissingBinaryInPlace(t *testing.T) {
	codexHome := t.TempDir()
	path := filepath.Join(codexHome, "hooks.json")
	gone := filepath.Join(codexHome, "src", "opencode-monitor-add-codex-support", "cogitator")
	writeFile(t, path, everyEventRunning(t, gone+" codex-hook"))

	before, err := codex.CheckHooks(codexHome, "/opt/cog")
	if err != nil || before.State != installkit.Stale {
		t.Fatalf("CheckHooks = %+v, %v; want stale", before, err)
	}

	if _, err := codex.InstallHooks(codexHome, "/opt/cog", false); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	assertEveryEventRuns(t, path, "/opt/cog codex-hook")
}

func TestInstallHooks_RefusesWiringOutsideHooksJSON(t *testing.T) {
	cases := map[string]struct {
		setup    func(t *testing.T, codexHome string)
		wantFile string
		wantLine int
	}{
		"wrapper script in hooks.json": {
			setup: func(t *testing.T, codexHome string) {
				script := filepath.Join(codexHome, "cogitator-hook-debug.sh")
				writeFile(t, script, "#!/bin/sh\nexec cogitator codex-hook\n")
				writeFile(t, filepath.Join(codexHome, "hooks.json"), everyEventRunning(t, script))
			},
			wantFile: "hooks.json",
		},
		"inline hooks in config.toml": {
			setup: func(t *testing.T, codexHome string) {
				writeFile(t, filepath.Join(codexHome, "config.toml"),
					"# command = \"cogitator codex-hook\"\n[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"cogitator codex-hook\"\n")
			},
			wantFile: "config.toml",
			wantLine: 5,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			codexHome := t.TempDir()
			tc.setup(t, codexHome)
			hooksPath := filepath.Join(codexHome, "hooks.json")
			before, _ := os.ReadFile(hooksPath)

			status, err := codex.InstallHooks(codexHome, "/opt/cog", false)

			var foreign *installkit.ForeignError
			if !errors.As(err, &foreign) {
				t.Fatalf("err = %v, want *ForeignError", err)
			}
			if status.Path != filepath.Join(codexHome, tc.wantFile) || (tc.wantLine > 0 && status.Line != tc.wantLine) {
				t.Errorf("status = %+v, want %s line %d", status, tc.wantFile, tc.wantLine)
			}
			if after, _ := os.ReadFile(hooksPath); string(after) != string(before) {
				t.Fatalf("refused install changed hooks.json:\n%s", after)
			}

			if _, err := codex.InstallHooks(codexHome, "/opt/cog", true); err != nil {
				t.Fatalf("forced install: %v", err)
			}
			assertEveryEventRuns(t, hooksPath, "/opt/cog codex-hook")
		})
	}
}
