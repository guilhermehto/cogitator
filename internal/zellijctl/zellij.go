// Package zellijctl implements mux.Backend over the zellij CLI. Actions on
// cogitator's own zellij session are issued from inside it; other sessions
// are addressed with `--session`. A worktree is a tab of cogitator's session
// (ModeWindow) or a session of its own (ModeSession), identified by its pane
// cwd (zellij has no user options to tag tabs with).
//
// No import of bubbletea or internal/ui is permitted in this package.
package zellijctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// Runner executes zellij with args (e.g. ["action", "list-panes", "-a", "-j"])
// and returns its stdout, or a non-nil error if the command fails.
type Runner interface {
	Run(args ...string) (string, error)
}

// RunnerFunc is a function that implements Runner.
type RunnerFunc func(args ...string) (string, error)

// Run implements Runner.
func (f RunnerFunc) Run(args ...string) (string, error) { return f(args...) }

type execRunner struct{}

// Run executes zellij with the given arguments and returns stdout.
func (execRunner) Run(args ...string) (string, error) {
	out, err := exec.Command("zellij", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("zellij %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("zellij %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// DefaultRunner is the Runner used by a Backend with no Runner set.
var DefaultRunner Runner = execRunner{}

const paneIDPrefix = "terminal_"

// startupWait bounds how long EnsureWindow polls a new session for its shell.
const (
	startupWait = 3 * time.Second
	startupPoll = 100 * time.Millisecond
)

// Backend implements mux.Backend over zellij across every live session.
// Target = "<session>:<tab id>", pane ids "<session>:terminal_<id>"; both
// split on the last ':' since session names may contain one. A client is the
// name of the session the user is viewing, "" meaning cogitator's own. A zero
// Runner uses DefaultRunner; a nil Getenv reads the process environment.
type Backend struct {
	Runner Runner
	Getenv func(string) string
}

var _ mux.Backend = Backend{}

func (b Backend) runner() Runner {
	if b.Runner == nil {
		return DefaultRunner
	}
	return b.Runner
}

func (b Backend) getenv(key string) string {
	if b.Getenv == nil {
		return os.Getenv(key)
	}
	return b.Getenv(key)
}

func (b Backend) session() string { return b.getenv("ZELLIJ_SESSION_NAME") }

func (b Backend) action(args ...string) (string, error) {
	return b.runner().Run(append([]string{"action"}, args...)...)
}

// in runs an action in session: implicitly for cogitator's own session, with
// --session for any other.
func (b Backend) in(session string, args ...string) (string, error) {
	if session == b.session() {
		return b.action(args...)
	}
	return b.runner().Run(append([]string{"--session", session, "action"}, args...)...)
}

// viewing is the session client is looking at; "" is cogitator's own.
func (b Backend) viewing(client string) string {
	if client == "" {
		return b.session()
	}
	return client
}

func address(session, local string) string { return session + ":" + local }

func splitAddress(addr string) (session, local string, err error) {
	i := strings.LastIndex(addr, ":")
	if i <= 0 || i == len(addr)-1 {
		return "", "", fmt.Errorf("zellijctl: malformed address %q", addr)
	}
	return addr[:i], addr[i+1:], nil
}

// Name implements mux.Backend.
func (Backend) Name() string { return "zellij" }

// Available implements mux.Backend: inside a zellij session.
func (b Backend) Available() bool {
	return b.getenv("ZELLIJ") != "" && b.session() != ""
}

// FindWindowByDir implements mux.Backend: the tab of the preferred terminal
// pane whose canonical cwd is dir, in any live session.
func (b Backend) FindWindowByDir(dir string) (mux.Target, error) {
	if !b.Available() {
		return "", mux.ErrNotAvailable
	}

	canonicalDir, err := pathnorm.Canonical(dir)
	if err != nil {
		return "", fmt.Errorf("zellijctl: canonicalize dir %q: %w", dir, err)
	}

	return b.findCanonical(canonicalDir)
}

func (b Backend) findCanonical(canonicalDir string) (mux.Target, error) {
	panes, err := b.panes()
	if err != nil {
		return "", err
	}

	var inDir []pane
	for _, p := range panes {
		if p.cwd == canonicalDir {
			inDir = append(inDir, p)
		}
	}

	best, ok := choose(inDir)
	if !ok {
		return "", mux.ErrWindowNotFound
	}
	return best.target(), nil
}

// WindowProcessAlive implements mux.Backend: false once every terminal pane
// of the tab has exited or is held.
func (b Backend) WindowProcessAlive(t mux.Target) (bool, error) {
	if !b.Available() {
		return false, mux.ErrNotAvailable
	}

	inTab, err := b.tabPanes(t)
	if err != nil {
		return false, err
	}

	for _, p := range inTab {
		if !p.dead {
			return true, nil
		}
	}
	return false, nil
}

// RelaunchInWindow implements mux.Backend by replacing the tab's exited pane
// in place with a new pane running argv in the same dir.
func (b Backend) RelaunchInWindow(t mux.Target, argv []string) error {
	if !b.Available() {
		return mux.ErrNotAvailable
	}
	if len(argv) == 0 {
		return errors.New("zellijctl: argv must not be empty")
	}

	inTab, err := b.tabPanes(t)
	if err != nil {
		return err
	}

	victim := relaunchVictim(inTab)
	if victim.cwd == "" {
		return fmt.Errorf("zellijctl: relaunch in tab %s: pane %s has no known cwd", t, victim.paneID())
	}

	args := []string{"new-pane", "--in-place", "--close-replaced-pane", "--pane-id", victim.localID(), "--cwd", victim.cwd, "--"}
	if _, err := b.in(victim.session, append(args, argv...)...); err != nil {
		return fmt.Errorf("zellijctl: relaunch in tab %s: %w", t, err)
	}
	return nil
}

// EnsureWindow implements mux.Backend: the existing tab for dir in any
// session, else argv in dir as a new tab of cogitator's session named name
// (ModeWindow) or as a new session named after name (ModeSession).
func (b Backend) EnsureWindow(dir, name string, argv []string, mode mux.LaunchMode) (mux.Target, error) {
	if !b.Available() {
		return "", mux.ErrNotAvailable
	}
	if len(argv) == 0 {
		return "", errors.New("zellijctl: argv must not be empty")
	}

	canonicalDir, err := pathnorm.Canonical(dir)
	if err != nil {
		return "", fmt.Errorf("zellijctl: canonicalize dir %q: %w", dir, err)
	}

	existing, err := b.findCanonical(canonicalDir)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, mux.ErrWindowNotFound) {
		return "", err
	}

	if mode == mux.ModeSession {
		return b.newSession(canonicalDir, name, argv)
	}
	return b.newTab(canonicalDir, name, argv)
}

func (b Backend) newTab(canonicalDir, name string, argv []string) (mux.Target, error) {
	args := []string{"new-tab", "--name", name, "--cwd", canonicalDir, "--"}
	out, err := b.action(append(args, argv...)...)
	if err != nil {
		return "", fmt.Errorf("zellijctl: new-tab %s: %w", name, err)
	}

	tab := strings.TrimSpace(out)
	if _, err := strconv.Atoi(tab); err != nil {
		return "", fmt.Errorf("zellijctl: new-tab %s: unexpected tab id %q", name, out)
	}

	tabDirs.set(b.session(), tab, canonicalDir)
	return mux.Target(address(b.session(), tab)), nil
}

// newSession starts a client-less session and swaps its initial shell for
// argv in dir: `attach -b` cannot take options and a command together, and
// its shell starts in cogitator's cwd.
func (b Backend) newSession(canonicalDir, name string, argv []string) (mux.Target, error) {
	session, err := b.freeSessionName(strings.ReplaceAll(name, "/", "-"))
	if err != nil {
		return "", err
	}

	if _, err := b.runner().Run("attach", "-b", session, "options", "--show-startup-tips", "false"); err != nil {
		return "", fmt.Errorf("zellijctl: create session %s: %w", session, err)
	}

	shell, err := b.initialPane(session)
	if err == nil {
		args := []string{"new-pane", "--in-place", "--close-replaced-pane", "--pane-id", shell.localID(), "--cwd", canonicalDir, "--"}
		_, err = b.in(session, append(args, argv...)...)
	}
	if err != nil {
		_ = b.deleteSession(session)
		return "", fmt.Errorf("zellijctl: start session %s: %w", session, err)
	}

	tabDirs.set(session, shell.tab, canonicalDir)
	return shell.target(), nil
}

// freeSessionName is base, else the first free base-2, base-3, …: `attach -b`
// on a live or exited name attaches to or resurrects it instead.
func (b Backend) freeSessionName(base string) (string, error) {
	sessions, err := b.listSessions()
	if err != nil {
		return "", err
	}

	taken := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		taken[s.name] = true
	}

	name := base
	for n := 2; taken[name]; n++ {
		name = base + "-" + strconv.Itoa(n)
	}
	return name, nil
}

// initialPane waits for a new session's shell, which materialises shortly
// after `attach -b` returns together with the About popup zellij opens over
// it regardless of --show-startup-tips; the popup is closed so the session
// opens on the harness.
func (b Backend) initialPane(session string) (pane, error) {
	deadline := time.Now().Add(startupWait)
	for {
		raw, err := b.rawPanes(session)
		if panes := toPanes(session, raw); err == nil && len(panes) > 0 {
			b.closeAbout(session, raw)
			return panes[0], nil
		}
		if time.Now().After(deadline) {
			return pane{}, fmt.Errorf("zellijctl: session %s: no pane after %s", session, startupWait)
		}
		time.Sleep(startupPoll)
	}
}

// closeAbout closes the About popups in raw; one left open is harmless.
func (b Backend) closeAbout(session string, raw []paneJSON) {
	for _, r := range raw {
		if r.IsPlugin && strings.TrimPrefix(r.PluginURL, "zellij:") == "about" {
			_, _ = b.in(session, "close-pane", "--pane-id", "plugin_"+strconv.Itoa(r.ID))
		}
	}
}

// Select implements mux.Backend. Within the client's session it goes to the
// tab; across sessions it switches the client there, landing where zellij
// last left it (ModeSession) or on the tab's preferred pane.
func (b Backend) Select(t mux.Target, mode mux.LaunchMode, client string) error {
	if !b.Available() {
		return mux.ErrNotAvailable
	}

	session, tab, err := splitAddress(string(t))
	if err != nil {
		return err
	}

	viewing := b.viewing(client)
	if session == viewing {
		if _, err := b.in(viewing, "go-to-tab-by-id", tab); err != nil {
			return fmt.Errorf("zellijctl: go-to-tab-by-id %s: %w", t, err)
		}
		return nil
	}

	args := []string{"switch-session", session}
	if mode != mux.ModeSession {
		inTab, err := b.tabPanes(t)
		if err != nil {
			return err
		}
		best, _ := choose(inTab)
		args = append(args, "--pane-id", best.localID())
	}
	if _, err := b.in(viewing, args...); err != nil {
		return fmt.Errorf("zellijctl: switch-session %s: %w", session, err)
	}
	return nil
}

// SelectPane implements mux.Backend: focuses the pane's tab, then the pane,
// switching the client's session first when the pane lives elsewhere.
func (b Backend) SelectPane(paneID, client string) error {
	if !b.Available() {
		return mux.ErrNotAvailable
	}

	session, local, err := splitAddress(paneID)
	if err != nil {
		return err
	}

	panes, err := b.sessionPanes(session)
	if err != nil {
		return err
	}

	tab := ""
	for _, p := range panes {
		if p.localID() == local {
			tab = p.tab
		}
	}
	if tab == "" {
		return fmt.Errorf("zellijctl: pane %s not found", paneID)
	}

	viewing := b.viewing(client)
	if session != viewing {
		if _, err := b.in(viewing, "switch-session", session, "--pane-id", local); err != nil {
			return fmt.Errorf("zellijctl: switch-session %s: %w", session, err)
		}
		return nil
	}

	if _, err := b.in(viewing, "go-to-tab-by-id", tab); err != nil {
		return fmt.Errorf("zellijctl: go-to-tab-by-id %s: %w", tab, err)
	}
	if _, err := b.in(viewing, "focus-pane-id", local); err != nil {
		return fmt.Errorf("zellijctl: focus-pane-id %s: %w", paneID, err)
	}
	return nil
}

// Kill implements mux.Backend: deletes the tab's session when the tab is its
// only one and it is not cogitator's own, else closes the tab. Identity is by
// pane cwd, so a matched tab can sit in a session the user opened; mode never
// widens a kill to the whole of such a session.
func (b Backend) Kill(t mux.Target, _ mux.LaunchMode) error {
	if !b.Available() {
		return mux.ErrNotAvailable
	}

	session, tab, err := splitAddress(string(t))
	if err != nil {
		return err
	}

	if session != b.session() {
		whole, err := b.onlyTab(session, tab)
		if err != nil {
			return err
		}
		if whole {
			return b.deleteSession(session)
		}
	}

	if _, err := b.in(session, "close-tab-by-id", tab); err != nil {
		return fmt.Errorf("zellijctl: close-tab-by-id %s: %w", t, err)
	}
	return nil
}

func (b Backend) onlyTab(session, tab string) (bool, error) {
	out, err := b.in(session, "list-tabs", "-j")
	if err != nil {
		return false, fmt.Errorf("zellijctl: list-tabs %s: %w", session, err)
	}

	var tabs []struct {
		TabID int `json:"tab_id"`
	}
	if err := json.Unmarshal([]byte(out), &tabs); err != nil {
		return false, fmt.Errorf("zellijctl: parse list-tabs %s: %w", session, err)
	}
	return len(tabs) == 1 && strconv.Itoa(tabs[0].TabID) == tab, nil
}

// deleteSession kills and forgets session in one step: closing its last tab
// would leave it resurrectable, and delete-session after kill-session fails
// for a session zellij never serialised.
func (b Backend) deleteSession(session string) error {
	if _, err := b.runner().Run("delete-session", "--force", session); err != nil {
		return fmt.Errorf("zellijctl: delete-session %s: %w", session, err)
	}
	return nil
}

// ListDirs implements mux.Backend: the canonical cwds of terminal panes.
func (b Backend) ListDirs() (map[string]bool, error) {
	if !b.Available() {
		return nil, mux.ErrNotAvailable
	}

	panes, err := b.panes()
	if err != nil {
		return nil, err
	}

	dirs := make(map[string]bool, len(panes))
	for _, p := range panes {
		if p.cwd != "" {
			dirs[p.cwd] = true
		}
	}
	return dirs, nil
}

// ListPanes implements mux.Backend. CogDir is the canonical pane cwd.
func (b Backend) ListPanes() ([]mux.Pane, error) {
	if !b.Available() {
		return nil, mux.ErrNotAvailable
	}

	panes, err := b.panes()
	if err != nil {
		return nil, err
	}

	out := make([]mux.Pane, 0, len(panes))
	for _, p := range panes {
		out = append(out, mux.Pane{
			PaneID:      p.paneID(),
			WindowID:    string(p.target()),
			SessionID:   p.session,
			SessionName: p.session,
			Cwd:         p.cwd,
			Command:     p.command,
			CogDir:      p.cwd,
			TabName:     p.tabName,
			TabNumber:   p.tabNumber,
		})
	}
	return out, nil
}

// Capture implements mux.Backend. target is a pane id or a tab target (its
// preferred pane); the result is the last lines lines of the viewport with
// trailing blank lines dropped.
func (b Backend) Capture(target string, lines int) (string, error) {
	if !b.Available() {
		return "", mux.ErrNotAvailable
	}

	session, local, err := splitAddress(target)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(local, paneIDPrefix) {
		inTab, err := b.tabPanes(mux.Target(target))
		if err != nil {
			return "", err
		}
		best, _ := choose(inTab)
		local = best.localID()
	}

	out, err := b.in(session, "dump-screen", "-p", local)
	if err != nil {
		return "", fmt.Errorf("zellijctl: dump-screen %s: %w", address(session, local), err)
	}
	return lastLines(out, lines), nil
}

// Message implements mux.Backend: zellij cannot show a client message.
func (Backend) Message(_, _ string) error { return mux.ErrUnsupported }

// SelfPaneID implements mux.Backend.
func (b Backend) SelfPaneID() string {
	id := b.getenv("ZELLIJ_PANE_ID")
	if id == "" {
		return ""
	}
	return address(b.session(), paneIDPrefix+id)
}

// CurrentLocation implements mux.Backend: the issuing client's tab in the
// current session, which is also the client.
func (b Backend) CurrentLocation() (mux.Location, error) {
	if !b.Available() {
		return mux.Location{}, mux.ErrNotAvailable
	}

	session := b.session()
	loc := mux.Location{Client: session, SessionID: session}
	out, err := b.action("current-tab-info", "-j")
	if err != nil {
		return loc, fmt.Errorf("zellijctl: current-tab-info: %w", err)
	}

	var info struct {
		TabID *int `json:"tab_id"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return loc, fmt.Errorf("zellijctl: parse current-tab-info: %w", err)
	}
	if info.TabID == nil {
		return loc, errors.New("zellijctl: current-tab-info: no tab_id")
	}

	loc.WindowID = address(session, strconv.Itoa(*info.TabID))
	return loc, nil
}

type sessionInfo struct {
	name   string
	exited bool
}

// listSessions parses `list-sessions -n` lines such as
// "NAME [Created 1s ago] (EXITED - attach to resurrect)".
func (b Backend) listSessions() ([]sessionInfo, error) {
	out, err := b.runner().Run("list-sessions", "-n")
	if err != nil {
		return nil, fmt.Errorf("zellijctl: list-sessions: %w", err)
	}

	var sessions []sessionInfo
	for _, line := range strings.Split(out, "\n") {
		name, rest, ok := strings.Cut(line, " [Created ")
		if !ok {
			continue
		}
		sessions = append(sessions, sessionInfo{name: name, exited: strings.Contains(rest, "(EXITED")})
	}
	return sessions, nil
}

// liveSessions is cogitator's own session, then every other live one.
func (b Backend) liveSessions() ([]string, error) {
	sessions, err := b.listSessions()
	if err != nil {
		return nil, err
	}

	own := b.session()
	names := []string{own}
	for _, s := range sessions {
		if !s.exited && s.name != own {
			names = append(names, s.name)
		}
	}
	return names, nil
}

type paneJSON struct {
	ID              int    `json:"id"`
	IsPlugin        bool   `json:"is_plugin"`
	Exited          bool   `json:"exited"`
	IsHeld          bool   `json:"is_held"`
	TabID           int    `json:"tab_id"`
	TabName         string `json:"tab_name"`
	TabPosition     int    `json:"tab_position"`
	PaneCommand     string `json:"pane_command"`
	TerminalCommand string `json:"terminal_command"`
	PaneCwd         string `json:"pane_cwd"`
	PluginURL       string `json:"plugin_url"`
}

type pane struct {
	session   string
	id        int
	tab       string
	tabName   string
	tabNumber int
	cwd       string
	command   string
	dead      bool
}

func (p pane) localID() string { return paneIDPrefix + strconv.Itoa(p.id) }

func (p pane) paneID() string { return address(p.session, p.localID()) }

func (p pane) target() mux.Target { return mux.Target(address(p.session, p.tab)) }

func (p pane) runsHarness() bool {
	if p.dead {
		return false
	}

	command := strings.ToLower(p.command)
	for _, name := range harnessNames() {
		if strings.Contains(command, name) {
			return true
		}
	}
	return false
}

// panes lists the terminal panes of every live session, cogitator's own
// first. A session that vanished since list-sessions is skipped; only a
// failure to list the own session is an error.
func (b Backend) panes() ([]pane, error) {
	sessions, err := b.liveSessions()
	if err != nil {
		return nil, err
	}

	perSession := make([][]pane, len(sessions))
	errs := make([]error, len(sessions))
	var wg sync.WaitGroup
	for i, session := range sessions {
		wg.Go(func() { perSession[i], errs[i] = b.sessionPanes(session) })
	}
	wg.Wait()

	if errs[0] != nil {
		return nil, errs[0]
	}

	var panes []pane
	for _, ps := range perSession {
		panes = append(panes, ps...)
	}
	return panes, nil
}

func (b Backend) sessionPanes(session string) ([]pane, error) {
	raw, err := b.rawPanes(session)
	if err != nil {
		return nil, err
	}
	return toPanes(session, raw), nil
}

func (b Backend) rawPanes(session string) ([]paneJSON, error) {
	out, err := b.in(session, "list-panes", "-a", "-j")
	if err != nil {
		return nil, fmt.Errorf("zellijctl: list-panes %s: %w", session, err)
	}

	var raw []paneJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("zellijctl: parse list-panes %s: %w", session, err)
	}
	return raw, nil
}

// toPanes keeps session's terminal panes. A held pane reports no cwd, so it
// inherits the dir remembered for its tab.
func toPanes(session string, raw []paneJSON) []pane {
	panes := make([]pane, 0, len(raw))
	for _, r := range raw {
		if r.IsPlugin {
			continue
		}

		p := pane{
			session:   session,
			id:        r.ID,
			tab:       strconv.Itoa(r.TabID),
			tabName:   chosenTabName(r.TabName),
			tabNumber: r.TabPosition + 1,
			cwd:       canonical(r.PaneCwd),
			command:   paneCommand(r),
			dead:      r.Exited || r.IsHeld,
		}
		if p.cwd != "" {
			tabDirs.learn(session, p.tab, p.cwd)
		}
		panes = append(panes, p)
	}

	for i := range panes {
		if panes[i].cwd == "" {
			panes[i].cwd = tabDirs.get(session, panes[i].tab)
		}
	}
	return panes
}

// paneCommand is the pane's foreground command plus, for a live command pane,
// the command it was started with: `zellij run -- claude` may exec into a
// process that no longer names the harness.
func paneCommand(r paneJSON) string {
	started := r.TerminalCommand
	if started == "" || r.Exited || r.IsHeld || strings.Contains(r.PaneCommand, started) {
		return r.PaneCommand
	}
	return strings.TrimSpace(r.PaneCommand + " " + started)
}

// defaultTabName matches the "Tab #N" zellij gives a tab nobody named.
var defaultTabName = regexp.MustCompile(`^Tab #\d+$`)

func chosenTabName(name string) string {
	if defaultTabName.MatchString(name) {
		return ""
	}
	return name
}

func (b Backend) tabPanes(t mux.Target) ([]pane, error) {
	session, tab, err := splitAddress(string(t))
	if err != nil {
		return nil, err
	}

	panes, err := b.sessionPanes(session)
	if err != nil {
		return nil, err
	}

	var inTab []pane
	for _, p := range panes {
		if p.tab == tab {
			inTab = append(inTab, p)
		}
	}
	if len(inTab) == 0 {
		return nil, fmt.Errorf("zellijctl: tab %s: %w", t, mux.ErrWindowNotFound)
	}
	return inTab, nil
}

// choose prefers a live pane running a harness, then the lowest pane id.
func choose(candidates []pane) (pane, bool) {
	var best pane
	found := false
	for _, p := range candidates {
		if !found || betterPane(p, best) {
			best, found = p, true
		}
	}
	return best, found
}

func betterPane(p, best pane) bool {
	pRuns, bestRuns := p.runsHarness(), best.runsHarness()
	if pRuns != bestRuns {
		return pRuns
	}
	return p.id < best.id
}

// relaunchVictim is the tab's lowest exited pane, else its preferred pane.
func relaunchVictim(inTab []pane) pane {
	var victim pane
	found := false
	for _, p := range inTab {
		if p.dead && (!found || p.id < victim.id) {
			victim, found = p, true
		}
	}
	if found {
		return victim
	}

	best, _ := choose(inTab)
	return best
}

// harnessNames are the lowercased harness kinds and launch binaries that
// identify a pane running an agent.
var harnessNames = sync.OnceValue(func() []string {
	var names []string
	for _, kind := range harness.DefaultRegistry.Kinds() {
		names = append(names, strings.ToLower(string(kind)))

		h, err := harness.DefaultRegistry.Get(kind)
		if err != nil {
			continue
		}
		if argv := h.LaunchArgv("", ""); len(argv) > 0 {
			names = append(names, strings.ToLower(filepath.Base(argv[0])))
		}
	}
	return names
})

// tabDirMemo remembers each tab's canonical dir, keyed by session and tab id,
// so a tab whose only pane exited (zellij then reports no cwd) is still found
// by dir and relaunched there. Tab ids are never reused within a session.
type tabDirMemo struct {
	mu   sync.Mutex
	dirs map[string]string
}

var tabDirs = &tabDirMemo{dirs: map[string]string{}}

func tabKey(session, tab string) string { return session + "\x00" + tab }

func (m *tabDirMemo) set(session, tab, dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.dirs[tabKey(session, tab)] = dir
}

func (m *tabDirMemo) learn(session, tab, dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := tabKey(session, tab)
	if _, ok := m.dirs[key]; !ok {
		m.dirs[key] = dir
	}
}

func (m *tabDirMemo) get(session, tab string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.dirs[tabKey(session, tab)]
}

func canonical(path string) string {
	if path == "" {
		return ""
	}

	c, err := pathnorm.Canonical(path)
	if err != nil {
		return path
	}
	return c
}

// lastLines drops trailing blank lines and keeps the last n (all when n <= 0).
func lastLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
