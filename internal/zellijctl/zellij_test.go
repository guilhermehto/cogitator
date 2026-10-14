package zellijctl

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// fakeRunner answers by command (the action name of `[--session S] action X
// …`, else the zellij subcommand), preferring a per-session answer, and
// records every call. A session in down fails every action.
type fakeRunner struct {
	mu        sync.Mutex
	calls     [][]string
	responses map[string]string
	errs      map[string]error
	sessions  map[string]map[string]string
	down      map[string]bool
}

func command(args []string) (session, cmd string) {
	if len(args) > 2 && args[0] == "--session" {
		session, args = args[1], args[2:]
	}
	if len(args) > 1 && args[0] == "action" {
		args = args[1:]
	}
	if len(args) == 0 {
		return session, ""
	}
	return session, args[0]
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, slices.Clone(args))
	session, cmd := command(args)
	if f.down[session] {
		return "", errors.New("fake: session " + session + " not found")
	}
	if out, ok := f.sessions[session][cmd]; ok {
		return out, nil
	}
	return f.responses[cmd], f.errs[cmd]
}

func (f *fakeRunner) callsTo(cmd string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if _, got := command(c); got == cmd {
			out = append(out, c)
		}
	}
	return out
}

// mutations are the calls that change zellij state, in order.
func (f *fakeRunner) mutations() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if _, cmd := command(c); !strings.HasPrefix(cmd, "list-") {
			out = append(out, c)
		}
	}
	return out
}

// ownSession is the fake session newBackend runs in.
func ownSession(t *testing.T) string { return "sess-" + t.Name() }

// newBackend returns a Backend inside a fake session named after the test,
// with a fresh package-level tab dir memo so no test sees another's dirs.
func newBackend(t *testing.T, r *fakeRunner) Backend {
	t.Helper()
	tabDirs = &tabDirMemo{dirs: map[string]string{}}
	env := map[string]string{
		"ZELLIJ":              "0",
		"ZELLIJ_SESSION_NAME": ownSession(t),
		"ZELLIJ_PANE_ID":      "4",
	}
	return Backend{Runner: r, Getenv: func(k string) string { return env[k] }}
}

// sessionsList renders `list-sessions -n` output; a name ending in "!" is
// listed as exited.
func sessionsList(names ...string) string {
	var b strings.Builder
	for _, name := range names {
		if exited, ok := strings.CutSuffix(name, "!"); ok {
			b.WriteString(exited + " [Created 3m ago] (EXITED - attach to resurrect)\n")
			continue
		}
		b.WriteString(name + " [Created 1h 2m 3s ago] \n")
	}
	return b.String()
}

type paneFixture struct {
	ID         int
	Plugin     bool
	URL        string
	Exited     bool
	Held       bool
	Suppressed bool
	Tab        int
	TabName    string
	Command    string
	Started    string
	Cwd        string
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
			"is_suppressed":    p.Suppressed,
			"title":            p.Command,
			"exited":           p.Exited,
			"exit_status":      nil,
			"is_held":          p.Held,
			"tab_id":           p.Tab,
			"tab_position":     p.Tab,
			"tab_name":         cmp.Or(p.TabName, fmt.Sprintf("Tab #%d", p.Tab+1)),
			"terminal_command": nilIfEmpty(p.Started),
		}
		if p.Plugin {
			m["plugin_url"] = cmp.Or(p.URL, "tab-bar")
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

func TestSplitAddress_RoundTripsSessionNamesWithColons(t *testing.T) {
	for _, session := range []string{"main", "work:1", "a:b:c", "repo-feat.x_y"} {
		for _, local := range []string{"3", "terminal_12"} {
			gotSession, gotLocal, err := splitAddress(address(session, local))
			if err != nil || gotSession != session || gotLocal != local {
				t.Errorf("splitAddress(address(%q, %q)) = %q, %q, %v", session, local, gotSession, gotLocal, err)
			}
		}
	}

	for _, bad := range []string{"3", ":3", "main:", ""} {
		if _, _, err := splitAddress(bad); err == nil {
			t.Errorf("splitAddress(%q): want error", bad)
		}
	}
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

	own := ownSession(t)
	want := []mux.Pane{
		{PaneID: own + ":terminal_0", WindowID: own + ":0", SessionID: own, SessionName: own, Cwd: canonical, Command: "/bin/zsh", CogDir: canonical, TabNumber: 1},
		{PaneID: own + ":terminal_2", WindowID: own + ":1", SessionID: own, SessionName: own, Cwd: canonical, Command: "sh -c echo hi2; sleep 300", CogDir: canonical, TabNumber: 2},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Errorf("ListPanes =\n%+v\nwant\n%+v", panes, want)
	}

	wantCalls := [][]string{{"list-sessions", "-n"}, {"action", "list-panes", "-a", "-j"}}
	if !reflect.DeepEqual(r.calls, wantCalls) {
		t.Errorf("calls = %v, want %v", r.calls, wantCalls)
	}
}

// The panes are as probed in zellij 0.45.1: a shell, then `zellij run --
// claude` where the claude script execs `sleep`.
func TestListPanes_CommandPaneKeepsTheCommandItWasStartedWith(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 0, Tab: 0, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 1, Tab: 0, Command: "sleep 100000", Started: "claude", Cwd: raw},
		paneFixture{ID: 2, Tab: 0, Command: "claude --resume x", Started: "claude --resume x", Cwd: raw},
		paneFixture{ID: 3, Tab: 0, Started: "claude", Exited: true, Held: true},
	)}}

	panes, err := newBackend(t, r).ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}

	var got []string
	for _, p := range panes {
		got = append(got, p.Command)
	}
	want := []string{"/bin/zsh", "sleep 100000 claude", "claude --resume x", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

func TestListPanes_MergesLiveSessionsSkippingExitedAndVanished(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	own := ownSession(t)
	r := &fakeRunner{
		responses: map[string]string{"list-sessions": sessionsList("work:1", own, "old!", "gone")},
		sessions: map[string]map[string]string{
			"":       {"list-panes": panesJSON(t, paneFixture{ID: 4, Tab: 0, Command: "cogitator", Cwd: raw})},
			"work:1": {"list-panes": panesJSON(t, paneFixture{ID: 1, Tab: 2, Command: "claude", Cwd: raw})},
			"old":    {"list-panes": panesJSON(t, paneFixture{ID: 9, Tab: 0, Command: "claude", Cwd: raw})},
		},
		down: map[string]bool{"gone": true},
	}

	panes, err := newBackend(t, r).ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}

	want := []mux.Pane{
		{PaneID: own + ":terminal_4", WindowID: own + ":0", SessionID: own, SessionName: own, Cwd: canonical, Command: "cogitator", CogDir: canonical, TabNumber: 1},
		{PaneID: "work:1:terminal_1", WindowID: "work:1:2", SessionID: "work:1", SessionName: "work:1", Cwd: canonical, Command: "claude", CogDir: canonical, TabNumber: 3},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Errorf("ListPanes =\n%+v\nwant\n%+v", panes, want)
	}

	for _, c := range r.callsTo("list-panes") {
		if session, _ := command(c); session == "old" || session == own {
			t.Errorf("unexpected list-panes call %v", c)
		}
	}
}

func TestListPanes_KeepsOnlyTabNamesSomeoneChose(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 0, Tab: 0, TabName: "LEX-123", Command: "omp", Cwd: raw},
		paneFixture{ID: 1, Tab: 1, Command: "omp", Cwd: raw},
		paneFixture{ID: 2, Tab: 2, TabName: "Tab #9 notes", Command: "omp", Cwd: raw},
	)}}

	panes, err := newBackend(t, r).ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}

	var got []string
	for _, p := range panes {
		got = append(got, fmt.Sprintf("%s/%d", p.TabName, p.TabNumber))
	}
	want := []string{"LEX-123/1", "/2", "Tab #9 notes/3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tab name/number = %v, want %v", got, want)
	}
}

func TestListPanes_OwnSessionFailureIsAnError(t *testing.T) {
	r := &fakeRunner{errs: map[string]error{"list-panes": errors.New("boom")}}

	if _, err := newBackend(t, r).ListPanes(); err == nil {
		t.Error("expected error when the own session cannot be listed")
	}
}

func TestListDirs_CanonicalTerminalCwds(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	other, otherCanonical := canonicalTempDir(t)
	r := &fakeRunner{
		responses: map[string]string{
			"list-sessions": sessionsList("work"),
			"list-panes": panesJSON(t,
				paneFixture{ID: 1, Plugin: true},
				paneFixture{ID: 0, Command: "/bin/zsh", Cwd: raw},
				paneFixture{ID: 3, Tab: 2, Exited: true, Held: true},
			),
		},
		sessions: map[string]map[string]string{"work": {"list-panes": panesJSON(t, paneFixture{ID: 1, Command: "codex", Cwd: other})}},
	}

	dirs, err := newBackend(t, r).ListDirs()
	if err != nil {
		t.Fatalf("ListDirs: %v", err)
	}
	if !reflect.DeepEqual(dirs, map[string]bool{canonical: true, otherCanonical: true}) {
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
	if want := mux.Target(ownSession(t) + ":3"); got != want {
		t.Errorf("FindWindowByDir = %q, want harness tab %q", got, want)
	}
}

func TestFindWindowByDir_PrefersPaneStartedAsHarnessOverShell(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 0, Tab: 0, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 1, Tab: 1, Command: "sleep 100000", Started: "claude", Cwd: raw},
	)}}

	got, err := newBackend(t, r).FindWindowByDir(raw)
	if err != nil {
		t.Fatalf("FindWindowByDir: %v", err)
	}
	if want := mux.Target(ownSession(t) + ":1"); got != want {
		t.Errorf("FindWindowByDir = %q, want harness tab %q", got, want)
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
	if want := mux.Target(ownSession(t) + ":2"); got != want {
		t.Errorf("FindWindowByDir = %q, want lowest pane's tab %q", got, want)
	}
}

func TestFindWindowByDir_FindsHarnessInOtherSession(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{
		responses: map[string]string{
			"list-sessions": sessionsList("repo-feat"),
			"list-panes":    panesJSON(t, paneFixture{ID: 1, Tab: 1, Command: "/bin/zsh", Cwd: raw}),
		},
		sessions: map[string]map[string]string{"repo-feat": {"list-panes": panesJSON(t, paneFixture{ID: 1, Tab: 0, Command: "claude", Cwd: raw})}},
	}

	got, err := newBackend(t, r).FindWindowByDir(raw)
	if err != nil {
		t.Fatalf("FindWindowByDir: %v", err)
	}
	if got != "repo-feat:0" {
		t.Errorf("FindWindowByDir = %q, want repo-feat:0", got)
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
	own := ownSession(t)

	alive, err := b.WindowProcessAlive(mux.Target(own + ":1"))
	if err != nil || alive {
		t.Errorf("held-only tab: alive=%v err=%v, want false nil", alive, err)
	}

	alive, err = b.WindowProcessAlive(mux.Target(own + ":2"))
	if err != nil || !alive {
		t.Errorf("tab with live pane: alive=%v err=%v, want true nil", alive, err)
	}

	if _, err := b.WindowProcessAlive(mux.Target(own + ":9")); !errors.Is(err, mux.ErrWindowNotFound) {
		t.Errorf("missing tab err = %v, want ErrWindowNotFound", err)
	}
}

func TestEnsureWindow_WindowModeOpensTabInOwnSession(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{
		"list-panes": "[]",
		"new-tab":    "7\n",
	}}

	got, err := newBackend(t, r).EnsureWindow(raw+"/.", "repo/branch", []string{"sh", "-c", "echo hi"}, mux.ModeWindow)
	if err != nil {
		t.Fatalf("EnsureWindow: %v", err)
	}
	if want := mux.Target(ownSession(t) + ":7"); got != want {
		t.Errorf("EnsureWindow = %q, want %q", got, want)
	}

	want := [][]string{{"action", "new-tab", "--name", "repo/branch", "--cwd", canonical, "--", "sh", "-c", "echo hi"}}
	if got := r.mutations(); !reflect.DeepEqual(got, want) {
		t.Errorf("mutations = %v, want %v", got, want)
	}
}

func TestEnsureWindow_SessionModeCreatesFreeSessionAndRunsHarnessOverShell(t *testing.T) {
	cases := []struct {
		name     string
		sessions []string
		want     string
	}{
		{"free name", []string{"main"}, "cz-repo-feat-x"},
		{"live name taken", []string{"cz-repo-feat-x"}, "cz-repo-feat-x-2"},
		{"exited names taken", []string{"cz-repo-feat-x!", "cz-repo-feat-x-2!"}, "cz-repo-feat-x-3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, canonical := canonicalTempDir(t)
			r := &fakeRunner{
				responses: map[string]string{"list-sessions": sessionsList(c.sessions...), "list-panes": "[]"},
				// The listing holds the initial shell and the shell that replaces it.
				sessions: map[string]map[string]string{c.want: {"list-panes": panesJSON(t,
					paneFixture{ID: 1, Plugin: true, Tab: 0},
					paneFixture{ID: 3, Plugin: true, URL: "about", Tab: 0},
					paneFixture{ID: 0, Tab: 0, Command: "/bin/zsh", Cwd: t.TempDir()},
					paneFixture{ID: 5, Tab: 0, Command: "/opt/bin/fish", Cwd: canonical},
				)}},
			}
			b := newBackend(t, r)
			env := b.Getenv
			b.Getenv = func(k string) string {
				if k == "SHELL" {
					return "/opt/bin/fish"
				}
				return env(k)
			}

			got, err := b.EnsureWindow(raw, "cz-repo/feat/x", []string{"claude", "--resume"}, mux.ModeSession)
			if err != nil {
				t.Fatalf("EnsureWindow: %v", err)
			}
			if want := mux.Target(c.want + ":0"); got != want {
				t.Errorf("EnsureWindow = %q, want %q", got, want)
			}

			want := [][]string{
				{"attach", "-b", c.want, "options", "--show-startup-tips", "false"},
				{"--session", c.want, "action", "close-pane", "--pane-id", "plugin_3"},
				{"--session", c.want, "action", "new-pane", "--in-place", "--close-replaced-pane", "--pane-id", "terminal_0", "--cwd", canonical, "--", "/opt/bin/fish"},
				{"--session", c.want, "action", "new-pane", "--in-place", "--close-on-exit", "--pane-id", "terminal_5", "--cwd", canonical, "--", "claude", "--resume"},
			}
			if got := r.mutations(); !reflect.DeepEqual(got, want) {
				t.Errorf("mutations =\n%v\nwant\n%v", got, want)
			}
		})
	}
}

// withSocketBudget points the backend's ZELLIJ_SOCKET_DIR at a directory
// that leaves room for session names of exactly budget bytes.
func withSocketBudget(b Backend, budget int) Backend {
	dir := "/" + strings.Repeat("d", socketPathMax()-len("/contract_version_1")-3-budget)
	env := b.Getenv
	b.Getenv = func(k string) string {
		if k == "ZELLIJ_SOCKET_DIR" {
			return dir
		}
		return env(k)
	}
	return b
}

func TestEnsureWindow_SessionModeFitsNameToSocketPath(t *testing.T) {
	cases := []struct {
		name     string
		worktree string
		sessions []string
		want     string
	}{
		{"cut to budget", "cz-repo/feat/improve-setup", nil, "cz-repo-feat"},
		{"suffix stays in budget", "cz-repo/feat/improve-setup", []string{"cz-repo-feat"}, "cz-repo-fe-2"},
		{"no trailing separator", "cz-repo/fea/xyz", nil, "cz-repo-fea"},
		{"short name untouched", "cz/x", nil, "cz-x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := canonicalTempDir(t)
			r := &fakeRunner{
				responses: map[string]string{"list-sessions": sessionsList(c.sessions...), "list-panes": "[]"},
				sessions: map[string]map[string]string{c.want: {"list-panes": panesJSON(t,
					paneFixture{ID: 0, Tab: 0, Command: "/bin/zsh", Cwd: t.TempDir()},
					paneFixture{ID: 1, Tab: 0, Command: "/bin/sh", Cwd: t.TempDir()},
				)}},
			}

			got, err := withSocketBudget(newBackend(t, r), 12).EnsureWindow(raw, c.worktree, []string{"claude"}, mux.ModeSession)
			if err != nil {
				t.Fatalf("EnsureWindow: %v", err)
			}
			if want := mux.Target(c.want + ":0"); got != want {
				t.Errorf("EnsureWindow = %q, want %q", got, want)
			}
		})
	}
}

func TestTruncateName_KeepsRunesWhole(t *testing.T) {
	// é is two bytes: a 7-byte cut would split it.
	if got := truncateName("ab-café", 7); got != "ab-caf" {
		t.Errorf("truncateName = %q, want %q", got, "ab-caf")
	}
}

func TestEnsureWindow_SessionModeFailsWithoutRoomForAName(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-sessions": "", "list-panes": "[]"}}

	_, err := withSocketBudget(newBackend(t, r), 0).EnsureWindow(raw, "repo/x", []string{"claude"}, mux.ModeSession)
	if err == nil || !strings.Contains(err.Error(), "ZELLIJ_SOCKET_DIR") {
		t.Fatalf("EnsureWindow error = %v, want a ZELLIJ_SOCKET_DIR hint", err)
	}
	if got := r.mutations(); len(got) != 0 {
		t.Errorf("mutations = %v, want none", got)
	}
}

func TestEnsureWindow_SessionModeDeletesSessionWhenSwapFails(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{
		responses: map[string]string{"list-panes": panesJSON(t, paneFixture{ID: 0, Tab: 0, Command: "/bin/zsh"})},
		errs:      map[string]error{"new-pane": errors.New("boom")},
	}

	if _, err := newBackend(t, r).EnsureWindow(raw, "repo/swapfail", []string{"claude"}, mux.ModeSession); err == nil {
		t.Fatal("expected error")
	}

	want := []string{"delete-session", "--force", "repo-swapfail"}
	if calls := r.callsTo("delete-session"); len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Errorf("delete-session calls = %v, want [%v]", calls, want)
	}
}

func TestEnsureWindow_DedupsExistingTabInAnySession(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{
		responses: map[string]string{"list-sessions": sessionsList("repo-branch"), "list-panes": "[]"},
		sessions:  map[string]map[string]string{"repo-branch": {"list-panes": panesJSON(t, paneFixture{ID: 2, Tab: 5, Command: "claude", Cwd: raw})}},
	}
	b := newBackend(t, r)

	for _, mode := range []mux.LaunchMode{mux.ModeWindow, mux.ModeSession} {
		got, err := b.EnsureWindow(raw, "repo/branch", []string{"claude"}, mode)
		if err != nil {
			t.Fatalf("EnsureWindow(%v): %v", mode, err)
		}
		if got != "repo-branch:5" {
			t.Errorf("EnsureWindow(%v) = %q, want existing repo-branch:5", mode, got)
		}
	}
	if got := r.mutations(); len(got) != 0 {
		t.Errorf("unexpected mutations: %v", got)
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
	if want := mux.Target(ownSession(t) + ":3"); err != nil || found != want {
		t.Fatalf("FindWindowByDir = %q, %v; want %q", found, err, want)
	}

	if err := b.RelaunchInWindow(found, []string{"claude", "--resume", "x"}); err != nil {
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

	if err := newBackend(t, r).RelaunchInWindow(mux.Target(ownSession(t)+":1"), []string{"codex"}); err != nil {
		t.Fatalf("RelaunchInWindow: %v", err)
	}

	want := []string{"action", "new-pane", "--in-place", "--close-replaced-pane", "--pane-id", "terminal_4", "--cwd", canonical, "--", "codex"}
	calls := r.callsTo("new-pane")
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Errorf("new-pane calls = %v, want [%v]", calls, want)
	}
}

func TestRelaunchInWindow_OtherSessionUsesSessionFlag(t *testing.T) {
	raw, canonical := canonicalTempDir(t)
	r := &fakeRunner{sessions: map[string]map[string]string{"repo-relaunch": {"list-panes": panesJSON(t,
		paneFixture{ID: 1, Tab: 0, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 3, Tab: 0, Exited: true, Held: true},
	)}}}

	if err := newBackend(t, r).RelaunchInWindow("repo-relaunch:0", []string{"codex"}); err != nil {
		t.Fatalf("RelaunchInWindow: %v", err)
	}

	want := [][]string{{"--session", "repo-relaunch", "action", "new-pane", "--in-place", "--close-replaced-pane", "--pane-id", "terminal_3", "--cwd", canonical, "--", "codex"}}
	if got := r.mutations(); !reflect.DeepEqual(got, want) {
		t.Errorf("mutations = %v, want %v", got, want)
	}
}

func TestRelaunchInWindow_UnknownCwd(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 4, Tab: 1, Exited: true, Held: true},
	)}}

	if err := newBackend(t, r).RelaunchInWindow(mux.Target(ownSession(t)+":1"), []string{"codex"}); err == nil {
		t.Error("expected error when the held pane's dir is unknown")
	}
	if calls := r.callsTo("new-pane"); len(calls) != 0 {
		t.Errorf("unexpected new-pane calls: %v", calls)
	}
}

func TestSelect_SameSessionGoesToTabElseSwitchesSession(t *testing.T) {
	own := ownSession(t)
	cases := []struct {
		name   string
		target string
		mode   mux.LaunchMode
		client string
		want   []string
	}{
		{"own session", own + ":4", mux.ModeSession, "", []string{"action", "go-to-tab-by-id", "4"}},
		{"client already in target session", "work:1:2", mux.ModeWindow, "work:1", []string{"--session", "work:1", "action", "go-to-tab-by-id", "2"}},
		{"session mode lands where zellij left it", "work:1:2", mux.ModeSession, "", []string{"action", "switch-session", "work:1"}},
		{"window mode lands on the harness pane", "work:1:2", mux.ModeWindow, "", []string{"action", "switch-session", "work:1", "--pane-id", "terminal_5"}},
		{"client elsewhere returns to own session", own + ":0", mux.ModeWindow, "work:1", []string{"--session", "work:1", "action", "switch-session", own, "--pane-id", "terminal_4"}},
	}
	for _, c := range cases {
		r := &fakeRunner{sessions: map[string]map[string]string{
			"":       {"list-panes": panesJSON(t, paneFixture{ID: 4, Tab: 0, Command: "cogitator"})},
			"work:1": {"list-panes": panesJSON(t, paneFixture{ID: 1, Tab: 2, Command: "/bin/zsh"}, paneFixture{ID: 5, Tab: 2, Command: "claude"})},
		}}

		if err := newBackend(t, r).Select(mux.Target(c.target), c.mode, c.client); err != nil {
			t.Fatalf("%s: Select: %v", c.name, err)
		}
		if got := r.mutations(); !reflect.DeepEqual(got, [][]string{c.want}) {
			t.Errorf("%s: mutations = %v, want [%v]", c.name, got, c.want)
		}
	}
}

func TestSelectPane_GoesToTabThenFocuses(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 2, Tab: 1, Command: "/bin/zsh", Cwd: raw},
		paneFixture{ID: 5, Tab: 3, Command: "claude", Cwd: raw},
	)}}
	b := newBackend(t, r)
	own := ownSession(t)

	if err := b.SelectPane(own+":terminal_5", ""); err != nil {
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

	if err := b.SelectPane(own+":terminal_9", ""); err == nil {
		t.Error("expected error for unknown pane")
	}
}

func TestSelectPane_CrossSessionSwitchesToPane(t *testing.T) {
	r := &fakeRunner{sessions: map[string]map[string]string{
		"":          {"list-panes": panesJSON(t, paneFixture{ID: 4, Tab: 0, Command: "cogitator"})},
		"repo:feat": {"list-panes": panesJSON(t, paneFixture{ID: 5, Tab: 3, Command: "claude"})},
	}}
	b := newBackend(t, r)

	if err := b.SelectPane("repo:feat:terminal_5", ""); err != nil {
		t.Fatalf("SelectPane: %v", err)
	}
	if err := b.SelectPane(ownSession(t)+":terminal_4", "repo:feat"); err != nil {
		t.Fatalf("SelectPane back: %v", err)
	}

	want := [][]string{
		{"action", "switch-session", "repo:feat", "--pane-id", "terminal_5"},
		{"--session", "repo:feat", "action", "switch-session", ownSession(t), "--pane-id", "terminal_4"},
	}
	if got := r.mutations(); !reflect.DeepEqual(got, want) {
		t.Errorf("mutations = %v, want %v", got, want)
	}
}

func TestKill_DeletesWorktreeSessionsButNeverOwn(t *testing.T) {
	own := ownSession(t)
	oneTab := `[{"position":0,"name":"Tab #1","active":true,"tab_id":2}]`
	twoTabs := `[{"position":0,"tab_id":2},{"position":1,"tab_id":7}]`
	cases := []struct {
		name   string
		target string
		mode   mux.LaunchMode
		tabs   string
		want   []string
	}{
		{"session mode deletes a single-tab session", "repo-feat:2", mux.ModeSession, oneTab, []string{"delete-session", "--force", "repo-feat"}},
		{"session mode on a multi-tab session closes only the tab", "repo-feat:2", mux.ModeSession, twoTabs, []string{"--session", "repo-feat", "action", "close-tab-by-id", "2"}},
		{"last tab of another session deletes it", "repo-feat:2", mux.ModeWindow, oneTab, []string{"delete-session", "--force", "repo-feat"}},
		{"one of several tabs closes the tab", "repo-feat:2", mux.ModeWindow, twoTabs, []string{"--session", "repo-feat", "action", "close-tab-by-id", "2"}},
		{"own session in session mode closes the tab", own + ":3", mux.ModeSession, oneTab, []string{"action", "close-tab-by-id", "3"}},
		{"own session in window mode closes the tab", own + ":3", mux.ModeWindow, oneTab, []string{"action", "close-tab-by-id", "3"}},
	}
	for _, c := range cases {
		r := &fakeRunner{responses: map[string]string{"list-tabs": c.tabs}}

		if err := newBackend(t, r).Kill(mux.Target(c.target), c.mode); err != nil {
			t.Fatalf("%s: Kill: %v", c.name, err)
		}
		if got := r.mutations(); !reflect.DeepEqual(got, [][]string{c.want}) {
			t.Errorf("%s: mutations = %v, want [%v]", c.name, got, c.want)
		}
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

	got, err := newBackend(t, r).Capture(ownSession(t)+":1", 2)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got != "three\nfour" {
		t.Errorf("Capture = %q, want %q", got, "three\nfour")
	}

	calls := r.callsTo("dump-screen")
	if len(calls) != 1 || !slices.Equal(calls[0], []string{"action", "dump-screen", "-p", "terminal_5", "--ansi"}) {
		t.Errorf("dump-screen calls = %v", calls)
	}
}

func TestCapture_TabSkipsShellSuspendedUnderPane(t *testing.T) {
	raw, _ := canonicalTempDir(t)
	r := &fakeRunner{responses: map[string]string{"list-panes": panesJSON(t,
		paneFixture{ID: 1, Tab: 2, Command: "/bin/zsh", Cwd: raw, Suppressed: true},
		paneFixture{ID: 2, Tab: 2, Command: "sh", Started: "sh -c make", Cwd: raw},
	)}}

	if _, err := newBackend(t, r).Capture(ownSession(t)+":2", 5); err != nil {
		t.Fatalf("Capture: %v", err)
	}

	calls := r.callsTo("dump-screen")
	if len(calls) != 1 || !slices.Equal(calls[0], []string{"action", "dump-screen", "-p", "terminal_2"}) {
		t.Errorf("dump-screen calls = %v, want the visible terminal_2", calls)
	}
}

func TestCapture_PaneID(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{"dump-screen": "a\nb\n"}}
	b := newBackend(t, r)

	got, err := b.Capture(ownSession(t)+":terminal_8", 10)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got != "a\nb" {
		t.Errorf("Capture = %q", got)
	}

	if _, err := b.Capture("repo:feat:terminal_3", 10); err != nil {
		t.Fatalf("Capture other session: %v", err)
	}

	want := [][]string{
		{"action", "dump-screen", "-p", "terminal_8", "--ansi"},
		{"--session", "repo:feat", "action", "dump-screen", "-p", "terminal_3", "--ansi"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
}

func TestCapture_DropsStyledBlankTail(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{"dump-screen": "a\n\x1b[48;2;1;2;3m   \x1b[0m\n"}}

	got, err := newBackend(t, r).Capture(ownSession(t)+":terminal_8", 10)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got != "a" {
		t.Errorf("Capture = %q, want %q", got, "a")
	}
}

func TestCapture_MissingTab(t *testing.T) {
	r := &fakeRunner{responses: map[string]string{"list-panes": "[]"}}

	if _, err := newBackend(t, r).Capture(ownSession(t)+":3", 10); !errors.Is(err, mux.ErrWindowNotFound) {
		t.Errorf("err = %v, want ErrWindowNotFound", err)
	}
}

func TestMessageUnsupported(t *testing.T) {
	if err := newBackend(t, &fakeRunner{}).Message("", "hi"); !errors.Is(err, mux.ErrUnsupported) {
		t.Errorf("Message err = %v, want ErrUnsupported", err)
	}
}

func TestSelfPaneID(t *testing.T) {
	if got, want := newBackend(t, &fakeRunner{}).SelfPaneID(), ownSession(t)+":terminal_4"; got != want {
		t.Errorf("SelfPaneID = %q, want %q", got, want)
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

	own := ownSession(t)
	want := mux.Location{Client: own, WindowID: own + ":6", SessionID: own}
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
	own := ownSession(t)
	if loc != (mux.Location{Client: own, SessionID: own}) {
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
	_, checks["WindowProcessAlive"] = b.WindowProcessAlive("s:1")
	checks["RelaunchInWindow"] = b.RelaunchInWindow("s:1", []string{"sh"})
	_, checks["EnsureWindow"] = b.EnsureWindow("/tmp", "n", []string{"sh"}, mux.ModeWindow)
	checks["Select"] = b.Select("s:1", mux.ModeWindow, "")
	checks["SelectPane"] = b.SelectPane("s:terminal_1", "")
	checks["Kill"] = b.Kill("s:1", mux.ModeWindow)
	_, checks["ListDirs"] = b.ListDirs()
	_, checks["ListPanes"] = b.ListPanes()
	_, checks["Capture"] = b.Capture("s:1", 10)
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
