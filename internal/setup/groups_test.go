package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/hookinstall"
	"github.com/guilhermehto/cogitator/internal/installkit"
	"github.com/guilhermehto/cogitator/internal/tmuxinstall"
	"github.com/guilhermehto/cogitator/internal/zellijinstall"
)

func fakeEnv(onPath map[string]bool, zellijVersion string) Env {
	return Env{
		LookPath: func(file string) (string, error) {
			if onPath[file] {
				return "/usr/bin/" + file, nil
			}
			return "", errors.New("not on PATH")
		},
		Output: func(string, ...string) (string, error) {
			if zellijVersion == "" {
				return "", errors.New("exit status 1")
			}
			return zellijVersion + "\n", nil
		},
	}
}

func TestZellijNotesWarnOffThePinnedMinor(t *testing.T) {
	cases := map[string]struct {
		version  string
		wantWarn string
	}{
		"pinned minor":      {version: "zellij 0.45.1"},
		"later patch":       {version: "zellij 0.45.7"},
		"newer minor":       {version: "zellij 0.46.0", wantWarn: "found zellij 0.46.0"},
		"older minor":       {version: "zellij 0.44.3", wantWarn: "found zellij 0.44.3"},
		"unreadable output": {wantWarn: "could not read the zellij version"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			notes := strings.Join(zellijNotes(fakeEnv(nil, tc.version)), "\n")
			if tc.wantWarn == "" && notes != "" {
				t.Fatalf("notes = %q, want none", notes)
			}
			if !strings.Contains(notes, tc.wantWarn) || (tc.wantWarn != "" && !strings.Contains(notes, "0.45.x")) {
				t.Fatalf("notes = %q, want %q naming 0.45.x", notes, tc.wantWarn)
			}
		})
	}
}

func TestMultiplexerGroupsSkipToolsNotOnPath(t *testing.T) {
	env := fakeEnv(nil, "")
	groups := []Group{
		TmuxGroup(env, tmuxinstall.Installer{}, false),
		ZellijGroup(env, zellijinstall.Installer{}, nil),
	}

	for _, group := range groups {
		for _, row := range group.Check(testBinary) {
			if row.Status.State != installkit.Unavailable || !strings.Contains(row.Status.Detail, "not on PATH") {
				t.Errorf("%s = %+v, want unavailable, not on PATH", row.Component, row.Status)
			}
		}
		if group.Install != nil {
			t.Errorf("%s has an installer although its tool is missing", group.Name)
		}
	}
}

func newZellijInstaller(t *testing.T) (zellijinstall.Installer, string) {
	t.Helper()

	home := t.TempDir()
	conf := filepath.Join(home, ".config", "zellij", "config.kdl")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("default_mode \"locked\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return zellijinstall.Installer{Home: home, Out: &bytes.Buffer{}}, conf
}

func TestZellijGroupDefaultsToTheKeysAlreadyInstalled(t *testing.T) {
	bundled := &zellijinstall.BundledPlugin{Wasm: []byte("\x00asm test"), Version: "1.0.0"}
	cases := map[string]struct {
		installed *zellijinstall.Options
		wantKeys  string
		wantState installkit.State
	}{
		"fresh config offers the installer defaults": {
			wantKeys:  "Space,a,g",
			wantState: installkit.Missing,
		},
		"custom keys are offered back and stay current": {
			installed: &zellijinstall.Options{Binary: testBinary, NextKey: "n", PickKey: "p", SidebarKey: "s", Bundled: bundled},
			wantKeys:  "n,p,s",
			wantState: installkit.Current,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			installer, _ := newZellijInstaller(t)
			if tc.installed != nil {
				if err := installer.Install(*tc.installed); err != nil {
					t.Fatal(err)
				}
			}

			group := ZellijGroup(fakeEnv(map[string]bool{"zellij": true}, "zellij 0.45.1"), installer, bundled)
			if got := strings.Join(defaults(group.Keys), ","); got != tc.wantKeys {
				t.Fatalf("key defaults = %s, want %s", got, tc.wantKeys)
			}
			for _, row := range group.Check(testBinary) {
				if row.Err != nil || row.Status.State != tc.wantState {
					t.Errorf("%s = %+v (%v), want %v", row.Component, row.Status, row.Err, tc.wantState)
				}
			}
		})
	}
}

func TestZellijGroupWithoutBundledPluginSaysSo(t *testing.T) {
	installer, _ := newZellijInstaller(t)
	group := ZellijGroup(fakeEnv(map[string]bool{"zellij": true}, "zellij 0.45.1"), installer, nil)

	rows := group.Check(testBinary)
	if len(rows) != 2 || rows[1].Status.State != installkit.Unavailable || rows[1].Status.Detail != "plugin not bundled in this build" {
		t.Fatalf("rows = %+v, want an unavailable sidebar naming the missing plugin", rows)
	}
	if len(group.Keys) != 2 {
		t.Fatalf("keys = %+v, want next and pick only", group.Keys)
	}
}

func TestHooksGroupInstallsOnlyPendingHarnesses(t *testing.T) {
	var installed []string
	harness := func(name string, detected bool, state installkit.State) hookinstall.Harness {
		return hookinstall.Harness{
			Name:     name,
			Detected: func() bool { return detected },
			Check: func(string) (installkit.Status, error) {
				return installkit.Status{State: state, Path: "/h/" + name}, nil
			},
			Install: func(binary string, force bool) (installkit.Status, error) {
				if force {
					t.Errorf("%s installed with force", name)
				}
				installed = append(installed, name+"="+binary)
				return installkit.Status{State: state, Path: "/h/" + name}, nil
			},
			FollowUp: func(string) string { return "restart " + name },
		}
	}

	var out bytes.Buffer
	group := HooksGroup(fakeEnv(nil, ""), []hookinstall.Harness{
		harness("Claude Code", true, installkit.Missing),
		harness("Codex", false, installkit.Missing),
		harness("omp", true, installkit.Foreign),
	}, &out)

	rows := group.Check(testBinary)
	states := make([]string, 0, len(rows))
	for _, row := range rows {
		states = append(states, row.Component+":"+row.Status.State.String())
	}
	if got := strings.Join(states, ","); got != "Claude Code:missing,Codex:unavailable,omp:foreign,opencode:unavailable" {
		t.Fatalf("rows = %s", got)
	}

	var pending []Row
	for _, row := range rows {
		if row.pending() {
			pending = append(pending, row)
		}
	}
	followUps, err := group.Install(testBinary, nil, pending)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(installed, ",") != "Claude Code="+testBinary {
		t.Fatalf("installed = %v, want only Claude Code", installed)
	}
	if strings.Join(followUps, ",") != "restart Claude Code" || !strings.Contains(out.String(), "Claude Code: installed /h/Claude Code") {
		t.Fatalf("follow-ups = %v, output = %q", followUps, out.String())
	}
}
