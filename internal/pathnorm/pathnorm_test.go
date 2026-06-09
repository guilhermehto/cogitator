package pathnorm_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
)

// TestCanonical_TrailingSlash verifies that paths with and without a trailing
// separator canonicalize to the same string.
func TestCanonical_TrailingSlash(t *testing.T) {
	dir := t.TempDir()

	withSlash := dir + string(filepath.Separator)
	without := dir

	a, err := pathnorm.Canonical(withSlash)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", withSlash, err)
	}
	b, err := pathnorm.Canonical(without)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", without, err)
	}
	if a != b {
		t.Errorf("trailing-slash mismatch: %q != %q", a, b)
	}
	if a == "" {
		t.Error("canonical path must not be empty")
	}
}

// TestCanonical_Clean verifies that double separators and dot components are
// collapsed.
func TestCanonical_Clean(t *testing.T) {
	dir := t.TempDir()

	// Build a dirty path: dir + "/." + "/" + base
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	dirty := dir + string(filepath.Separator) + "." + string(filepath.Separator) + "sub"
	got, err := pathnorm.Canonical(dirty)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", dirty, err)
	}
	want, err := pathnorm.Canonical(sub)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", sub, err)
	}
	if got != want {
		t.Errorf("clean mismatch: %q != %q", got, want)
	}
}

// TestCanonical_Symlink verifies that two paths pointing to the same real
// directory via different symlinks canonicalize to the same string.
//
// On macOS this also covers the /tmp -> /private/tmp case: if the OS resolves
// the temp dir through a symlink, both the symlinked form and the real form
// must produce the same canonical path.
func TestCanonical_Symlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(filepath.Dir(real), "link-"+filepath.Base(real))

	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create symlink (may need elevated privileges): %v", err)
	}
	t.Cleanup(func() { os.Remove(link) })

	a, err := pathnorm.Canonical(real)
	if err != nil {
		t.Fatalf("Canonical(real=%q): %v", real, err)
	}
	b, err := pathnorm.Canonical(link)
	if err != nil {
		t.Fatalf("Canonical(link=%q): %v", link, err)
	}
	if a != b {
		t.Errorf("symlink mismatch: real=%q link=%q → %q != %q", real, link, a, b)
	}
}

// TestCanonical_MacOSTmpSymlink specifically exercises the macOS /tmp ->
// /private/tmp symlink that is the primary motivation for this package.
func TestCanonical_MacOSTmpSymlink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific /tmp symlink test")
	}

	// /tmp is a symlink to /private/tmp on macOS.
	// Create a real directory under /private/tmp and verify that the /tmp
	// form and the /private/tmp form canonicalize identically.
	real, err := os.MkdirTemp("/private/tmp", "pathnorm-test-*")
	if err != nil {
		t.Skipf("cannot create dir under /private/tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(real) })

	// The /tmp form of the same directory.
	linked := filepath.Join("/tmp", filepath.Base(real))

	a, err := pathnorm.Canonical(real)
	if err != nil {
		t.Fatalf("Canonical(real=%q): %v", real, err)
	}
	b, err := pathnorm.Canonical(linked)
	if err != nil {
		t.Fatalf("Canonical(linked=%q): %v", linked, err)
	}
	if a != b {
		t.Errorf("/tmp symlink mismatch: %q != %q", a, b)
	}
}

// TestCanonical_NonExistentLeafUnderSymlinkedParent verifies the pre-create
// path matching requirement: a not-yet-created worktree path under a symlinked
// root must canonicalize to the same string as the path after creation.
func TestCanonical_NonExistentLeafUnderSymlinkedParent(t *testing.T) {
	// Create: real/ -> link/ (symlink)
	real := t.TempDir()
	link := filepath.Join(filepath.Dir(real), "link-"+filepath.Base(real))
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	t.Cleanup(func() { os.Remove(link) })

	// The worktree leaf does not exist yet.
	leafName := "feature-branch"
	prePath := filepath.Join(link, leafName)   // via symlink, leaf absent
	postPath := filepath.Join(real, leafName)  // via real dir, leaf absent

	// Both must canonicalize to the same string before creation.
	pre, err := pathnorm.Canonical(prePath)
	if err != nil {
		t.Fatalf("Canonical(pre=%q): %v", prePath, err)
	}
	post, err := pathnorm.Canonical(postPath)
	if err != nil {
		t.Fatalf("Canonical(post=%q): %v", postPath, err)
	}
	if pre != post {
		t.Errorf("pre-create mismatch: %q != %q", pre, post)
	}

	// Now create the leaf and verify the post-create canonical matches.
	if err := os.Mkdir(filepath.Join(real, leafName), 0o755); err != nil {
		t.Fatal(err)
	}
	afterCreate, err := pathnorm.Canonical(prePath)
	if err != nil {
		t.Fatalf("Canonical(after-create=%q): %v", prePath, err)
	}
	if afterCreate != pre {
		t.Errorf("post-create mismatch: before=%q after=%q", pre, afterCreate)
	}
}

// TestCanonical_DeepNonExistentPath verifies that a path with multiple
// non-existent components does not error and returns a cleaned path.
func TestCanonical_DeepNonExistentPath(t *testing.T) {
	base := t.TempDir()
	deep := filepath.Join(base, "a", "b", "c", "d")

	got, err := pathnorm.Canonical(deep)
	if err != nil {
		t.Fatalf("Canonical(%q): unexpected error: %v", deep, err)
	}
	if got == "" {
		t.Error("canonical path must not be empty for non-existent deep path")
	}
	// The result must be a cleaned path (no double separators, no trailing sep).
	if filepath.Clean(got) != got {
		t.Errorf("result is not clean: %q", got)
	}
}

// TestCanonical_CasePreserved verifies that Canonical does not alter the case
// of path components (important for case-sensitive filesystems).
func TestCanonical_CasePreserved(t *testing.T) {
	dir := t.TempDir()
	// Create a subdirectory with a specific case.
	sub := filepath.Join(dir, "MyDir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := pathnorm.Canonical(sub)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", sub, err)
	}
	// The base name must be preserved as-is (not lowercased).
	if filepath.Base(got) != "MyDir" {
		t.Errorf("case not preserved: got base %q, want %q", filepath.Base(got), "MyDir")
	}
}
