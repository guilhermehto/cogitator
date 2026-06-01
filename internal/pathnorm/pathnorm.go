// Package pathnorm provides a single canonical path form used everywhere
// cogitator compares worktree paths: git worktree output, tmux @cog_dir
// window options, and OpenCode SessionView.Directory.
//
// # Why a normalizer is needed
//
// On macOS, /tmp and /var are symlinks to /private/tmp and /private/var.
// A user may launch opencode from /tmp/wt while git resolves the worktree
// to /private/tmp/wt. Without normalization these two strings never match,
// so the session row would be duplicated or mislabeled.
//
// OpenCode stores the session Directory as the literal CWD string reported
// by the process at launch time (empirically confirmed: the DB contains
// /Users/... paths, not /private/Users/... paths, because /Users is not a
// symlink on macOS). When a worktree is created under /tmp or /var, however,
// the literal CWD and the git-resolved path diverge. Canonical resolves both
// forms to the same real path so all three sources (git, tmux, OpenCode) can
// be compared with a plain string equality check.
//
// # Known minor gap
//
// Case-only mismatches on case-insensitive filesystems (e.g. HFS+) are NOT
// reconciled: Canonical preserves the case it receives. Two paths that differ
// only in case on a case-insensitive FS will produce different canonical
// strings. This is acceptable because git worktrees, tmux options, and
// OpenCode all record the path with the same case as the shell that created
// them, so in practice the mismatch does not arise.
package pathnorm

import (
	"os"
	"path/filepath"
	"strings"
)

// Canonical returns the canonical form of p: symlinks resolved, cleaned, and
// trailing separators stripped. Case is preserved.
//
// If p does not exist, Canonical walks up the path until it finds an existing
// ancestor, resolves symlinks on that ancestor, then re-appends the remaining
// (not-yet-existing) suffix. This ensures that a not-yet-created worktree path
// under a symlinked root (e.g. /tmp/wt/feature before the worktree is created)
// produces the same canonical string as the path after creation.
//
// Canonical never returns an error to the caller for a non-existent path; it
// only returns an error when the OS refuses to stat an existing path for a
// reason other than ENOENT/ENOTDIR.
func Canonical(p string) (string, error) {
	// Resolve the full path first so relative paths and ~ are not an issue.
	p = filepath.Clean(p)

	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		// Happy path: the path exists and all symlinks are resolved.
		return filepath.Clean(resolved), nil
	}

	if !isNotExist(err) {
		// A real OS error (permission denied, etc.) — propagate it.
		return "", err
	}

	// The path (or some component of it) does not exist yet.
	// Walk up to the nearest existing ancestor, resolve symlinks there,
	// then re-append the non-existent suffix.
	return resolveWithMissingLeaf(p)
}

// resolveWithMissingLeaf resolves the nearest existing ancestor of p via
// EvalSymlinks and re-appends the remaining path components.
func resolveWithMissingLeaf(p string) (string, error) {
	// Collect the suffix of non-existent components.
	suffix := ""
	cur := p
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the root without finding an existing ancestor.
			// Return the cleaned original path — best we can do.
			return filepath.Clean(p), nil
		}

		// Build the suffix: prepend the current base to what we have so far.
		base := filepath.Base(cur)
		if suffix == "" {
			suffix = base
		} else {
			suffix = base + string(filepath.Separator) + suffix
		}

		cur = parent

		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			// Found an existing ancestor; re-attach the non-existent suffix.
			return filepath.Clean(filepath.Join(resolved, suffix)), nil
		}
		if !isNotExist(err) {
			return "", err
		}
		// Parent also doesn't exist; keep walking up.
	}
}

// isNotExist reports whether err indicates a path component does not exist.
// filepath.EvalSymlinks wraps os.Lstat errors, so we use os.IsNotExist which
// handles both *PathError and *LinkError.
func isNotExist(err error) bool {
	return os.IsNotExist(err) || isErrNotDir(err)
}

// isErrNotDir reports whether err is an ENOTDIR error, which EvalSymlinks can
// return when a non-directory component appears in the middle of a path.
func isErrNotDir(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "not a directory")
}
