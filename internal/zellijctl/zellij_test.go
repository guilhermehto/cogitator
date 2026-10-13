package zellijctl

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// fakeRunner answers by action name (args[1]) and records every call.
type fakeRunner struct {
	calls     [][]string
	responses map[string]string
	errs      map[string]error
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, slices.Clone(args))
	if len(args) < 2 || args[0] != "action" {
		return "", errors.New("fake: not an action call")
	}
	return f.responses[args[1]], f.errs[args[1]]
}

func (f *fakeRunner) callsTo(action string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 1 && c[1] == action {
			out = append(out, c)
		}
	}
	return out
}

// newBackend returns a Backend inside a fake session unique to the test, so
// the package-level tab dir memo never leaks between tests.
func newBackend(t *testing.T, r *fakeRunner) Backend {
	t.Helper()
	env := map[string]string{
		"ZELLIJ":              "0",
		"ZELLIJ_SESSION_NAME": "sess-" + t.Name(),
		"ZELLIJ_PANE_ID":      "4",
	}
	return Backend{Runner: r, Getenv: func(k string) string { return env[k] }}
}

type paneFixture struct {
	ID      int
	Plugin  bool
	Exited  bool
	Held    bool
	Tab     int
	Command string
	Cwd     string
}

// panesJSON renders panes in the shape probed from `list-panes -a -j`.
func panesJSON(t *testing.T, panes ...paneFixture) string {
	t.Helper()
	out := make([]map[string]any, 0, len(panes))
	for _, p := range panes {
		m := map[string]any{
			"id":               p.ID,
			"is_plugin":        p.Plugin,
			"is_focused":       false,
			"is_floating":      false,
			"is_suppressed":    false,
			"title":            p.Command,
			"exited":           p.Exited,
			"exit_status":      nil,
			"is_held":          p.Held,
			"tab_id":           p.Tab,
			"tab_position":     p.Tab,
			"tab_name":         "tab",
			"terminal_command": nil,
		}
		if p.Plugin {
			m["plugin_url"] = "tab-bar"
		} else {
			m["pane_command"] = nilIfEmpty(p.Command)
			m["pane_cwd"] = nilIfEmpty(p.Cwd)
		}
		out = append(out, m)
	}

	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func canonicalTempDir(t *testing.T) (raw, canonical string) {
	t.Helper()
	raw = t.TempDir()
	c, err := pathnorm.Canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, c
}

func TestListPanes_TerminalPanesOnlyWithCanonicalCwds(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 1, Plugin: true, Tab: 0},
		paneFixture{ID: 0, Tab: 0, Command: "/bin/zsh", Cwd: raw + "/"},
		paneFixture{ID: 2, Tab: 1, Command: "sh -c echo hi2; sleep 300", Cwd: raw},
		paneFixture{ID: 2, Plugin: true, Tab: 1},
	)}}
	b := newBackend(t, r)

	panes, err := b.ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}

	session := "sess-" + t.Name()
	want := []mux.Pane{
		{PaneID: "terminal_0", WindowID: "0", SessionID: session, SessionName: session, Cwd: canonical, Command: "/bin/zsh", CogDir: canonical},
		{PaneID: "terminal_2", WindowID: "1", SessionID: session, SessionName: session, Cwd: canonical, Command: "sh -c echo hi2; sleep 300", CogDir: canonical},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Errorf("ListPanes =\n%+v\nwant\n%+v", panes, want)
	}
	if got := r.calls[0]; !slices.Equal(got, []string{"action", "list-panes", "-a", "-j"}) {
		t.Errorf("call = %v", got)
	}
}

func TestListDirs_CanonicalTerminalCwds(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 1, Plugin: true},
		paneFixture{ID: 0, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 3, Tab: 2, Exited: true, Held: true},
	)}}

	dirs, err := newBackend(t, r).ListDirs()
	if err != nil {
		t.Fatalf("ListDirs: %v", err)
	}
	if !reflect.DeepEqual(dirs, map[string]bool{canonical: true}) {
		t.Errorf("ListDirs = %v", dirs)
	}
}

func TestFindWindowByDir_PrefersHarnessPane(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	other := t.TempDir()
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 1, Tab: 1, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 2, Tab: 2, Command: "claude", Cwd: other},
		paneFixture{ID: 5, Tab: 3, Command: "opencode --mdns " + raw, Cwd: raw},
	)}}

	got, err := newBackend(t, r).FindWindowByDir(raw)
	if err != nil {
		t.Fatalf("FindWindowByDir: %v", err)
	}
	if got != "3" {
		t.Errorf("FindWindowByDir = %q, want harness tab 3", got)
	}
}

func TestFindWindowByDir_LowestPaneWithoutHarness(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 7, Tab: 4, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 3, Tab: 2, Command: "/bin/bash", Cwd: raw},
		paneFixture{ID: 1, Tab: 1, Command: "claude", Exited: true, Held: true},
	)}}

	got, err := newBackend(t, r).FindWindowByDir(raw)
	if err != nil {
		t.Fatalf("FindWindowByDir: %v", err)
	}
	if got != "2" {
		t.Errorf("FindWindowByDir = %q, want lowest pane's tab 2", got)
	}
}

func TestFindWindowByDir_NotFound(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 1, Tab: 1, Command: "/bin/zsh", Cwd: t.TempDir()},
	)}}

	_, err := newBackend(t, r).FindWindowByDir(raw)
	if !errors.Is(err, mux.ErrWindowNotFound) {
		t.Errorf("err = %v, want ErrWindowNotFound", err)
	}
}

func TestWindowProcessAlive(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 2, Tab: 1, Exited: true, Held: true},
		paneFixture{ID: 3, Tab: 2, Exited: true, Held: true},
		paneFixture{ID: 4, Tab: 2, Command: "/bin/zsh", Cwd: raw},
	)}}
	b := newBackend(t, r)

	alive, err := b.WindowProcessAlive("1")
	if err != nil || alive {
		t.Errorf("held-only tab: alive=%v err=%v, want false nil", alive, err)
	}

	alive, err = b.WindowProcessAlive("2")
	if err != nil || !alive {
		t.Errorf("tab with live pane: alive=%v err=%v, want true nil", alive, err)
	}

	if _, err := b.WindowProcessAlive("9"); !errors.Is(err, mux.ErrWindowNotFound) {
		t.Errorf("missing tab err = %v, want ErrWindowNotFound", err)
	}
}

func TestEnsureWindow_NewTab(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{
		"list-panes": "[]",
		"new-tab":    "7\n",
	}}

	got, err := newBackend(t, r).EnsureWindow(raw+"/.", "repo/branch", []string{"sh", "-c", "echo hi"}, mux.ModeSession)
	if err != nil {
		t.Fatalf("EnsureWindow: %v", err)
	}
	if got != "7" {
		t.Errorf("EnsureWindow = %q, want 7", got)
	}

	calls := r.callsTo("new-tab")
	want := []string{"action", "new-tab", "--name", "repo/branch", "--cwd", canonical, "--", "sh", "-c", "echo hi"}
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Errorf("new-tab calls = %v, want [%v]", calls, want)
	}
}

func TestEnsureWindow_DedupsExistingTab(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 2, Tab: 5, Command: "claude", Cwd: raw},
	)}}

	got, err := newBackend(t, r).EnsureWindow(raw, "repo/branch", []string{"claude"}, mux.ModeWindow)
	if err != nil {
		t.Fatalf("EnsureWindow: %v", err)
	}
	if got != "5" {
		t.Errorf("EnsureWindow = %q, want existing tab 5", got)
	}
	if calls := r.callsTo("new-tab"); len(calls) != 0 {
		t.Errorf("unexpected new-tab calls: %v", calls)
	}
}

func TestEnsureWindow_BadTabID(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": "[]", "new-tab": "oops"}}

	if _, err := newBackend(t, r).EnsureWindow(raw, "n", []string{"sh"}, mux.ModeWindow); err == nil {
		t.Error("expected error for non-numeric tab id")
	}
}

func TestEnsureWindow_EmptyArgv(t *testing.T) {
	r := &fakeRunner{}
	if _, err := newBackend(t, r).EnsureWindow(t.TempDir(), "n", nil, mux.ModeWindow); err == nil {
		t.Error("expected error for empty argv")
	}
}

func TestHeldTab_FoundByDirAndRelaunchedInPlace(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": "[]", "new-tab": "3"}}
	b := newBackend(t, r)

	if _, err := b.EnsureWindow(raw, "repo/branch", []string{"claude"}, mux.ModeWindow); err != nil {
		t.Fatalf("EnsureWindow: %v", err)
	}

	r.responses["list-panes"] = panesJSON(t, paneFixture{ID: 6, Tab: 3, Exited: true, Held: true})

	found, err := b.FindWindowByDir(raw)
	if err != nil || found != "3" {
		t.Fatalf("FindWindowByDir = %q, %v; want 3", found, err)
	}

	if err := b.RelaunchInWindow("3", []string{"claude", "--resume", "x"}); err != nil {
		t.Fatalf("RelaunchInWindow: %v", err)
	}

	want := []string{"action", "new-pane", "--in-place", "--close-replaced-pane", "--pane-id", "terminal_6", "--cwd", canonical, "--", "claude", "--resume", "x"}
	calls := r.callsTo("new-pane")
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Errorf("new-pane calls = %v, want [%v]", calls, want)
	}
}

func TestRelaunchInWindow_PrefersExitedPane(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 2, Tab: 1, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 4, Tab: 1, Exited: true, Held: true},
	)}}

	if err := newBackend(t, r).RelaunchInWindow("1", []string{"codex"}); err != nil {
		t.Fatalf("RelaunchInWindow: %v", err)
	}

	want := []string{"action", "new-pane", "--in-place", "--close-replaced-pane", "--pane-id", "terminal_4", "--cwd", canonical, "--", "codex"}
	calls := r.callsTo("new-pane")
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Errorf("new-pane calls = %v, want [%v]", calls, want)
	}
}

func TestRelaunchInWindow_UnknownCwd(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 4, Tab: 1, Exited: true, Held: true},
	)}}

	if err := newBackend(t, r).RelaunchInWindow("1", []string{"codex"}); err == nil {
		t.Error("expected error when the held pane's dir is unknown")
	}
	if calls := r.callsTo("new-pane"); len(calls) != 0 {
		t.Errorf("unexpected new-pane calls: %v", calls)
	}
}

func TestSelectAndKill(t *testing.T) {
	r := &fakeRunner{}
	b := newBackend(t, r)

	if err := b.Select("4", mux.ModeSession, "client"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if err := b.Kill("4", mux.ModeSession); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	want := [][]string{
		{"action", "go-to-tab-by-id", "4"},
		{"action", "close-tab-by-id", "4"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
}

func TestSelectPane_GoesToTabThenFocuses(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 2, Tab: 1, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 5, Tab: 3, Command: "claude", Cwd: raw},
	)}}
	b := newBackend(t, r)

	if err := b.SelectPane("terminal_5", ""); err != nil {
		t.Fatalf("SelectPane: %v", err)
	}

	want := [][]string{
		{"action", "list-panes", "-a", "-j"},
		{"action", "go-to-tab-by-id", "3"},
		{"action", "focus-pane-id", "terminal_5"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}

	if err := b.SelectPane("terminal_9", ""); err == nil {
		t.Error("expected error for unknown pane")
	}
}

func TestCapture_TabResolvesPreferredPane(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{
		"list-panes": panesJSON(t,
			paneFixture{ID: 2, Tab: 1, Command: "/bin/zsh", Cwd: raw},
			paneFixture{ID: 5, Tab: 1, Command: "omp", Cwd: raw},
		),
		"dump-screen": "one\ntwo\nthree\nfour\n\n   \n\n",
	}}

	got, err := newBackend(t, r).Capture("1", 2)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got != "three\nfour" {
		t.Errorf("Capture = %q, want %q", got, "three\nfour")
	}

	calls := r.callsTo("dump-screen")
	if len(calls) != 1 || !slices.Equal(calls[0], []string{"action", "dump-screen", "-p", "terminal_5"}) {
		t.Errorf("dump-screen calls = %v", calls)
	}
}

func TestCapture_PaneID(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{"dump-screen": "a\nb\n"}}

	got, err := newBackend(t, r).Capture("terminal_8", 10)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got != "a\nb" {
		t.Errorf("Capture = %q", got)
	}
	if !reflect.DeepEqual(r.calls, [][]string{{"action", "dump-screen", "-p", "terminal_8"}}) {
		t.Errorf("calls = %v", r.calls)
	}
}

func TestCapture_MissingTab(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{"list-panes": "[]"}}

	if _, err := newBackend(t, r).Capture("3", 10); !errors.Is(err, mux.ErrWindowNotFound) {
		t.Errorf("err = %v, want ErrWindowNotFound", err)
	}
}

func TestMessageUnsupported(t *testing.T) {
	if err := newBackend(t, &fakeRunner{}).Message("", "hi"); !errors.Is(err, mux.ErrUnsupported) {
		t.Errorf("Message err = %v, want ErrUnsupported", err)
	}
}

func TestSelfPaneID(t *testing.T) {
	if got := newBackend(t, &fakeRunner{}).SelfPaneID(); got != "terminal_4" {
		t.Errorf("SelfPaneID = %q", got)
	}

	b := Backend{Getenv: func(string) string { return "" }}
	if got := b.SelfPaneID(); got != "" {
		t.Errorf("SelfPaneID without env = %q, want empty", got)
	}
}

func TestCurrentLocation(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{"current-tab-info": `{"position":1,"name":"t1","active":true,"other_focused_clients":[1],"tab_id":6}`}}

	loc, err := newBackend(t, r).CurrentLocation()
	if err != nil {
		t.Fatalf("CurrentLocation: %v", err)
	}

	want := mux.Location{WindowID: "6", SessionID: "sess-" + t.Name()}
	if loc != want {
		t.Errorf("CurrentLocation = %+v, want %+v", loc, want)
	}
	if !slices.Equal(r.calls[0], []string{"action", "current-tab-info", "-j"}) {
		t.Errorf("call = %v", r.calls[0])
	}
}

func TestCurrentLocation_NoClient(t *testing.T) {
	r := &fakeRunner{errs: map[string]error{"current-tab-info": errors.New("No active tab found for current client")}}

	loc, err := newBackend(t, r).CurrentLocation()
	if err == nil {
		t.Fatal("expected error")
	}
	if loc.SessionID != "sess-"+t.Name() || loc.WindowID != "" {
		t.Errorf("loc = %+v", loc)
	}
}

func TestAvailable(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"ZELLIJ": "0", "ZELLIJ_SESSION_NAME": "s"}, true},
		{map[string]string{"ZELLIJ": "0"}, false},
		{map[string]string{"ZELLIJ_SESSION_NAME": "s"}, false},
		{map[string]string{}, false},
	}
	for _, c := range cases {
		b := Backend{Getenv: func(k string) string { return c.env[k] }}
		if got := b.Available(); got != c.want {
			t.Errorf("Available(%v) = %v, want %v", c.env, got, c.want)
		}
	}
}

func TestNotAvailable(t *testing.T) {
	r := &fakeRunner{}
	b := Backend{Runner: r, Getenv: func(string) string { return "" }}

	checks := map[string]error{}
	_, checks["FindWindowByDir"] = b.FindWindowByDir("/tmp")
	_, checks["WindowProcessAlive"] = b.WindowProcessAlive("1")
	checks["RelaunchInWindow"] = b.RelaunchInWindow("1", []string{"sh"})
	_, checks["EnsureWindow"] = b.EnsureWindow("/tmp", "n", []string{"sh"}, mux.ModeWindow)
	checks["Select"] = b.Select("1", mux.ModeWindow, "")
	checks["SelectPane"] = b.SelectPane("terminal_1", "")
	checks["Kill"] = b.Kill("1", mux.ModeWindow)
	_, checks["ListDirs"] = b.ListDirs()
	_, checks["ListPanes"] = b.ListPanes()
	_, checks["Capture"] = b.Capture("1", 10)
	_, checks["CurrentLocation"] = b.CurrentLocation()

	for name, err := range checks {
		if !errors.Is(err, mux.ErrNotAvailable) {
			t.Errorf("%s err = %v, want ErrNotAvailable", name, err)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("runner called while unavailable: %v", r.calls)
	}
}

func TestHarnessNames(t *testing.T) {
	names := harnessNames()
	for _, want := range []string{"claude", "codex", "omp", "opencode"} {
		if !slices.Contains(names, want) {
			t.Errorf("harnessNames() = %v, missing %q", names, want)
		}
	}
}

func TestLastLines(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc"},
		{"a\nb\n\n \n", 0, "a\nb"},
		{"\n\n", 3, ""},
		{"a", 5, "a"},
	}
	for _, c := range cases {
		if got := lastLines(c.in, c.n); got != c.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
