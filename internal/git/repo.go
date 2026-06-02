package git

import (
	"fmt"
	"strings"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// RepoRoot validates that path is inside a git work tree and returns the
// canonical absolute path to that work tree's root.
//
// It runs `git rev-parse --show-toplevel` from path. A non-nil error means
// path is not a git repository (or git could not resolve it); the message is
// suitable for surfacing to the user. Selecting any directory inside a repo
// resolves to the repo root, so callers get a consistent root regardless of
// which subdirectory was picked.
//
// The returned root is pathnorm.Canonical so it matches the form stored in the
// workspace config and reported by OpenCode as SessionView.Directory.
func RepoRoot(path string) (string, error) {
	out, err := runGit(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", path)
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", fmt.Errorf("not a git repository: %s", path)
	}
	canonical, err := pathnorm.Canonical(top)
	if err != nil {
		return "", fmt.Errorf("canonicalize repo root %q: %w", top, err)
	}
	return canonical, nil
}
