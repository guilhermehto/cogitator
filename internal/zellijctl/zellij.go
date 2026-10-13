// Package zellijctl implements mux.Backend over the zellij CLI. Every call is
// a `zellij action …` issued from inside cogitator's own zellij session, and
// each worktree is a tab of that session identified by its pane cwd (zellij
// has no user options to tag tabs with).
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
	"strconv"
	"strings"
	"sync"

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

// Backend implements mux.Backend over zellij: one tab per worktree in the
// current session regardless of LaunchMode, Target = tab id, pane ids
// "terminal_<id>". A zero Runner uses DefaultRunner; a nil Getenv reads the
// process environment.
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

// Name implements mux.Backend.
func (Backend) Name() string { return "zellij" }

// Available implements mux.Backend: inside a zellij session.
func (b Backend) Available() bool {
	return b.getenv("ZELLIJ") != "" && b.session() != ""
}

// FindWindowByDir implements mux.Backend: the tab of the preferred terminal
// pane whose canonical cwd is dir.
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
	return mux.Target(best.tab), nil
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

	args := []string{"new-pane", "--in-place", "--close-replaced-pane", "--pane-id", victim.paneID(), "--cwd", victim.cwd, "--"}
	if _, err := b.action(append(args, argv...)...); err != nil {
		return fmt.Errorf("zellijctl: relaunch in tab %s: %w", t, err)
	}
	return nil
}

// EnsureWindow implements mux.Backend: the existing tab for dir, else a new
// tab named name running argv in dir. mode is ignored (always a tab).
func (b Backend) EnsureWindow(dir, name string, argv []string, _ mux.LaunchMode) (mux.Target, error) {
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
	return mux.Target(tab), nil
}

// Select implements mux.Backend. zellij has no client flag: the action moves
// the client focused on the issuing pane, else the most recently active one.
func (b Backend) Select(t mux.Target, _ mux.LaunchMode, _ string) error {
	if !b.Available() {
		return mux.ErrNotAvailable
	}

	if _, err := b.action("go-to-tab-by-id", string(t)); err != nil {
		return fmt.Errorf("zellijctl: go-to-tab-by-id %s: %w", t, err)
	}
	return nil
}

// SelectPane implements mux.Backend: focuses the pane's tab, then the pane.
func (b Backend) SelectPane(paneID, _ string) error {
	if !b.Available() {
		return mux.ErrNotAvailable
	}

	panes, err := b.panes()
	if err != nil {
		return err
	}

	tab := ""
	for _, p := range panes {
		if p.paneID() == paneID {
			tab = p.tab
		}
	}
	if tab == "" {
		return fmt.Errorf("zellijctl: pane %s not found", paneID)
	}

	if _, err := b.action("go-to-tab-by-id", tab); err != nil {
		return fmt.Errorf("zellijctl: go-to-tab-by-id %s: %w", tab, err)
	}
	if _, err := b.action("focus-pane-id", paneID); err != nil {
		return fmt.Errorf("zellijctl: focus-pane-id %s: %w", paneID, err)
	}
	return nil
}

// Kill implements mux.Backend: closes the tab.
func (b Backend) Kill(t mux.Target, _ mux.LaunchMode) error {
	if !b.Available() {
		return mux.ErrNotAvailable
	}

	if _, err := b.action("close-tab-by-id", string(t)); err != nil {
		return fmt.Errorf("zellijctl: close-tab-by-id %s: %w", t, err)
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

	session := b.session()
	out := make([]mux.Pane, 0, len(panes))
	for _, p := range panes {
		out = append(out, mux.Pane{
			PaneID:      p.paneID(),
			WindowID:    p.tab,
			SessionID:   session,
			SessionName: session,
			Cwd:         p.cwd,
			Command:     p.command,
			CogDir:      p.cwd,
		})
	}
	return out, nil
}

// Capture implements mux.Backend. target is a pane id ("terminal_N") or a tab
// id (its preferred pane); the result is the last lines lines of the viewport
// with trailing blank lines dropped.
func (b Backend) Capture(target string, lines int) (string, error) {
	if !b.Available() {
		return "", mux.ErrNotAvailable
	}

	paneID := target
	if !strings.HasPrefix(target, paneIDPrefix) {
		inTab, err := b.tabPanes(mux.Target(target))
		if err != nil {
			return "", err
		}
		best, _ := choose(inTab)
		paneID = best.paneID()
	}

	out, err := b.action("dump-screen", "-p", paneID)
	if err != nil {
		return "", fmt.Errorf("zellijctl: dump-screen %s: %w", paneID, err)
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
	return paneIDPrefix + id
}

// CurrentLocation implements mux.Backend: the issuing client's tab in the
// current session. Client is always empty (zellij has no client addressing).
func (b Backend) CurrentLocation() (mux.Location, error) {
	if !b.Available() {
		return mux.Location{}, mux.ErrNotAvailable
	}

	loc := mux.Location{SessionID: b.session()}
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

	loc.WindowID = strconv.Itoa(*info.TabID)
	return loc, nil
}

type paneJSON struct {
	ID          int    `json:"id"`
	IsPlugin    bool   `json:"is_plugin"`
	Exited      bool   `json:"exited"`
	IsHeld      bool   `json:"is_held"`
	TabID       int    `json:"tab_id"`
	PaneCommand string `json:"pane_command"`
	PaneCwd     string `json:"pane_cwd"`
}

type pane struct {
	id      int
	tab     string
	cwd     string
	command string
	dead    bool
}

func (p pane) paneID() string { return paneIDPrefix + strconv.Itoa(p.id) }

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

// panes lists the session's terminal panes. A held pane reports no cwd, so
// it inherits the dir remembered for its tab.
func (b Backend) panes() ([]pane, error) {
	out, err := b.action("list-panes", "-a", "-j")
	if err != nil {
		return nil, fmt.Errorf("zellijctl: list-panes: %w", err)
	}

	var raw []paneJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("zellijctl: parse list-panes: %w", err)
	}

	session := b.session()
	panes := make([]pane, 0, len(raw))
	for _, r := range raw {
		if r.IsPlugin {
			continue
		}

		p := pane{
			id:      r.ID,
			tab:     strconv.Itoa(r.TabID),
			cwd:     canonical(r.PaneCwd),
			command: r.PaneCommand,
			dead:    r.Exited || r.IsHeld,
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
	return panes, nil
}

func (b Backend) tabPanes(t mux.Target) ([]pane, error) {
	panes, err := b.panes()
	if err != nil {
		return nil, err
	}

	var inTab []pane
	for _, p := range panes {
		if p.tab == string(t) {
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
