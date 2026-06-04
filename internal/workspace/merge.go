// Package workspace manages durable user configuration and the session roster
// for cogitator. merge.go provides the pure Merge function that combines
// repos, worktrees, roster, live sessions, and tmux window presence into a
// single ordered list of Row values for the UI to render.
//
// No import of bubbletea or internal/ui is permitted in this package.
package workspace

import (
	"time"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/state"
)

// RowState is the lifecycle state of a worktree row.
type RowState string

const (
	// StateRunning: a live session is active in this worktree.
	StateRunning RowState = "running"
	// StateStopped: the worktree is in the roster, the harness has LiveStatus,
	// but no live session is present. The agent was running and has stopped.
	StateStopped RowState = "stopped"
	// StateMissing: the roster has an entry for this worktree but the directory
	// is absent from disk. Resume is disabled until the directory reappears.
	StateMissing RowState = "missing"
	// StateUnknown: the harness does not have LiveStatus, so running vs stopped
	// cannot be determined. A tmux window exists for the dir, suggesting the
	// harness may be alive, but cogitator cannot confirm it.
	StateUnknown RowState = "unknown"
)

// Row is one entry in the merged worktree list. Every field that is a path
// is in canonical form (pathnorm.Canonical).
type Row struct {
	// Repo is the canonical path to the repository root this worktree belongs
	// to. Empty for roster-only rows whose repo is not in the configured list.
	Repo string
	// Worktree is the canonical path to the worktree directory.
	Worktree string
	// Branch is the git branch checked out in this worktree. Empty when
	// unknown (e.g. detached HEAD or roster-only entry).
	Branch string
	// Harness is the harness kind string (e.g. "opencode"). Sourced from the
	// roster when available; empty for disk-only worktrees with no roster entry.
	Harness string
	// Title is the last-known session title. Non-empty for running and stopped
	// rows; empty for empty/missing/unknown rows with no roster entry.
	Title string
	// SessionID is the last-known session identifier. Non-empty when a live or
	// roster session is associated with this worktree.
	SessionID string
	// State is the lifecycle state of this worktree.
	State RowState
	// Attention is the attention classification of the live session. Only
	// meaningful when State == StateRunning; zero value otherwise.
	Attention state.Attention
	// LastActivity is the timestamp of the most recent activity. Sourced from
	// the live session when running, or from the roster when stopped/unknown.
	LastActivity time.Time
}

// liveCandidate holds the best live SessionView for a given canonical dir.
type liveCandidate struct {
	view state.SessionView
	live bool // true when Source == SourceLive
}

// Merge combines repos, worktrees, roster, live sessions, and tmux window
// presence into a single ordered list of Row values.
//
// Parameters:
//   - repos: the ordered list of configured repository roots (from LoadConfig).
//   - worktreesByRepo: map from canonical repo path to its git worktrees
//     (from git.ListWorktrees). Repos with no worktrees should map to nil or
//     an empty slice; they still yield a navigable repo-header row.
//   - roster: map from canonical worktree dir to RosterEntry (from Load).
//   - liveTopLevel: live SessionView slice PRE-FILTERED to top-level sessions
//     only (ParentID == "" and not a subagent). The caller is responsible for
//     applying shouldHideSubagent (internal/ui/visibility.go) before passing
//     this slice; Merge trusts the slice to be subagent-free and does NOT
//     re-implement that filter.
//   - tmuxDirs: set of canonical dirs that have a tmux window tagged with
//     @cog_dir (from tmuxctl). Keys must be canonical paths.
//
// Merge applies its own per-directory collapse over liveTopLevel: when
// multiple top-level sessions share a canonical directory, the one with
// Source==SourceLive wins; among equal sources, the newest LastActivity wins.
// This mirrors the per-SessionID tie-break in internal/state/store.go:683-701
// but operates on directory identity rather than session identity.
//
// Configured-worktree rows are exempt from InactiveHideAfter: an idle-but-
// alive agent in a configured worktree stays State=running, not hidden.
//
// Only worktrees belonging to a configured repo are surfaced. Live sessions
// and roster entries in directories that are not part of a configured repo's
// worktrees are ignored: cogitator no longer discovers and displays arbitrary
// sessions, only those launched into pre-registered repos.
//
// The returned slice is ordered: repos appear in the order given by repos;
// worktrees within a repo appear in the order returned by worktreesByRepo.
func Merge(
	repos []RepoConfig,
	worktreesByRepo map[string][]git.Worktree,
	roster map[string]RosterEntry,
	liveTopLevel []state.SessionView,
	tmuxDirs map[string]bool,
) []Row {
	// Build a per-directory index of the best live session.
	// All keys are canonical paths.
	liveByDir := buildLiveByDir(liveTopLevel)

	// Canonicalize the tmuxDirs keys so lookups are consistent.
	canonTmux := canonicalizeBoolMap(tmuxDirs)

	var rows []Row

	// --- Configured repos ---
	for _, repo := range repos {
		// Canonicalize the repo path used as the map key.
		repoKey, err := pathnorm.Canonical(repo.Path)
		if err != nil {
			repoKey = repo.Path
		}
		wts := worktreesByRepo[repoKey]
		if len(wts) == 0 {
			// Also try the raw path in case the caller used a non-canonical key.
			wts = worktreesByRepo[repo.Path]
		}

		if len(wts) == 0 {
			// A configured repo with zero worktrees still yields a navigable
			// repo-header row so the user can press 'n' to create one.
			rows = append(rows, Row{
				Repo:     repoKey,
				Worktree: repoKey,
				State:    StateStopped,
			})
			continue
		}

		for _, wt := range wts {
			// Canonicalize the worktree path. git.ListWorktrees guarantees
			// canonical paths, but callers (e.g. tests) may pass raw paths.
			dir, err := pathnorm.Canonical(wt.Path)
			if err != nil {
				dir = wt.Path
			}

			row := buildRow(repoKey, dir, wt.Branch, roster, liveByDir, canonTmux)
			rows = append(rows, row)
		}
	}

	return rows
}

// canonicalizeBoolMap returns a new map with all keys passed through
// pathnorm.Canonical. Keys that fail canonicalization are kept as-is.
func canonicalizeBoolMap(m map[string]bool) map[string]bool {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		if c, err := pathnorm.Canonical(k); err == nil {
			out[c] = v
		} else {
			out[k] = v
		}
	}
	return out
}

// buildLiveByDir collapses liveTopLevel into a map keyed by canonical dir.
// When multiple sessions share a dir: SourceLive beats SourceRecent; among
// equal sources, newest LastActivity wins. This mirrors store.go:683-701.
func buildLiveByDir(liveTopLevel []state.SessionView) map[string]liveCandidate {
	byDir := make(map[string]liveCandidate, len(liveTopLevel))
	for _, sv := range liveTopLevel {
		if sv.Directory == "" {
			continue
		}
		dir, err := pathnorm.Canonical(sv.Directory)
		if err != nil {
			// Unresolvable path — skip rather than storing a bad key.
			continue
		}
		cand := liveCandidate{view: sv, live: sv.Source == state.SourceLive}
		cur, ok := byDir[dir]
		if !ok {
			byDir[dir] = cand
			continue
		}
		// Prefer live source over recent.
		if cand.live && !cur.live {
			byDir[dir] = cand
			continue
		}
		// Among equal sources, prefer newer LastActivity.
		if cand.live == cur.live && sv.LastActivity.After(cur.view.LastActivity) {
			byDir[dir] = cand
		}
	}
	return byDir
}

// buildRow constructs a Row for the given worktree dir, consulting the live
// index, roster, and tmux window set to determine the correct RowState.
//
// dir must be a canonical path. liveByDir and tmuxDirs must also use canonical
// keys. The roster map may use non-canonical keys; buildRow searches by
// canonical match when a direct lookup fails.
func buildRow(
	repo string,
	dir string,
	branch string,
	roster map[string]RosterEntry,
	liveByDir map[string]liveCandidate,
	tmuxDirs map[string]bool,
) Row {
	row := Row{
		Repo:     repo,
		Worktree: dir,
		Branch:   branch,
	}

	// Populate from roster if present. Try direct lookup first, then
	// canonical-key fallback for callers that pass non-canonical roster keys.
	rosterEntry, inRoster := rosterLookup(roster, dir)
	if inRoster {
		row.Harness = rosterEntry.Harness
		row.Title = rosterEntry.Title
		row.SessionID = rosterEntry.SessionID
		row.LastActivity = rosterEntry.LastActivity
	}

	// Check for a live session in this dir (liveByDir keys are canonical).
	if cand, ok := liveByDir[dir]; ok {
		// Live session wins: override roster fields with live data.
		row.Title = cand.view.Title
		row.SessionID = cand.view.SessionID
		row.Attention = cand.view.Attention
		row.LastActivity = cand.view.LastActivity
		if row.Harness == "" {
			// No roster entry for this dir; derive harness from the live session's provider.
			if cand.view.Provider != "" {
				row.Harness = string(cand.view.Provider)
			} else {
				row.Harness = "opencode"
			}
		}
		row.State = StateRunning
		return row
	}

	// No live session. Determine state from roster + harness capabilities + tmux.
	if !inRoster {
		// Worktree on disk, no roster, no live → treat as stopped.
		row.State = StateStopped
		return row
	}

	// In roster but not live. Check harness capabilities.
	h, err := harness.DefaultRegistry.Get(harness.Kind(rosterEntry.Harness))
	if err == nil && h.Capabilities().LiveStatus {
		// Harness can report live status and it's not live → stopped.
		row.State = StateStopped
		return row
	}

	// Harness lacks LiveStatus (or is unknown). Check tmux window presence.
	if tmuxDirs[dir] {
		// A tmux window exists for this dir — the harness may be alive but
		// cogitator cannot confirm it.
		row.State = StateUnknown
		return row
	}

	// No live status capability and no tmux window. We cannot determine state.
	// Treat as stopped if the harness is unknown (conservative), but the plan
	// says unknown only when tmux window exists. Without a tmux window and
	// without LiveStatus, we have no signal — render as stopped (best guess).
	row.State = StateStopped
	return row
}

// rosterLookup finds the RosterEntry for the given canonical dir. It first
// tries a direct map lookup, then falls back to canonicalizing each key and
// comparing. The fallback handles callers (e.g. tests) that pass non-canonical
// roster keys.
func rosterLookup(roster map[string]RosterEntry, canonDir string) (RosterEntry, bool) {
	if e, ok := roster[canonDir]; ok {
		return e, true
	}
	// Fallback: canonicalize each key and compare.
	for k, v := range roster {
		if c, err := pathnorm.Canonical(k); err == nil && c == canonDir {
			return v, true
		}
	}
	return RosterEntry{}, false
}
