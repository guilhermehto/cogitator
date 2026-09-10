package workspace

import (
	"os"
	"time"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
)

// SessionStatus pairs a workspace Session with the live/roster-derived
// status data the Workspaces view renders: lifecycle State, plus the
// last-known Title, SessionID, Provider, Attention, and LastActivity.
type SessionStatus struct {
	Session      Session
	State        settings.RowState
	Title        string
	SessionID    string
	Provider     string
	Attention    state.Attention
	LastActivity time.Time
}

// WorkspaceStatus pairs a Workspace with the merged SessionStatus of each of
// its sessions, in the same order as Workspace.Sessions.
type WorkspaceStatus struct {
	Workspace Workspace
	Sessions  []SessionStatus
}

// liveSessionCandidate holds the best live state.SessionView observed so far
// for a given canonical directory.
type liveSessionCandidate struct {
	view state.SessionView
	live bool // true when view.Source == state.SourceLive
}

// MergeStatus combines the loaded workspaces, the settings roster, and a
// pre-filtered slice of top-level live session views into a per-session
// status ready for the Workspaces view to render.
//
// liveTopLevel must already be filtered to top-level, non-subagent sessions
// by the caller (via internal/ui/visibility.go's shouldHideSubagent) — the
// same contract settings.Merge documents for its liveTopLevel parameter.
// MergeStatus applies its own per-directory collapse over liveTopLevel
// exactly as settings.Merge does: when multiple top-level sessions share a
// canonical directory, state.SourceLive beats state.SourceRecent, and among
// equal sources the newest LastActivity wins.
//
// A session's directory is looked up directly by its own canonical form
// (Session.Dir and the roster's keys are both documented as canonical), so
// no extra path canonicalization is needed on that side; only the live
// views' Directory field is canonicalized before use, mirroring
// settings.Merge's buildLiveByDir.
//
// State is derived as follows, per session:
//   - a matching live view present for the session's directory: State is
//     StateRunning and Title, SessionID, Provider, Attention, and
//     LastActivity are all taken from that live view.
//   - no live view, and the session directory does not exist on disk: State
//     is StateMissing.
//   - no live view, and the session directory exists: State is StateStopped.
//
// A roster entry for the session's directory seeds Title, SessionID,
// Provider, and LastActivity as a baseline in every case; a live view
// overrides those fields, but StateMissing/StateStopped keep the roster's
// values so the view can still show what was last known.
//
// MergeStatus is pure: the only filesystem access is an existence check per
// session directory used to derive StateMissing, so it stays table-testable
// with no git, tmux, or process inspection.
//
// The returned slice preserves the order of workspaces, and each
// WorkspaceStatus.Sessions preserves the order of its Workspace.Sessions.
func MergeStatus(workspaces []Workspace, roster map[string]settings.RosterEntry, liveTopLevel []state.SessionView) []WorkspaceStatus {
	liveByDir := buildLiveSessionsByDir(liveTopLevel)

	out := make([]WorkspaceStatus, 0, len(workspaces))
	for _, ws := range workspaces {
		statuses := make([]SessionStatus, 0, len(ws.Sessions))
		for _, sess := range ws.Sessions {
			statuses = append(statuses, buildSessionStatus(sess, roster, liveByDir))
		}
		out = append(out, WorkspaceStatus{Workspace: ws, Sessions: statuses})
	}
	return out
}

// buildSessionStatus derives the SessionStatus for a single workspace
// Session from the roster and the per-directory live index.
func buildSessionStatus(sess Session, roster map[string]settings.RosterEntry, liveByDir map[string]liveSessionCandidate) SessionStatus {
	st := SessionStatus{Session: sess}

	if entry, ok := roster[sess.Dir]; ok {
		st.Title = entry.Title
		st.SessionID = entry.SessionID
		st.Provider = entry.Provider
		if st.Provider == "" {
			// Older roster entries omit Provider; fall back to Harness.
			st.Provider = entry.Harness
		}
		st.LastActivity = entry.LastActivity
	}

	if cand, ok := liveByDir[sess.Dir]; ok {
		st.Title = cand.view.Title
		st.SessionID = cand.view.SessionID
		st.Provider = string(cand.view.Provider)
		st.Attention = cand.view.Attention
		st.LastActivity = cand.view.LastActivity
		st.State = settings.StateRunning
		return st
	}

	if _, err := os.Stat(sess.Dir); err != nil {
		st.State = settings.StateMissing
		return st
	}

	st.State = settings.StateStopped
	return st
}

// buildLiveSessionsByDir collapses liveTopLevel into a map keyed by canonical
// directory. When multiple sessions share a directory, state.SourceLive
// beats state.SourceRecent; among equal sources, the newest LastActivity
// wins. This mirrors settings.Merge's buildLiveByDir (internal/settings/merge.go).
func buildLiveSessionsByDir(liveTopLevel []state.SessionView) map[string]liveSessionCandidate {
	byDir := make(map[string]liveSessionCandidate, len(liveTopLevel))
	for _, sv := range liveTopLevel {
		if sv.Directory == "" {
			continue
		}
		dir, err := pathnorm.Canonical(sv.Directory)
		if err != nil {
			// Unresolvable path — skip rather than storing a bad key.
			continue
		}
		cand := liveSessionCandidate{view: sv, live: sv.Source == state.SourceLive}
		cur, ok := byDir[dir]
		if !ok {
			byDir[dir] = cand
			continue
		}
		if cand.live && !cur.live {
			byDir[dir] = cand
			continue
		}
		if cand.live == cur.live && sv.LastActivity.After(cur.view.LastActivity) {
			byDir[dir] = cand
		}
	}
	return byDir
}
