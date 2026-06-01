// Package git wraps the git CLI for worktree operations used by cogitator.
//
// All returned paths are passed through pathnorm.Canonical so they match the
// canonical form used by tmux @cog_dir and OpenCode SessionView.Directory.
package git

import (
	"bytes"
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
