package hookinstall_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/hookinstall"
	"github.com/guilhermehto/cogitator/internal/installkit"
)

// machine is a temp home holding ~/.claude and ~/.omp/agent but no ~/.codex.
type machine struct {
	home string
	cfg  *config.Config
}

func newMachine(t *testing.T) machine {
	t.Helper()
	home := t.TempDir()
	for _, dir := range []string{".claude", filepath.Join(".omp", "agent")} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	return machine{home: home, cfg: &config.Config{
		ClaudeCodeHome: filepath.Join(home, ".claude"),
		CodexHome:      filepath.Join(home, ".codex"),
		CodexEnabled:   false,
		OmpHome:        filepath.Join(home, ".omp", "agent"),
		OmpEnabled:     true,
	}}
}

func (m machine) path(rel string) string {
	return filepath.Join(m.home, rel)
}

func (m machine) read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(m.path(rel))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// checksums maps every file under home to its sha256.
func (m machine) checksums(t *testing.T) map[string][32]byte {
	t.Helper()
	sums := map[string][32]byte{}
	err := filepath.WalkDir(m.home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		sums[path] = sha256.Sum256(data)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return sums
}

func TestInstallWiresDetectedHarnessesAndSkipsTheRest(t *testing.T) {
	m := newMachine(t)
	var out bytes.Buffer

	if err := hookinstall.Install(hookinstall.Harnesses(m.cfg), "/opt/cog", false, &out); err != nil {
		t.Fatalf("Install: %v\n%s", err, out.String())
	}

	if settings := m.read(t, ".claude/settings.json"); !strings.Contains(settings, `"/opt/cog claude-hook"`) {
		t.Errorf("Claude Code settings do not run /opt/cog:\n%s", settings)
	}
	if ext := m.read(t, ".omp/agent/extensions/cogitator.ts"); !strings.Contains(ext, `const COGITATOR_BIN = "/opt/cog";`) {
		t.Error("omp extension does not run /opt/cog")
	}
	if _, err := os.Stat(m.path(".codex")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.codex was created for an undetected harness (stat err %v)", err)
	}

	for _, want := range []string{
		"Claude Code: installed: " + m.path(".claude/settings.json"),
		"Codex: not found, skipped",
		"omp: installed: " + m.path(".omp/agent/extensions/cogitator.ts"),
		"opencode: no hook needed (discovered over mDNS)",
		"Restart Claude Code for the hooks to take effect.",
		"Restart any running omp sessions",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestInstallSkipsClaudeCodeWithoutItsHomeDirectory(t *testing.T) {
	m := newMachine(t)
	m.cfg.ClaudeCodeHome = m.path("no-claude")
	var out bytes.Buffer

	if err := hookinstall.Install(hookinstall.Harnesses(m.cfg), "/opt/cog", false, &out); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if !strings.Contains(out.String(), "Claude Code: not found, skipped") {
		t.Errorf("output lacks the Claude Code skip:\n%s", out.String())
	}
	if _, err := os.Stat(m.path("no-claude")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Claude home was created (stat err %v)", err)
	}
}

func TestInstallKeepsGoingPastAFailingHarness(t *testing.T) {
	m := newMachine(t)
	if err := os.WriteFile(m.path(".claude/settings.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	err := hookinstall.Install(hookinstall.Harnesses(m.cfg), "/opt/cog", false, &out)

	if err == nil || !strings.Contains(err.Error(), "Claude Code") {
		t.Fatalf("err = %v, want a failure naming Claude Code", err)
	}
	if !strings.Contains(out.String(), "Claude Code: error: ") {
		t.Errorf("output lacks the Claude Code error line:\n%s", out.String())
	}
	if ext := m.read(t, ".omp/agent/extensions/cogitator.ts"); !strings.Contains(ext, "/opt/cog") {
		t.Error("omp was not installed after Claude Code failed")
	}
	if got := m.read(t, ".claude/settings.json"); got != "{not json" {
		t.Errorf("invalid settings changed to %q", got)
	}
}

func TestInstallTwiceChangesNoFile(t *testing.T) {
	m := newMachine(t)
	m.cfg.CodexEnabled = true
	if err := os.MkdirAll(m.path(".codex"), 0o750); err != nil {
		t.Fatal(err)
	}
	harnesses := hookinstall.Harnesses(m.cfg)

	if err := hookinstall.Install(harnesses, "/opt/cog", false, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	before := m.checksums(t)

	var out bytes.Buffer
	if err := hookinstall.Install(harnesses, "/opt/cog", false, &out); err != nil {
		t.Fatalf("second Install: %v", err)
	}

	after := m.checksums(t)
	if len(after) != len(before) {
		t.Errorf("file count went from %d to %d", len(before), len(after))
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("%s changed on the second run", path)
		}
	}
	if strings.Count(out.String(), "already set up") != 3 || strings.Contains(out.String(), "Next:") {
		t.Errorf("second run should report three harnesses already set up and no follow-ups:\n%s", out.String())
	}
}

func TestInstallReportsForeignWiringWithoutFailing(t *testing.T) {
	foreign := installkit.Status{State: installkit.Foreign, Path: "/home/u/.claude/settings.json", Line: 4, Detail: `Stop hook "~/wrap.sh" runs claude-hook`}
	cases := map[string]struct {
		installErr error
		wantErr    bool
		wantLine   string
	}{
		"left alone": {
			installErr: &installkit.ForeignError{Status: foreign},
			wantLine:   `Claude Code: yours, left alone: /home/u/.claude/settings.json:4 (Stop hook "~/wrap.sh" runs claude-hook)`,
		},
		"forced install fails": {
			installErr: errors.New("disk full"),
			wantErr:    true,
			wantLine:   "Claude Code: error: disk full",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			harness := hookinstall.Harness{
				Name:     "Claude Code",
				Detected: func() bool { return true },
				Install: func(string, bool) (installkit.Status, error) {
					return foreign, tc.installErr
				},
				FollowUp: func(string) string { return "restart" },
			}
			var out bytes.Buffer

			err := hookinstall.Install([]hookinstall.Harness{harness}, "/opt/cog", true, &out)

			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want error %v", err, tc.wantErr)
			}
			if !strings.Contains(out.String(), tc.wantLine+"\n") || strings.Contains(out.String(), "restart") {
				t.Errorf("output:\n%s\nwant line %q and no follow-up", out.String(), tc.wantLine)
			}
		})
	}
}

func TestInstallGivesEveryHarnessTheSameBinaryAndForce(t *testing.T) {
	type call struct {
		binary string
		force  bool
	}
	var calls []call
	record := func(binary string, force bool) (installkit.Status, error) {
		calls = append(calls, call{binary, force})
		return installkit.Status{State: installkit.Missing}, nil
	}
	harnesses := []hookinstall.Harness{
		{Name: "a", Detected: func() bool { return true }, Install: record, FollowUp: func(string) string { return "" }},
		{Name: "b", Detected: func() bool { return false }, Install: record, FollowUp: func(string) string { return "" }},
		{Name: "c", Detected: func() bool { return true }, Install: record, FollowUp: func(string) string { return "" }},
	}

	if err := hookinstall.Install(harnesses, "/opt/cog", true, &bytes.Buffer{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	want := []call{{"/opt/cog", true}, {"/opt/cog", true}}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("installer calls = %+v, want %+v (detected harnesses only)", calls, want)
	}
}

func TestCheckMatchesWhatInstallWrites(t *testing.T) {
	m := newMachine(t)
	harnesses := hookinstall.Harnesses(m.cfg)
	before := m.checksums(t)

	for _, harness := range harnesses {
		if !harness.Detected() {
			continue
		}
		status, err := harness.Check("/opt/cog")
		if err != nil || status.State != installkit.Missing {
			t.Fatalf("%s check before install = %+v, %v; want missing", harness.Name, status, err)
		}
	}
	if after := m.checksums(t); len(after) != len(before) {
		t.Fatalf("check wrote files: %v", after)
	}

	if err := hookinstall.Install(harnesses, "/opt/cog", false, &bytes.Buffer{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	for _, harness := range harnesses {
		if !harness.Detected() {
			continue
		}
		if status, err := harness.Check("/opt/cog"); err != nil || status.State != installkit.Current {
			t.Errorf("%s check after install = %+v, %v; want current", harness.Name, status, err)
		}
		if status, err := harness.Check("/new/cog"); err != nil || status.State != installkit.Stale || status.Detail != "/opt/cog -> /new/cog" {
			t.Errorf("%s check for another binary = %+v, %v; want stale /opt/cog -> /new/cog", harness.Name, status, err)
		}
	}
}
