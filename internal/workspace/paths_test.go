package workspace_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

func TestPathSessionDir_SlugifiesSpacesAndUppercase(t *testing.T) {
	root := t.TempDir()
	// t.TempDir() can itself sit behind a symlink (e.g. macOS /var ->
	// /private/var), so compare against the canonical root, not the raw one.
	canonicalRoot, err := pathnorm.Canonical(root)
	if err != nil {
		t.Fatalf("pathnorm.Canonical(root): %v", err)
	}

	dir, err := workspace.SessionDir(root, "Payments Migration", "Feature X")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("SessionDir returned non-absolute path %q", dir)
	}
	if !strings.HasPrefix(dir, canonicalRoot) {
		t.Fatalf("SessionDir %q is not under canonical root %q", dir, canonicalRoot)
	}
	rel, err := filepath.Rel(canonicalRoot, dir)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	segments := strings.Split(rel, string(filepath.Separator))
	if len(segments) != 2 {
		t.Fatalf("expected 2 path segments under root, got %v", segments)
	}
	for _, seg := range segments {
		if seg != strings.ToLower(seg) {
			t.Errorf("segment %q is not lowercase", seg)
		}
		if strings.Contains(seg, " ") {
			t.Errorf("segment %q contains a space", seg)
		}
	}
	if segments[0] != "payments-migration" {
		t.Errorf("workspace segment = %q, want %q", segments[0], "payments-migration")
	}
	if segments[1] != "feature-x" {
		t.Errorf("session segment = %q, want %q", segments[1], "feature-x")
	}
}

func TestPathSessionDir_Deterministic(t *testing.T) {
	root := t.TempDir()

	first, err := workspace.SessionDir(root, "Payments Migration", "Feature X")
	if err != nil {
		t.Fatalf("SessionDir (first): %v", err)
	}
	second, err := workspace.SessionDir(root, "Payments Migration", "Feature X")
	if err != nil {
		t.Fatalf("SessionDir (second): %v", err)
	}
	if first != second {
		t.Errorf("SessionDir not deterministic: %q != %q", first, second)
	}
}

func TestPathSessionDir_NameSlugifiesEmptyErrors(t *testing.T) {
	root := t.TempDir()

	if _, err := workspace.SessionDir(root, "Payments Migration", "!!!"); err == nil {
		t.Fatal("expected error for a session name that slugifies to empty, got nil")
	}
	if _, err := workspace.SessionDir(root, "###", "Feature X"); err == nil {
		t.Fatal("expected error for a workspace name that slugifies to empty, got nil")
	}
}

func TestPathSlugify_EmptyResultErrors(t *testing.T) {
	if _, err := workspace.Slugify("   ---   "); err == nil {
		t.Fatal("expected error for a name with no safe characters, got nil")
	}
}

func TestPathSessionBranch_MatchesSessionDirSlug(t *testing.T) {
	root := t.TempDir()

	dir, err := workspace.SessionDir(root, "Payments Migration", "Feature X")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	branch, err := workspace.SessionBranch("Feature X")
	if err != nil {
		t.Fatalf("SessionBranch: %v", err)
	}
	if filepath.Base(dir) != branch {
		t.Errorf("session dir leaf %q != branch %q; path and branch must be derivable from each other", filepath.Base(dir), branch)
	}
}

func TestPathValidBranchShape_RejectsLeadingDash(t *testing.T) {
	err := workspace.ValidBranchShape("-feature-x")
	if err == nil {
		t.Fatal("expected error for a leading dash, got nil")
	}
	if !strings.Contains(err.Error(), "-feature-x") {
		t.Errorf("error %q does not name the offending name", err.Error())
	}
}

func TestPathValidBranchShape_RejectsDotDot(t *testing.T) {
	err := workspace.ValidBranchShape("feature..x")
	if err == nil {
		t.Fatal("expected error for \"..\", got nil")
	}
	if !strings.Contains(err.Error(), "feature..x") {
		t.Errorf("error %q does not name the offending name", err.Error())
	}
}

func TestPathValidBranchShape_RejectsControlCharacter(t *testing.T) {
	name := "feature\x07x"
	err := workspace.ValidBranchShape(name)
	if err == nil {
		t.Fatal("expected error for a control character, got nil")
	}
	// %-q escapes the control byte, so compare against the same escaped form
	// rather than the raw name.
	if !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) {
		t.Errorf("error %q does not name the offending name", err.Error())
	}
}

func TestPathValidBranchShape_RejectsSpace(t *testing.T) {
	if err := workspace.ValidBranchShape("feature x"); err == nil {
		t.Fatal("expected error for a space, got nil")
	}
}

func TestPathValidBranchShape_RejectsTrailingDot(t *testing.T) {
	if err := workspace.ValidBranchShape("feature-x."); err == nil {
		t.Fatal("expected error for a trailing dot, got nil")
	}
}

func TestPathValidBranchShape_AcceptsSlug(t *testing.T) {
	slug, err := workspace.Slugify("Feature X")
	if err != nil {
		t.Fatalf("Slugify: %v", err)
	}
	if err := workspace.ValidBranchShape(slug); err != nil {
		t.Errorf("ValidBranchShape rejected a valid slug %q: %v", slug, err)
	}
}

func TestPathMemberDirName_RejectsHiddenBasename(t *testing.T) {
	_, err := workspace.MemberDirName("/home/user/.dotfiles")
	if err == nil {
		t.Fatal("expected error for a hidden basename, got nil")
	}
	if !strings.Contains(err.Error(), ".dotfiles") {
		t.Errorf("error %q does not name the offending basename", err.Error())
	}
}

func TestPathMemberDirName_ReturnsBasename(t *testing.T) {
	name, err := workspace.MemberDirName("/home/user/src/cogitator")
	if err != nil {
		t.Fatalf("MemberDirName: %v", err)
	}
	if name != "cogitator" {
		t.Errorf("MemberDirName = %q, want %q", name, "cogitator")
	}
}

func TestPathCheckBasenameCollisions_ReportsSharedBasename(t *testing.T) {
	roots := []string{
		"/home/user/src/cogitator",
		"/home/user/work/cogitator",
	}
	err := workspace.CheckBasenameCollisions(roots)
	if err == nil {
		t.Fatal("expected a collision error, got nil")
	}
	if !strings.Contains(err.Error(), "cogitator") {
		t.Errorf("error %q does not name the shared basename", err.Error())
	}
}

func TestPathCheckBasenameCollisions_NoConflict(t *testing.T) {
	roots := []string{
		"/home/user/src/cogitator",
		"/home/user/src/other-repo",
	}
	if err := workspace.CheckBasenameCollisions(roots); err != nil {
		t.Errorf("unexpected collision error: %v", err)
	}
}
