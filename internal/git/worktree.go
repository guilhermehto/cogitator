// Package git wraps the git CLI for worktree operations used by cogitator.
//
// All returned paths are passed through pathnorm.Canonical so they match the
// canonical form used by tmux @cog_dir and OpenCode SessionView.Directory.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// Worktree describes a single git worktree entry.
type Worktree struct {
	// Path is the canonical absolute path to the worktree directory.
	Path string
	// Branch is the short branch name (e.g. "main", "feature/foo").
	// Empty when the worktree is in detached-HEAD state.
	Branch string
}

// ListWorktrees returns all worktrees for the repository rooted at repoPath.
// It runs `git worktree list --porcelain` and parses the output.
// Every returned Worktree.Path is pathnorm.Canonical.
func ListWorktrees(repoPath string) ([]Worktree, error) {
	out, err := runGit(repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreePorcelain(out)
}

// AddWorktree creates a new worktree at dest on a new branch named branch.
// It runs `git worktree add <dest> -b <branch>` from repoPath.
//
// The returned path is pathnorm.Canonical of dest evaluated AFTER creation,
// so it matches the path OpenCode will later report as SessionView.Directory.
//
// Returns a non-nil error (and creates nothing) when branch already exists.
func AddWorktree(repoPath, branch, dest string) (string, error) {
	_, err := runGit(repoPath, "worktree", "add", dest, "-b", branch)
	if err != nil {
		return "", fmt.Errorf("git worktree add: %w", err)
	}

	// Canonicalize the actual on-disk path after creation so the caller gets
	// the same form that OpenCode will report as SessionView.Directory.
	canonical, err := pathnorm.Canonical(dest)
	if err != nil {
		return "", fmt.Errorf("canonicalize worktree path %q: %w", dest, err)
	}
	return canonical, nil
}

// FetchAndAddWorktree fetches branch from the "origin" remote and creates a new
// worktree at dest that checks it out. It runs `git fetch origin <branch>`
// followed by `git worktree add --track -b <branch> <dest> origin/<branch>` from
// repoPath, so the new worktree's local branch tracks the freshly-fetched
// remote branch.
//
// Unlike AddWorktree — which creates a brand-new branch off the current HEAD —
// this checks out a branch that already exists on origin.
//
// The returned path is pathnorm.Canonical of dest evaluated AFTER creation, so
// it matches the path OpenCode will later report as SessionView.Directory.
//
// Returns a non-nil error (and creates nothing) when the fetch fails (e.g. the
// branch does not exist on origin) or when a local branch named branch already
// exists.
func FetchAndAddWorktree(repoPath, branch, dest string) (string, error) {
	// Register the branch in origin's fetch refspec so the fetch below creates
	// refs/remotes/origin/<branch>. A full clone's wildcard refspec already
	// covers every branch (so this is a no-op there), but a single-branch clone
	// only maps its one branch; without this, the fetch updates FETCH_HEAD only
	// and "origin/<branch>" fails to resolve in the worktree add. Registering it
	// is also what lets --track set up @{upstream}, which git validates against
	// the configured refspec.
	if _, err := runGit(repoPath, "remote", "set-branches", "--add", "origin", branch); err != nil {
		return "", fmt.Errorf("git remote set-branches origin %s: %w", branch, err)
	}
	if _, err := runGit(repoPath, "fetch", "origin", branch); err != nil {
		return "", fmt.Errorf("git fetch origin %s: %w", branch, err)
	}
	if _, err := runGit(repoPath, "worktree", "add", "--track", "-b", branch, dest, "origin/"+branch); err != nil {
		return "", fmt.Errorf("git worktree add: %w", err)
	}

	canonical, err := pathnorm.Canonical(dest)
	if err != nil {
		return "", fmt.Errorf("canonicalize worktree path %q: %w", dest, err)
	}
	return canonical, nil
}

// CheckRefFormat validates branch as a legal git branch name by running
// `git check-ref-format --branch <branch>`. This is the authoritative check —
// unlike workspace.ValidBranchShape's conservative pure pre-check, it enforces
// git's full ref-name grammar (no "~^:?*[\", no "@{", no trailing ".lock",
// etc.). It needs no repository context, since ref-name syntax does not
// depend on any particular repo. Returns a non-nil error (wrapping git's
// message) when branch is not a valid branch name.
func CheckRefFormat(branch string) error {
	if _, err := runGit("", "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid branch name %q: %w", branch, err)
	}
	return nil
}

// BranchExists reports whether a local branch named branch already exists in
// the repository rooted at repoPath. It runs `git rev-parse --verify --quiet
// refs/heads/<branch>`, the same probe defaultBranch uses to test for "main"
// and "master".
func BranchExists(repoPath, branch string) bool {
	_, err := runGit(repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// PruneWorktrees removes administrative files for worktrees under repoPath
// whose working directory is gone, by running `git worktree prune`. It is
// best-effort rollback cleanup: callers that already have a primary error to
// report should ignore a non-nil return here rather than let it mask that
// error.
func PruneWorktrees(repoPath string) error {
	if _, err := runGit(repoPath, "worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune: %w", err)
	}
	return nil
}

// DeleteBranch deletes branch in the repository rooted at repoPath, running
// `git branch -D <branch>` (or `-d` when force is false). It is best-effort
// rollback cleanup for callers that need to guarantee a branch is gone even
// when an earlier RemoveWorktree call itself failed and so never reached its
// own branch-delete step; a non-nil return (e.g. branch already gone) should
// not mask a primary error.
func DeleteBranch(repoPath, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, err := runGit(repoPath, "branch", flag, branch); err != nil {
		return fmt.Errorf("git branch %s %s: %w", flag, branch, err)
	}
	return nil
}

// Pull fast-forwards branch in the worktree at worktreePath from origin by
// running `git pull --ff-only --no-tags --autostash origin <branch>` there.
//
// --ff-only keeps the pull non-interactive and side-effect-free: rather than
// creating a merge commit or opening an editor, git returns a non-nil error
// when the branch has diverged. --no-tags avoids fetching tag refs while
// refreshing the selected branch. --autostash lets dirty tracked files survive
// the fast-forward when they reapply cleanly. Callers should surface errors to
// the user. On success it returns a one-line summary of git's output ("Already
// up to date." or the "Updating <range>" fast-forward line) suitable for a
// transient status hint.
func Pull(worktreePath, branch string) (string, error) {
	out, err := runGit(worktreePath, "pull", "--ff-only", "--no-tags", "--autostash", "origin", branch)
	if err != nil {
		return "", fmt.Errorf("git pull: %w", err)
	}
	return firstNonEmptyLine(out), nil
}

// firstNonEmptyLine returns the first non-blank line of s, trimmed of
// surrounding whitespace, or "" when s holds no non-blank line.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// RemoveWorktree removes the worktree at worktreePath belonging to the
// repository rooted at repoPath, then deletes the branch it had checked out.
// It runs `git worktree remove [--force] <worktreePath>` followed by
// `git branch (-d|-D) <branch>`, both from repoPath.
//
// When force is false, git refuses (returning a non-nil error) if the worktree
// has uncommitted or untracked changes, protecting unsaved work, and the branch
// is deleted with `-d`, which only succeeds when the branch is merged. When
// force is true, `--force` removes a dirty worktree (discarding its uncommitted
// changes) and `-D` deletes the branch regardless of merge state. A single
// --force does not override a *locked* worktree (git requires `-f -f` for
// that); such a removal still returns an error.
//
// Deleting the branch is what lets a worktree be recreated under the same name:
// creation runs `git worktree add -b <branch>`, which fails while a branch of
// that name still exists. Branch deletion is best-effort — a detached HEAD has
// no branch (branch == ""), and a branch that is unmerged (force=false) or
// still checked out by another worktree cannot be deleted; none of these fail
// the removal that already succeeded. Only the worktree removal itself can
// return an error.
func RemoveWorktree(repoPath, worktreePath, branch string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)
	if _, err := runGit(repoPath, args...); err != nil {
		return fmt.Errorf("git worktree remove: %w", err)
	}

	// Delete the now-unused branch so its name is free to reuse. Must run
	// after removal: git refuses to delete a branch a live worktree checks out.
	if branch != "" {
		flag := "-d"
		if force {
			flag = "-D"
		}
		_, _ = runGit(repoPath, "branch", flag, branch)
	}
	return nil
}

// MergeState classifies whether a branch's commits are already contained in
// the repository's default branch.
type MergeState int

const (
	// MergeUnknown means the status could not be determined: no default branch
	// was found, the branch name is empty (e.g. detached HEAD), or git failed.
	MergeUnknown MergeState = iota
	// MergeMerged means the branch is fully contained in the default branch —
	// deleting its worktree loses no committed work.
	MergeMerged
	// MergeNotMerged means the branch has commits not present in the default
	// branch — those commits remain on the branch after the worktree is removed.
	MergeNotMerged
)

// BranchMergeStatus reports whether branch in the repository rooted at repoPath
// has been merged into the repository's default branch. It probes "main" then
// "master" for the default branch; if neither local branch exists, or branch is
// empty, it returns (MergeUnknown, "").
//
// The second return value is the default branch the status was computed against
// (e.g. "main"), so callers can phrase a message like "merged into main". It is
// empty when the state is MergeUnknown due to a missing default branch.
//
// Branch refs are shared across all worktrees of a repository, so the result is
// independent of which worktree is checked out where.
func BranchMergeStatus(repoPath, branch string) (MergeState, string) {
	if branch == "" {
		return MergeUnknown, ""
	}
	base := defaultBranch(repoPath)
	if base == "" {
		return MergeUnknown, ""
	}
	if branch == base {
		// The branch is the default branch itself — trivially merged.
		return MergeMerged, base
	}
	merged, err := isAncestor(repoPath, "refs/heads/"+branch, "refs/heads/"+base)
	if err != nil {
		return MergeUnknown, base
	}
	if merged {
		return MergeMerged, base
	}
	return MergeNotMerged, base
}

// defaultBranch returns the first of "main" or "master" that exists as a local
// branch in the repository at repoPath, or "" when neither does.
func defaultBranch(repoPath string) string {
	for _, candidate := range []string{"main", "master"} {
		if _, err := runGit(repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// isAncestor reports whether ancestor is an ancestor of descendant (i.e. fully
// contained in it) using `git merge-base --is-ancestor`. That command exits 0
// when true and 1 when false; any other exit (or failure to run) is returned as
// an error so callers can distinguish "not merged" from "could not determine".
func isAncestor(repoPath, ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = repoPath
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// runGit executes git with the given arguments in dir and returns combined
// stdout+stderr on success, or a wrapped error containing stderr on failure.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}

// parseWorktreePorcelain parses the output of `git worktree list --porcelain`.
//
// The porcelain format emits one blank-line-separated stanza per worktree:
//
//	worktree /abs/path
//	HEAD <sha>
//	branch refs/heads/<name>   (or "detached")
//
// We extract worktree path and branch; HEAD SHA is discarded.
func parseWorktreePorcelain(raw string) ([]Worktree, error) {
	var worktrees []Worktree

	// Split into stanzas on blank lines.
	stanzas := strings.Split(strings.TrimSpace(raw), "\n\n")
	for _, stanza := range stanzas {
		stanza = strings.TrimSpace(stanza)
		if stanza == "" {
			continue
		}

		var wt Worktree
		for _, line := range strings.Split(stanza, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				rawPath := strings.TrimPrefix(line, "worktree ")
				canonical, err := pathnorm.Canonical(rawPath)
				if err != nil {
					return nil, fmt.Errorf("canonicalize worktree path %q: %w", rawPath, err)
				}
				wt.Path = canonical

			case strings.HasPrefix(line, "branch "):
				ref := strings.TrimPrefix(line, "branch ")
				// refs/heads/main → main
				wt.Branch = strings.TrimPrefix(ref, "refs/heads/")
			}
			// "HEAD", "detached", "bare", "locked" lines are intentionally ignored.
		}

		if wt.Path != "" {
			worktrees = append(worktrees, wt)
		}
	}

	return worktrees, nil
}
