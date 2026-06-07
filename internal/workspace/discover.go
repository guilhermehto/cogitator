package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// repoScanMaxDepth bounds how deep DiscoverRepos descends below its root.
// Repositories almost always sit within a few levels of a project root
// (~/src/foo, ~/work/team/proj); a cap keeps a stray deep tree from turning the
// scan into a full home-directory crawl.
const repoScanMaxDepth = 6

// repoScanSkipDirs are directory names never descended into during discovery.
// They are large, repo-irrelevant, or dependency caches that would only slow
// the walk and pollute results.
var repoScanSkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"Library":      true, // macOS: enormous, holds no user repos
	".Trash":       true,
}

// DiscoverRepos walks root looking for git repositories — directories that
// contain a ".git" entry (a directory for a normal clone, or a file for a
// linked worktree/submodule). It returns the canonical paths of the
// repositories found, sorted and de-duplicated.
//
// The walk is deliberately bounded so it stays responsive on large home
// directories:
//
//   - it descends at most repoScanMaxDepth levels below root;
//   - it never descends into a discovered repository, so nested worktrees and
//     submodules are not reported as separate top-level repos;
//   - a hidden directory that is itself a repo (e.g. ~/.dotfiles) is reported,
//     but hidden directories that are not repos are not descended into — they
//     are dominated by large caches (~/.cache, ~/.cargo, …) that would blow the
//     scan budget;
//   - it skips a small set of known-noisy directories (node_modules, vendor, …).
//
// Permission and transient IO errors on individual entries are swallowed so a
// single unreadable subtree never aborts the scan; only a hard error on root
// itself is returned.
func DiscoverRepos(root string) ([]string, error) {
	root = filepath.Clean(root)

	var found []string
	seen := map[string]bool{}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			// Unreadable entry: skip its subtree but keep scanning the rest.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}

		if depthBelow(root, path) > repoScanMaxDepth {
			return fs.SkipDir
		}

		isRoot := path == root
		base := filepath.Base(path)

		// Prune known-noisy directories, but never the root itself even when
		// the user pointed us at a skipped path explicitly.
		if !isRoot && repoScanSkipDirs[base] {
			return fs.SkipDir
		}

		// A ".git" entry (dir or file) marks a repository. This check runs
		// before the hidden-directory prune below so a hidden repo such as
		// ~/.dotfiles is still discovered.
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
			canonical, cErr := pathnorm.Canonical(path)
			if cErr != nil {
				canonical = path
			}
			if !seen[canonical] {
				seen[canonical] = true
				found = append(found, canonical)
			}
			// Do not descend into a repo's working tree.
			return fs.SkipDir
		}

		// A hidden directory that is not itself a repo is not descended into:
		// these are dominated by large caches whose contents are not user
		// repositories, and crawling them would blow the scan budget.
		if !isRoot && isHiddenName(base) {
			return fs.SkipDir
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Strings(found)
	return found, nil
}

// depthBelow returns how many path segments path lies below root. root itself
// is depth 0, a direct child is depth 1, and so on. It is robust to the root's
// own separator count (unlike a raw separator tally), so a root of "/" or a
// trailing-slash root both behave sensibly.
func depthBelow(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(os.PathSeparator)) + 1
}

// isHiddenName reports whether a directory base name denotes a hidden directory
// ("." prefix). The current and parent links are not hidden in this sense and
// never reach here because WalkDir does not emit them.
func isHiddenName(base string) bool {
	return len(base) > 1 && base[0] == '.'
}
