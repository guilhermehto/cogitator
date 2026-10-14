package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/installkit"
)

const testBinary = "/opt/bin/cogitator"

type installCall struct {
	group   string
	binary  string
	keys    []string
	pending []string
}

// fakes is a machine of fake groups recording every installer call.
type fakes struct {
	calls []installCall
}

func (f *fakes) group(name string, shared bool, keys []Key, rows ...Row) Group {
	return Group{
		Name:   name,
		Keys:   keys,
		Shared: shared,
		Check:  func(string) []Row { return rows },
		Install: func(binary string, keys []string, pending []Row) ([]string, error) {
			call := installCall{group: name, binary: binary, keys: keys}
			for _, row := range pending {
				call.pending = append(call.pending, row.Component)
			}
			f.calls = append(f.calls, call)
			return []string{name + " follow-up"}, nil
		},
	}
}

func row(component string, state installkit.State) Row {
	return Row{Component: component, Status: installkit.Status{State: state, Path: "/home/u/" + component + ".conf"}}
}

// machine is tmux, zellij (keybinds + sidebar), and two harnesses, each in
// the given state.
func (f *fakes) machine(state installkit.State) []Group {
	return []Group{
		f.group("tmux", true, []Key{{Name: "next key", Default: "Space"}, {Name: "pick key", Default: "a"}}, row("tmux keybinds", state)),
		f.group("zellij", true, []Key{{Name: "next key", Default: "Space"}}, row("zellij keybinds", state), row("zellij sidebar", state)),
		f.group("hooks", false, nil, row("Claude Code", state), row("omp", state), Row{Component: "opencode", Note: "no hook needed"}),
	}
}

func run(groups []Group, mode Mode, stdin string) (string, error) {
	var out bytes.Buffer
	wizard := Wizard{Binary: testBinary, Home: "/home/u", Groups: groups, In: strings.NewReader(stdin), Out: &out}
	err := wizard.Run(mode)

	return out.String(), err
}

func TestRunInstallsEveryMissingComponentWithOneBinary(t *testing.T) {
	cases := map[string]struct {
		mode  Mode
		stdin string
	}{
		"interactive yes to every group": {mode: Interactive, stdin: "y\n\n\ny\n\ny\n"},
		"--yes without prompts":          {mode: Yes},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakes{}
			out, err := run(f.machine(installkit.Missing), tc.mode, tc.stdin)
			if err != nil {
				t.Fatalf("Run: %v\n%s", err, out)
			}

			if len(f.calls) != 3 {
				t.Fatalf("installer calls = %+v, want one per group", f.calls)
			}
			for _, call := range f.calls {
				if call.binary != testBinary {
					t.Errorf("%s installed with %q, want %q", call.group, call.binary, testBinary)
				}
			}
			if got := strings.Join(f.calls[2].pending, ","); got != "Claude Code,omp" {
				t.Errorf("hooks pending = %s, want Claude Code,omp", got)
			}
			for _, want := range []string{"tmux follow-up", "hooks follow-up", "Keep the cogitator TUI running"} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestRunOffersKeysWithTheirDefaults(t *testing.T) {
	cases := map[string]struct {
		stdin    string
		wantKeys string
	}{
		"blank answers keep the defaults": {stdin: "y\n\n\n", wantKeys: "Space,a"},
		"typed keys replace the defaults": {stdin: "y\nn\nC-p\n", wantKeys: "n,C-p"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakes{}
			groups := f.machine(installkit.Missing)[:1]

			out, err := run(groups, Interactive, tc.stdin)
			if err != nil {
				t.Fatal(err)
			}

			if len(f.calls) != 1 || strings.Join(f.calls[0].keys, ",") != tc.wantKeys {
				t.Fatalf("calls = %+v, want keys %s", f.calls, tc.wantKeys)
			}
			if !strings.Contains(out, "next key [Space]: ") || !strings.Contains(out, "pick key [a]: ") {
				t.Fatalf("key prompts lack defaults:\n%s", out)
			}
		})
	}
}

func TestRunDeclinedOrUnansweredGroupsInstallNothing(t *testing.T) {
	cases := map[string]string{
		"explicit no":  "n\nn\nn\n",
		"blank answer": "\n\n\n",
		"closed stdin": "",
	}

	for name, stdin := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakes{}
			out, err := run(f.machine(installkit.Missing), Interactive, stdin)
			if err != nil {
				t.Fatal(err)
			}

			if len(f.calls) != 0 {
				t.Fatalf("installer calls = %+v, want none", f.calls)
			}
			if strings.Contains(out, "Next:") {
				t.Fatalf("follow-ups printed without installing:\n%s", out)
			}
		})
	}
}

func TestRunOnASetUpMachineChangesNothing(t *testing.T) {
	for _, mode := range []Mode{Interactive, Yes, DryRun, CheckOnly} {
		f := &fakes{}
		groups := append(f.machine(installkit.Current), f.group("extra", false, nil,
			Row{Component: "Codex", Status: installkit.Status{State: installkit.Unavailable, Detail: "not found"}}))

		out, err := run(groups, mode, "y\ny\ny\n")
		if err != nil {
			t.Fatalf("mode %d: Run: %v", mode, err)
		}

		if len(f.calls) != 0 {
			t.Fatalf("mode %d: installer calls = %+v, want none", mode, f.calls)
		}
		if !strings.Contains(out, "everything is already set up") || strings.Contains(out, "[y/N]") {
			t.Fatalf("mode %d: output = %s, want the already-set-up message and no prompt", mode, out)
		}
		if !strings.Contains(out, "already set up, skip") || !strings.Contains(out, "not installed, skip") {
			t.Fatalf("mode %d: plan lacks the skip actions:\n%s", mode, out)
		}
	}
}

func TestRunLeavesForeignWiringAlone(t *testing.T) {
	foreign := Row{Component: "zellij keybinds", Status: installkit.Status{State: installkit.Foreign, Path: "/home/u/.config/zellij/config.kdl", Line: 12, Detail: `bind "a" { Run "cogitator" "pick"; }`}}
	foreignHook := Row{Component: "Claude Code", Status: installkit.Status{State: installkit.Foreign, Path: "/home/u/.claude/settings.json", Line: 4}}

	f := &fakes{}
	groups := []Group{
		f.group("zellij", true, nil, foreign, row("zellij sidebar", installkit.Missing)),
		f.group("hooks", false, nil, foreignHook, row("omp", installkit.Missing)),
	}

	out, err := run(groups, Yes, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 1 || f.calls[0].group != "hooks" || strings.Join(f.calls[0].pending, ",") != "omp" {
		t.Fatalf("installer calls = %+v, want only the hooks installer, for omp", f.calls)
	}
	for _, want := range []string{
		"~/.config/zellij/config.kdl:12",
		"~/.claude/settings.json:4",
		"yours, left alone",
		"skip, zellij is wired by hand",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRunUpdatesStaleWiringShowingOldAndNew(t *testing.T) {
	stale := Row{Component: "tmux keybinds", Status: installkit.Status{
		State:  installkit.Stale,
		Path:   "/home/u/.config/cogitator/tmux.conf",
		Detail: "/old/cogitator -> " + testBinary,
	}}

	f := &fakes{}
	out, err := run([]Group{f.group("tmux", true, nil, stale)}, Yes, "")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "update: /old/cogitator -> "+testBinary) {
		t.Fatalf("plan lacks old -> new:\n%s", out)
	}
	if len(f.calls) != 1 || f.calls[0].group != "tmux" {
		t.Fatalf("installer calls = %+v, want the tmux update", f.calls)
	}
}

func TestRunPlanOnlyModesNeverInstall(t *testing.T) {
	cases := map[string]struct {
		mode    Mode
		state   installkit.State
		wantErr error
		wantOut string
	}{
		"dry run with something missing": {mode: DryRun, state: installkit.Missing, wantOut: "dry run: nothing written"},
		"check with something missing":   {mode: CheckOnly, state: installkit.Missing, wantErr: ErrIncomplete},
		"check with something stale":     {mode: CheckOnly, state: installkit.Stale, wantErr: ErrIncomplete},
		"check on a set-up machine":      {mode: CheckOnly, state: installkit.Current, wantOut: "everything is already set up"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakes{}
			out, err := run(f.machine(tc.state), tc.mode, "y\ny\ny\n")

			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("Run error = %v, want %v", err, tc.wantErr)
			}
			if len(f.calls) != 0 {
				t.Fatalf("installer calls = %+v, want none", f.calls)
			}
			if strings.Contains(out, "[y/N]") || !strings.Contains(out, tc.wantOut) {
				t.Fatalf("output = %s, want %q and no prompt", out, tc.wantOut)
			}
		})
	}
}

func TestRunKeepsGoingPastAFailedCheck(t *testing.T) {
	f := &fakes{}
	broken := Row{Component: "Codex", Err: errors.New("invalid JSON")}
	groups := []Group{
		f.group("tmux", true, nil, row("tmux keybinds", installkit.Missing)),
		f.group("hooks", false, nil, broken, row("omp", installkit.Missing)),
	}

	out, err := run(groups, Yes, "")
	if err == nil || !strings.Contains(err.Error(), "Codex") {
		t.Fatalf("Run error = %v, want one naming Codex", err)
	}

	if len(f.calls) != 2 || strings.Join(f.calls[1].pending, ",") != "omp" {
		t.Fatalf("installer calls = %+v, want tmux and omp", f.calls)
	}
	if !strings.Contains(out, "error: invalid JSON") {
		t.Fatalf("plan lacks the check error:\n%s", out)
	}
}

func TestModeForPrefersModesThatWriteNothing(t *testing.T) {
	cases := map[string]struct {
		yes, dryRun, check bool
		want               Mode
	}{
		"no flags":          {want: Interactive},
		"--yes":             {yes: true, want: Yes},
		"--yes --dry-run":   {yes: true, dryRun: true, want: DryRun},
		"--dry-run --check": {dryRun: true, check: true, want: CheckOnly},
	}

	for name, tc := range cases {
		if got := ModeFor(tc.yes, tc.dryRun, tc.check); got != tc.want {
			t.Errorf("%s: ModeFor = %d, want %d", name, got, tc.want)
		}
	}
}

func TestRunShortensHomeOnlyAtThePathStart(t *testing.T) {
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	f := &fakes{}
	rows := []Row{
		{Component: "via link", Status: installkit.Status{State: installkit.Current, Path: link + "/a.conf"}},
		{Component: "resolved", Status: installkit.Status{State: installkit.Current, Path: target + "/b.conf"}},
		{Component: "elsewhere", Status: installkit.Status{State: installkit.Current, Path: "/mnt" + link + "/c.conf"}},
	}
	var out bytes.Buffer
	wizard := Wizard{Binary: testBinary, Home: link, Groups: []Group{f.group("x", false, nil, rows...)}, In: strings.NewReader(""), Out: &out}
	if err := wizard.Run(Yes); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{" ~/a.conf ", " ~/b.conf ", " /mnt" + link + "/c.conf "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}
}
