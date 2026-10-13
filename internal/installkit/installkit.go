// Package installkit holds the file plumbing shared by cogitator's
// multiplexer keybinding installers: locating the cogitator binary to bind,
// and reading and atomically rewriting the user's config without breaking
// dotfile symlinks or changing permissions.
//
// No import of bubbletea or internal/ui is permitted in this package.
package installkit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ResolveBinary picks the cogitator binary a keybinding should run: the
// explicit flag value, else cogitator on PATH, else the running executable.
// Temporary `go run` binaries are rejected because they vanish on exit.
func ResolveBinary(flagValue string, lookPath func(file string) (string, error), executable func() (string, error)) (string, error) {
	path := flagValue

	if path == "" {
		if found, err := lookPath("cogitator"); err == nil {
			path = found
		}
	}

	if path == "" {
		exe, err := executable()
		if err != nil {
			return "", fmt.Errorf("locate cogitator binary: %w", err)
		}
		path = exe
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve binary path %q: %w", path, err)
	}

	if isGoRunBinary(abs) {
		return "", fmt.Errorf("%s is a temporary `go run` binary; install cogitator first or pass --binary", abs)
	}

	return abs, nil
}

func isGoRunBinary(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if strings.HasPrefix(part, "go-build") {
			return true
		}
	}

	return false
}

// ResolveSymlinks returns the file a config path points at so the atomic
// write replaces the dotfiles target, not the link. A missing path is
// returned unchanged.
func ResolveSymlinks(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}

	return resolved, nil
}

// ReadConfig returns the file's content and permission bits; a missing file
// reads as empty with mode 0644.
func ReadConfig(path string) (content string, mode fs.FileMode, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0o644, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", path, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}

	return string(data), info.Mode().Perm(), nil
}

// WriteFileAtomic replaces path with data via a same-directory temp file and
// rename, creating parent directories as needed.
func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
