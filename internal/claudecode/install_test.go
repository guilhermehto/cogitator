package claudecode_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/installkit"
)

type settingsFile struct {
	Model string                    `json:"model"`
	Hooks map[string][]matcherGroup `json:"hooks"`
}

type matcherGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func readSettings(t *testing.T, path string) (string, settingsFile) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var parsed settingsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings.json is not JSON: %v\n%s", err, data)
	}

	return string(data), parsed
}

func writeSettings(t *testing.T, claudeHome, content string) string {
	t.Helper()
	path := filepath.Join(claudeHome, "settings.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// cogitatorGroups returns, per event, the groups whose hooks run command.
func cogitatorGroups(settings settingsFile, command string) map[string][]matcherGroup {
	out := map[string][]matcherGroup{}
	for event, groups := range settings.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if hook.Command == command {
					out[event] = append(out[event], group)
				}
			}
		}
	}

	return out
}

func assertWiredOnce(t *testing.T, settings settingsFile, bin string) {
	t.Helper()
	wired := cogitatorGroups(settings, bin+" claude-hook")
	for _, event := range claudecode.HookEvents {
		groups := wired[event.Name]
		if len(groups) != 1 {
			t.Errorf("%s has %d cogitator hooks, want 1", event.Name, len(groups))
			continue
		}
		if groups[0].Matcher != event.Matcher {
			t.Errorf("%s matcher = %q, want %q", event.Name, groups[0].Matcher, event.Matcher)
		}
	}
}

func TestInstallHooks_MergesIntoExistingSettings(t *testing.T) {
	claudeHome := t.TempDir()
	path := writeSettings(t, claudeHome, `{"model":"x","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`)

	status, err := claudecode.InstallHooks(claudeHome, "/opt/cog", false)
	if err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	if status.State != installkit.Missing || status.Path != path {
		t.Errorf("status = %+v, want missing at %s", status, path)
	}

	_, settings := readSettings(t, path)
	if settings.Model != "x" {
		t.Errorf("model = %q, want x", settings.Model)
	}
	if len(cogitatorGroups(settings, "echo hi")["Stop"]) != 1 {
		t.Error("the user's echo hook on Stop was lost")
	}
	assertWiredOnce(t, settings, "/opt/cog")
}

func TestInstallHooks_RerunIsByteIdenticalAndNewBinaryReplaces(t *testing.T) {
	claudeHome := t.TempDir()
	if _, err := claudecode.InstallHooks(claudeHome, "/opt/cog", false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	path := filepath.Join(claudeHome, "settings.json")
	first, _ := readSettings(t, path)

	status, err := claudecode.InstallHooks(claudeHome, "/opt/cog", false)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if again, _ := readSettings(t, path); again != first || status.State != installkit.Current {
		t.Fatalf("rerun state %v changed the file:\n%s", status.State, again)
	}

	status, err = claudecode.InstallHooks(claudeHome, "/new/cog", false)
	if err != nil {
		t.Fatalf("install with new binary: %v", err)
	}
	if status.State != installkit.Stale {
		t.Errorf("state = %v, want stale", status.State)
	}

	content, settings := readSettings(t, path)
	if strings.Contains(content, "/opt/cog") {
		t.Errorf("old binary entries survived:\n%s", content)
	}
	assertWiredOnce(t, settings, "/new/cog")
}

func TestInstallHooks_InvalidJSONLeavesFileUntouched(t *testing.T) {
	claudeHome := t.TempDir()
	path := writeSettings(t, claudeHome, "{not json")

	if _, err := claudecode.InstallHooks(claudeHome, "/opt/cog", false); err == nil {
		t.Fatal("InstallHooks succeeded on invalid JSON")
	}
	if got, _ := os.ReadFile(path); string(got) != "{not json" {
		t.Fatalf("file changed to %q", got)
	}
}

func TestInstallHooks_WrapperScriptInEveryEvent(t *testing.T) {
	claudeHome := t.TempDir()
	script := filepath.Join(claudeHome, "cogitator-hook-debug.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntee -a /tmp/hook.log | cogitator claude-hook\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	groups := map[string][]matcherGroup{}
	for _, event := range claudecode.HookEvents {
		groups[event.Name] = []matcherGroup{{Matcher: event.Matcher, Hooks: []hookEntry{{Type: "command", Command: script}}}}
	}
	data, err := json.MarshalIndent(map[string]any{"hooks": groups}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := writeSettings(t, claudeHome, string(data))

	status, err := claudecode.InstallHooks(claudeHome, "/opt/cog", false)
	var foreign *installkit.ForeignError
	if !errors.As(err, &foreign) || status.State != installkit.Foreign {
		t.Fatalf("status %+v err %v, want a foreign refusal", status, err)
	}
	if !strings.Contains(err.Error(), script) {
		t.Errorf("refusal %q does not name the wrapper entry", err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(data) {
		t.Fatalf("refused install changed the file:\n%s", got)
	}

	if _, err := claudecode.InstallHooks(claudeHome, "/opt/cog", true); err != nil {
		t.Fatalf("forced install: %v", err)
	}
	content, settings := readSettings(t, path)
	if strings.Contains(content, script) {
		t.Errorf("forced install kept the wrapper next to cogitator's hook:\n%s", content)
	}
	assertWiredOnce(t, settings, "/opt/cog")
}

func TestCheckHooks_CurrentAfterInstall(t *testing.T) {
	claudeHome := t.TempDir()
	before, err := claudecode.CheckHooks(claudeHome, "/opt/cog")
	if err != nil || before.State != installkit.Missing {
		t.Fatalf("before install: %+v, %v; want missing", before, err)
	}

	if _, err := claudecode.InstallHooks(claudeHome, "/opt/cog", false); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}

	after, err := claudecode.CheckHooks(claudeHome, "/opt/cog")
	if err != nil || after.State != installkit.Current {
		t.Fatalf("after install: %+v, %v; want current", after, err)
	}
}
