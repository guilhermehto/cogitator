package tmuxctl

import (
	"fmt"
	"os"
	"strings"

	"github.com/guilhermehto/cogitator/internal/mux"
)

// Backend implements mux.Backend and mux.Badger over tmux. A zero Runner
// uses DefaultRunner.
type Backend struct {
	Runner Runner
}

var (
	_ mux.Backend = Backend{}
	_ mux.Badger  = Backend{}
)

func (b Backend) runner() Runner {
	if b.Runner == nil {
		return DefaultRunner
	}
	return b.Runner
}

// Name implements mux.Backend.
func (Backend) Name() string { return "tmux" }

// Available implements mux.Backend.
func (Backend) Available() bool { return Available() }

// FindWindowByDir implements mux.Backend.
func (b Backend) FindWindowByDir(dir string) (mux.Target, error) {
	return FindWindowByDirWith(b.runner(), dir)
}

// WindowProcessAlive implements mux.Backend.
func (b Backend) WindowProcessAlive(t mux.Target) (bool, error) {
	return WindowProcessAliveWith(b.runner(), t)
}

// RelaunchInWindow implements mux.Backend.
func (b Backend) RelaunchInWindow(t mux.Target, argv []string) error {
	return RelaunchInWindowWith(b.runner(), t, argv)
}

// EnsureWindow implements mux.Backend.
func (b Backend) EnsureWindow(dir, name string, argv []string, mode mux.LaunchMode) (mux.Target, error) {
	return EnsureWindowModeWith(b.runner(), dir, name, argv, mode)
}

// Select implements mux.Backend. In session mode it switches to the session
// and lets tmux restore its last-active window (so you land where you left
// off); in window mode it focuses the exact tagged window.
func (b Backend) Select(t mux.Target, mode mux.LaunchMode, client string) error {
	if mode == mux.ModeSession {
		return SelectSessionForClientWith(b.runner(), t, client)
	}
	return SelectForClientWith(b.runner(), t, client)
}

// SelectPane implements mux.Backend.
func (b Backend) SelectPane(paneID, client string) error {
	return SelectPaneForClient(b.runner(), paneID, client)
}

// Kill implements mux.Backend: the whole session in session mode, the window
// otherwise.
func (b Backend) Kill(t mux.Target, mode mux.LaunchMode) error {
	if mode == mux.ModeSession {
		return KillSessionWith(b.runner(), t)
	}
	return KillWindowWith(b.runner(), t)
}

// ListDirs implements mux.Backend.
func (b Backend) ListDirs() (map[string]bool, error) {
	return ListCogDirsWith(b.runner())
}

// ListPanes implements mux.Backend.
func (b Backend) ListPanes() ([]mux.Pane, error) {
	return ListPanes(b.runner())
}

// Capture implements mux.Backend.
func (b Backend) Capture(target string, lines int) (string, error) {
	return CapturePane(b.runner(), target, lines)
}

// Message implements mux.Backend.
func (b Backend) Message(client, text string) error {
	return DisplayMessage(b.runner(), client, text)
}

// SelfPaneID implements mux.Backend.
func (Backend) SelfPaneID() string { return os.Getenv("TMUX_PANE") }

// CurrentLocation implements mux.Backend. It is not gated on $TMUX, which
// run-shell and popup environments may lack. Each field is read separately so
// one failing lookup leaves only that field empty.
func (b Backend) CurrentLocation() (mux.Location, error) {
	var loc mux.Location
	var firstErr error
	for _, f := range []struct {
		dst    *string
		format string
	}{
		{&loc.Client, "#{client_name}"},
		{&loc.WindowID, "#{window_id}"},
		{&loc.SessionID, "#{session_id}"},
	} {
		out, err := b.runner().Run("display-message", "-p", f.format)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("tmuxctl: display-message %s: %w", f.format, err)
			}
			continue
		}
		*f.dst = strings.TrimSpace(out)
	}
	return loc, firstErr
}

// ListClientFocus implements mux.Badger.
func (b Backend) ListClientFocus() ([]mux.ClientFocus, error) {
	return ListClientFocus(b.runner())
}

// ReadGlobalOptions implements mux.Badger.
func (b Backend) ReadGlobalOptions(names []string) (map[string]string, error) {
	return ReadGlobalOptions(b.runner(), names)
}

// ApplyOptions implements mux.Badger.
func (b Backend) ApplyOptions(writes []mux.OptionWrite) error {
	return ApplyOptions(b.runner(), writes)
}
