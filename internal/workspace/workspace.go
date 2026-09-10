// Package workspace manages named bundles of git repos ("workspaces") and the
// per-session worktree bundles built from them. A workspace holds an ordered
// set of member repos; each of its sessions checks out one git worktree per
// member, all on the same branch, inside a single session directory so one
// agent launched there can traverse every repo.
//
// Workspaces and their sessions are persisted in workspaces.json beside
// config.json (see store.go) rather than under $XDG_STATE_HOME, because
// membership is durable user-editable configuration and the session records
// are the only index of the real on-disk worktrees — unlike the roster in
// internal/settings, they cannot be recomputed from a scan.
//
// No import of bubbletea or internal/ui is permitted in this package.
package workspace

// MemberRepo is one repository that belongs to a Workspace.
type MemberRepo struct {
	// Path is the canonical absolute path to the repository root.
	Path string `json:"path"`
	// Missing is true when Path was not found on disk at load time. Set by
	// Store.LoadWorkspaces; never persisted.
	Missing bool `json:"-"`
}

// SessionMember pairs one Workspace member repo with the worktree checked out
// for it inside a Session's directory.
type SessionMember struct {
	// RepoPath is the canonical absolute path to the member repo's root.
	RepoPath string `json:"repoPath"`
	// WorktreePath is the canonical absolute path to the git worktree
	// checked out for RepoPath inside the session directory.
	WorktreePath string `json:"worktreePath"`
}

// Session is one unit of work inside a Workspace: a single branch checked out
// as a worktree in every member repo, all living under Dir.
type Session struct {
	// Name is the session's display name. Unique within its Workspace.
	Name string `json:"name"`
	// Dir is the canonical absolute path to the session directory that
	// holds one subdirectory per member worktree.
	Dir string `json:"dir"`
	// Branch is the single branch name checked out in every member's
	// worktree.
	Branch string `json:"branch"`
	// Harness is the harness kind (e.g. "opencode") launched into Dir.
	Harness string `json:"harness"`
	// Members is the ordered set of per-repo worktrees that make up this
	// session.
	Members []SessionMember `json:"members"`
}

// Workspace is a named, ordered bundle of member repos and the sessions built
// from them.
type Workspace struct {
	// Name is the workspace's display name. Unique among all workspaces.
	Name string `json:"name"`
	// Members is the ordered set of repos that belong to this workspace.
	Members []MemberRepo `json:"members"`
	// Sessions is the ordered set of sessions created in this workspace.
	// Session names are unique within a workspace.
	Sessions []Session `json:"sessions"`
}
