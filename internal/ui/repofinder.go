package ui

// repofinder.go implements the "add repo" flow as an embedded fuzzy finder.
// Pressing 'A' in the sessions pane opens an in-TUI finder (no external
// process): cogitator scans $HOME for git repositories, then the user
// fuzzy-filters the discovered list and presses enter to register one.
//
// Running the finder inside the Bubble Tea event loop — rather than shelling
// out to fzf via tea.ExecProcess — keeps the host terminal under Bubble Tea's
// control. The previous fzf approach suspended and resumed the program around
// an external process, which corrupted the surrounding tmux client (jumping the
// user to a different session) and could crash on resume.

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// repoFinderRoot returns the directory scanned for repositories when the finder
// opens: the user's home directory, falling back to "." when home cannot be
// resolved. It is a var so tests can pin it to a temp tree.
var repoFinderRoot = func() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}

// repoScanMsg carries the result of the background repository scan started when
// the finder opens. repos holds the canonical paths of the git repos found
// under the scanned root that are not already configured; err is set when the
// scan itself failed.
type repoScanMsg struct {
	repos []string
	err   error
}

// repoAddMsg reports the outcome of registering a selected repository.
//
//   - added:                       repoPath was newly appended to the config.
//   - added == false, addErr == nil: the repo was already configured (dup).
//   - addErr != nil:               validation or persistence failed.
type repoAddMsg struct {
	repoPath string
	added    bool
	addErr   error
}

// repoRemoveMsg reports the outcome of untracking a repository.
//
//   - removed:                          repoPath was dropped from the config.
//   - removed == false, removeErr == nil: the repo was not configured (no-op).
//   - removeErr != nil:                 persistence failed.
type repoRemoveMsg struct {
	repoPath  string
	removed   bool
	removeErr error
}

// removeRepoCmd untracks path from the workspace config off the UI goroutine
// (it writes the config file). It only forgets the repo; nothing on disk is
// touched. The result is a repoRemoveMsg.
func removeRepoCmd(path string) tea.Cmd {
	return func() tea.Msg {
		removed, err := workspace.RemoveRepo(path)
		if err != nil {
			return repoRemoveMsg{repoPath: path, removeErr: err}
		}
		return repoRemoveMsg{repoPath: path, removed: removed}
	}
}

// scanReposCmd discovers git repositories under root off the UI goroutine and
// returns a repoScanMsg. Discovery is filesystem-bound, so it must never run
// inline in Update. Repos already present in the config are filtered out so the
// finder only offers something new to add.
func scanReposCmd(root string) tea.Cmd {
	return func() tea.Msg {
		repos, err := workspace.DiscoverRepos(root)
		if err != nil {
			return repoScanMsg{err: err}
		}
		if cfg, cErr := workspace.LoadConfig(); cErr == nil {
			repos = filterConfigured(repos, cfg.Repos)
		}
		return repoScanMsg{repos: repos}
	}
}

// addSelectedRepoCmd validates path as a git work tree and, when valid,
// persists it to the workspace config. It runs off the UI goroutine because it
// shells out to git and writes the config file. The result is a repoAddMsg.
//
// Because the finder only ever offers discovered repositories, the validation
// here is a belt-and-braces re-check (the directory could have been removed
// between scan and selection); on failure it reports addErr rather than the
// old "not a git repo, re-search" dance.
func addSelectedRepoCmd(path string) tea.Cmd {
	return func() tea.Msg {
		repoRoot, err := git.RepoRoot(path)
		if err != nil {
			return repoAddMsg{repoPath: path, addErr: err}
		}
		added, err := workspace.AddRepo(repoRoot)
		if err != nil {
			return repoAddMsg{repoPath: repoRoot, addErr: err}
		}
		return repoAddMsg{repoPath: repoRoot, added: added}
	}
}

// filterConfigured returns the discovered repos that are not already present in
// configured, compared by canonical path. Both sides are canonical
// (DiscoverRepos and LoadConfig each canonicalize), so a plain string match is
// sufficient.
func filterConfigured(discovered []string, configured []workspace.RepoConfig) []string {
	if len(configured) == 0 {
		return discovered
	}
	have := make(map[string]bool, len(configured))
	for _, r := range configured {
		have[r.Path] = true
	}
	out := make([]string, 0, len(discovered))
	for _, d := range discovered {
		if !have[d] {
			out = append(out, d)
		}
	}
	return out
}

// clampIndex constrains i to [0, n-1], returning 0 when n <= 0. It keeps the
// finder cursor valid as the filtered match list grows and shrinks while the
// user types.
func clampIndex(i, n int) int {
	switch {
	case n <= 0:
		return 0
	case i < 0:
		return 0
	case i >= n:
		return n - 1
	default:
		return i
	}
}
