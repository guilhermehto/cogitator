package tmuxinstall

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const testBinary = "/opt/bin/cogitator"

type testEnv struct {
	home      string
	installer Installer
	out       *bytes.Buffer
	warn      *bytes.Buffer
}

func newTestEnv(t *testing.T, runner tmuxctl.Runner) testEnv {
	t.Helper()

	home := t.TempDir()
	out := &bytes.Buffer{}
	warn := &bytes.Buffer{}

	return testEnv{
		home: home,
		out:  out,
		warn: warn,
		installer: Installer{
			Home:       home,
			OwnedPath:  filepath.Join(home, ".config", "cogitator", "tmux.conf"),
			LookPath:   func(string) (string, error) { return "", errors.New("not on PATH") },
			Executable: func() (string, error) { return testBinary, nil },
			Tmux:       runner,
			Out:        out,
			Warn:       warn,
		},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func countLines(content, needle string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, needle) {
			count++
		}
	}

	return count
}

func TestInstallFollowsSymlinkedTmuxConf(t *testing.T) {
	env := newTestEnv(t, nil)
	target := filepath.Join(env.home, "dotfiles", "tmux.conf")
	link := filepath.Join(env.home, ".tmux.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("set -g mouse on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := env.installer.Install(Options{NoReload: true}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("~/.tmux.conf is no longer a symlink")
	}

	content := readFile(t, target)
	if got := countLines(content, "source-file -q"); got != 1 {
		t.Fatalf("want 1 source-file line, got %d in %q", got, content)
	}
	if !strings.HasPrefix(content, "set -g mouse on\n") {
		t.Fatalf("existing content altered: %q", content)
	}

	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if targetInfo.Mode().Perm() != 0o600 {
		t.Fatalf("mode not preserved: %v", targetInfo.Mode().Perm())
	}
}

func TestInstallTwiceKeepsSingleSourceLine(t *testing.T) {
	env := newTestEnv(t, nil)
	conf := filepath.Join(env.home, ".tmux.conf")
	if err := os.WriteFile(conf, []byte("set -g mouse on\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := env.installer.Install(Options{NoReload: true}); err != nil {
			t.Fatal(err)
		}
	}

	if got := countLines(readFile(t, conf), "source-file -q"); got != 1 {
		t.Fatalf("want 1 source-file line, got %d", got)
	}
}

func TestInstallCreatesHomeTmuxConfWhenNoneExists(t *testing.T) {
	env := newTestEnv(t, nil)

	if err := env.installer.Install(Options{NoReload: true}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(env.home, ".tmux.conf"))
	if !strings.Contains(content, "source-file -q "+env.installer.OwnedPath) {
		t.Fatalf("source line missing: %q", content)
	}
}

func TestInstallPrefersExistingXDGConfigOverCreatingHomeConf(t *testing.T) {
	env := newTestEnv(t, nil)
	env.installer.XDGConfigHome = filepath.Join(env.home, "xdg")
	xdgConf := filepath.Join(env.installer.XDGConfigHome, "tmux", "tmux.conf")
	if err := os.MkdirAll(filepath.Dir(xdgConf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgConf, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := env.installer.Install(Options{NoReload: true}); err != nil {
		t.Fatal(err)
	}

	if countLines(readFile(t, xdgConf), "source-file -q") != 1 {
		t.Fatal("source line not added to XDG tmux.conf")
	}
	if _, err := os.Stat(filepath.Join(env.home, ".tmux.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("~/.tmux.conf should not have been created")
	}
}

func TestInstallBindsCustomKeys(t *testing.T) {
	env := newTestEnv(t, nil)

	if err := env.installer.Install(Options{NextKey: "n", PickKey: "A", NoReload: true}); err != nil {
		t.Fatal(err)
	}

	owned := readFile(t, env.installer.OwnedPath)
	if !strings.Contains(owned, "bind n run-shell") || !strings.Contains(owned, "bind A display-popup") {
		t.Fatalf("custom keys not bound:\n%s", owned)
	}
}

func TestInstallRejectsGoRunBinary(t *testing.T) {
	env := newTestEnv(t, nil)

	err := env.installer.Install(Options{Binary: "/var/tmp/go-build123/b001/exe/cogitator", NoReload: true})
	if err == nil {
		t.Fatal("expected go run binary to be rejected")
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	env := newTestEnv(t, nil)

	if err := env.installer.Install(Options{DryRun: true}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{env.installer.OwnedPath, filepath.Join(env.home, ".tmux.conf")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s should not exist after dry run", path)
		}
	}
	if !strings.Contains(env.out.String(), "bind Space") {
		t.Fatalf("dry run should print the owned file: %q", env.out.String())
	}
}

func TestUninstallRestoresTmuxConfByteForByte(t *testing.T) {
	env := newTestEnv(t, nil)
	conf := filepath.Join(env.home, ".tmux.conf")
	original := "set -g mouse on\n# keep me\n\nbind r source-file ~/.tmux.conf\n"
	if err := os.WriteFile(conf, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := env.installer.Install(Options{NoReload: true}); err != nil {
		t.Fatal(err)
	}

	if err := env.installer.Uninstall(Options{NoReload: true}); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, conf); got != original {
		t.Fatalf("tmux.conf changed:\n got %q\nwant %q", got, original)
	}
	if _, err := os.Stat(env.installer.OwnedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned file should be deleted")
	}
}

func TestUninstallDryRunKeepsFiles(t *testing.T) {
	env := newTestEnv(t, nil)
	if err := env.installer.Install(Options{NoReload: true}); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(env.home, ".tmux.conf")
	before := readFile(t, conf)

	if err := env.installer.Uninstall(Options{DryRun: true}); err != nil {
		t.Fatal(err)
	}

	if readFile(t, conf) != before {
		t.Fatal("tmux.conf changed during dry run")
	}
	if _, err := os.Stat(env.installer.OwnedPath); err != nil {
		t.Fatal("owned file removed during dry run")
	}
}

type fakeTmux struct {
	bindings map[string]string
	calls    [][]string
}

func (f *fakeTmux) Run(args ...string) (string, error) {
	f.calls = append(f.calls, args)

	switch args[0] {
	case "list-keys":
		return f.bindings[args[len(args)-1]], nil
	default:
		return "", nil
	}
}

func (f *fakeTmux) ran(command string) bool {
	for _, call := range f.calls {
		if call[0] == command {
			return true
		}
	}

	return false
}

func TestInstallWarnsOnForeignBindingAndStillApplies(t *testing.T) {
	tmux := &fakeTmux{bindings: map[string]string{"a": "bind-key -T prefix a choose-tree"}}
	env := newTestEnv(t, tmux)

	if err := env.installer.Install(Options{}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(env.warn.String(), "prefix+a") {
		t.Fatalf("expected conflict warning, got %q", env.warn.String())
	}
	if !tmux.ran("source-file") {
		t.Fatal("bindings were not applied to the running server")
	}
}

func TestInstallNoReloadSkipsRunningServer(t *testing.T) {
	tmux := &fakeTmux{}
	env := newTestEnv(t, tmux)

	if err := env.installer.Install(Options{NoReload: true}); err != nil {
		t.Fatal(err)
	}

	if len(tmux.calls) != 0 {
		t.Fatalf("tmux touched despite --no-reload: %v", tmux.calls)
	}
}

func TestUninstallUnbindsOnlyKeysRunningCogitator(t *testing.T) {
	tmux := &fakeTmux{bindings: map[string]string{
		"n": `bind-key -T prefix n run-shell -b "'/x/cogitator' next --client '#{client_name}'"`,
		"A": "bind-key -T prefix A choose-tree",
	}}
	env := newTestEnv(t, tmux)
	if err := env.installer.Install(Options{NextKey: "n", PickKey: "A", NoReload: true}); err != nil {
		t.Fatal(err)
	}

	if err := env.installer.Uninstall(Options{}); err != nil {
		t.Fatal(err)
	}

	var unbound []string
	for _, call := range tmux.calls {
		if call[0] == "unbind-key" {
			unbound = append(unbound, call[len(call)-1])
		}
	}
	if len(unbound) != 1 || unbound[0] != "n" {
		t.Fatalf("want only n unbound, got %v", unbound)
	}
}

func TestInstalledBindingWithSpacedBinaryPathLoadsInTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	env := newTestEnv(t, nil)
	binary := filepath.Join(env.home, "my bin", "cogitator")
	if err := env.installer.Install(Options{Binary: binary, NoReload: true}); err != nil {
		t.Fatal(err)
	}

	socketDir, err := os.MkdirTemp("", "cog")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "s")
	tmux := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...).CombinedOutput()
		return string(out), err
	}

	if out, err := tmux("new-session", "-d"); err != nil {
		t.Skipf("cannot start scratch tmux server: %v %s", err, out)
	}
	t.Cleanup(func() { tmux("kill-server") })

	if out, err := tmux("has-session"); err != nil {
		t.Skipf("scratch tmux server unreachable: %v %s", err, out)
	}

	if out, err := tmux("source-file", env.installer.OwnedPath); err != nil {
		t.Fatalf("source-file failed: %v %s", err, out)
	}

	for _, key := range []string{"Space", "a"} {
		out, err := tmux("list-keys", "-T", "prefix", key)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, binary) || !strings.Contains(out, "#{client_name}") {
			t.Fatalf("binding for %s lost the full path or format: %s", key, out)
		}
	}
}
