package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// maxSlugLength caps a slugified name so a workspace/session path segment
// stays well inside typical filesystem path-length limits even after being
// joined with a root, a sibling slug, and a member repo's basename.
const maxSlugLength = 64

// Slugify converts an arbitrary display name into a conservative,
// filesystem- and git-ref-safe slug: lowercase ASCII letters and digits, with
// every run of other characters collapsed to a single '-', leading and
// trailing '-' trimmed, and the result capped at maxSlugLength bytes. It
// returns an error when name has no safe characters at all, since an empty
// path segment or branch name is unusable.
func Slugify(name string) (string, error) {
	var b strings.Builder
	prevHyphen := true // suppresses a leading '-'
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen {
			b.WriteByte('-')
			prevHyphen = true
		}
	}

	slug := strings.TrimSuffix(b.String(), "-")
	if len(slug) > maxSlugLength {
		slug = strings.TrimRight(slug[:maxSlugLength], "-")
	}
	if slug == "" {
		return "", fmt.Errorf("name %q has no safe characters after slugifying", name)
	}
	return slug, nil
}

// SessionBranch returns a session's git branch name: the slug of its display
// name, and nothing else. The branch and the session-directory segment
// returned by SessionDir must be derived from exactly the same slug, or a
// name like "Payments Migration" would reach
// `git worktree add -b "Payments Migration"` and fail after the session
// directory has already been created. Callers must pass this value to git,
// never the raw session name.
func SessionBranch(sessionName string) (string, error) {
	slug, err := Slugify(sessionName)
	if err != nil {
		return "", fmt.Errorf("session branch name: %w", err)
	}
	return slug, nil
}

// SessionDir returns the canonical, absolute session directory for a session
// named sessionName inside a workspace named workspaceName, rooted at root:
// root/<workspace-slug>/<session-slug>. root is taken as a parameter rather
// than resolved internally (e.g. via settings.LoadConfig), so this function
// stays pure and table-testable. The same two slugs (in the same order) are
// always produced for the same inputs, so calling this twice with identical
// arguments yields a byte-identical path.
func SessionDir(root, workspaceName, sessionName string) (string, error) {
	workspaceSlug, err := Slugify(workspaceName)
	if err != nil {
		return "", fmt.Errorf("workspace name: %w", err)
	}
	sessionSlug, err := Slugify(sessionName)
	if err != nil {
		return "", fmt.Errorf("session name: %w", err)
	}

	dir := filepath.Join(root, workspaceSlug, sessionSlug)
	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		return "", fmt.Errorf("canonicalize session dir %q: %w", dir, err)
	}
	return canonical, nil
}

// ValidBranchShape is a pure, conservative pre-check for a git branch name:
// non-empty; no leading or trailing '-' or '.'; no ".." anywhere; and no
// control or space characters. It exists so a create-session prompt can
// reject an obviously-illegal name instantly, without shelling out to git.
// It is deliberately conservative, not authoritative: the full ref-name
// grammar (no "~^:?*[\", no "@{", no trailing ".lock", etc.) is enforced by
// `git check-ref-format --branch`, which shells out and therefore lives in
// internal/git alongside every other git invocation, as the authoritative
// pre-flight check immediately before a worktree is actually created.
func ValidBranchShape(name string) error {
	if name == "" {
		return fmt.Errorf("branch name is empty")
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") {
		return fmt.Errorf("branch name %q starts with %q, which git rejects", name, name[:1])
	}
	if strings.HasSuffix(name, "-") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("branch name %q ends with %q, which git rejects", name, name[len(name)-1:])
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("branch name %q contains \"..\", which git rejects", name)
	}
	for _, r := range name {
		if r == ' ' || unicode.IsControl(r) {
			return fmt.Errorf("branch name %q contains a space or control character", name)
		}
	}
	return nil
}

// MemberDirName returns the directory name a member repo's worktree uses
// inside a session directory: filepath.Base of its (canonical) root, the
// same derivation the existing sibling-worktree convention uses
// (internal/ui/model.go's worktreeDest). It rejects a hidden basename (one
// beginning with '.') rather than silently de-dotting it: settings.
// DiscoverRepos deliberately reports repos that are themselves hidden
// directories (e.g. ~/.dotfiles), and a member directory named ".dotfiles"
// is skipped by ripgrep by default, which would silently defeat the entire
// reason this design uses real directories instead of symlinks. Renaming the
// directory out from under the caller would also make it no longer match the
// basename the user recognizes, so the repo is refused instead.
func MemberDirName(repoRoot string) (string, error) {
	base := filepath.Base(filepath.Clean(repoRoot))
	if strings.HasPrefix(base, ".") {
		return "", fmt.Errorf("repo %q has a hidden basename %q, which ripgrep skips by default; rename the directory or choose a different repo", repoRoot, base)
	}
	return base, nil
}

// CheckBasenameCollisions reports an error naming the shared member
// directory name and the two conflicting repo roots when any two entries in
// roots would produce the same MemberDirName. Two member repos sharing a
// basename cannot both be materialised as worktrees inside one session
// directory, so callers should run this check (with the full candidate list,
// existing members plus the one being added) before accepting a new member.
func CheckBasenameCollisions(roots []string) error {
	seen := make(map[string]string, len(roots))
	for _, root := range roots {
		base := filepath.Base(filepath.Clean(root))
		if prior, ok := seen[base]; ok {
			return fmt.Errorf("repos %q and %q both resolve to member directory name %q; rename one so they can coexist in a session", prior, root, base)
		}
		seen[base] = root
	}
	return nil
}
