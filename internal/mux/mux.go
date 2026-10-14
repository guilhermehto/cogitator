// Package mux describes what cogitator needs from a terminal multiplexer, so
// the UI, agent projection and feed stay independent of tmux. Backends (e.g.
// internal/tmuxctl) implement Backend; no import of bubbletea, internal/ui or
// a concrete backend is permitted here.
package mux

import "errors"

// ErrNotAvailable is returned when cogitator is not running inside a
// multiplexer the backend can control.
var ErrNotAvailable = errors.New("mux: not inside a terminal multiplexer session")

// ErrWindowNotFound is returned by FindWindowByDir when no window was opened
// for the requested canonical directory.
var ErrWindowNotFound = errors.New("mux: window not found for directory")

// ErrUnsupported is returned when the backend cannot perform an operation
// (e.g. zellij has no client-scoped message).
var ErrUnsupported = errors.New("mux: operation not supported by this multiplexer")

// Target is an opaque, backend-specific address of the place an agent runs
// (tmux "session:index" or "@window").
type Target string

// LaunchMode selects how a new worktree is opened: as a window in the current
// session (ModeWindow) or as a brand-new session (ModeSession).
type LaunchMode int

const (
	// ModeWindow opens worktrees as windows in the current session.
	ModeWindow LaunchMode = iota
	// ModeSession opens worktrees as new sessions.
	ModeSession
)

// Pane is one multiplexer pane with the cogitator state of its window. Cwd is
// canonical when it could be canonicalised, raw otherwise. CogDir is the
// canonical dir cogitator launched in that window. TabName is the window's
// name only when someone chose it; a name the multiplexer generated (tmux
// automatic-rename, zellij "Tab #N") is left empty. TabNumber is the window
// number the multiplexer's own tab bar shows.
type Pane struct {
	PaneID      string
	WindowID    string
	SessionID   string
	SessionName string
	Cwd         string
	Command     string
	CogDir      string
	CogStatus   string
	CogReason   string
	CogSince    string
	TabName     string
	TabNumber   int
}

// ClientFocus is where one attached client is looking.
type ClientFocus struct {
	Client           string
	SessionID        string
	WindowID         string
	ActivityUnixSecs int64
}

// OptionWrite is one set/unset of a @cog_* option. Global writes target the
// server-wide option; otherwise Target is a window id and the write is
// window-scoped.
type OptionWrite struct {
	Global bool
	Target string
	Name   string
	Value  string
	Unset  bool
}

// Location is where the invoking client is.
type Location struct {
	Client    string
	WindowID  string
	SessionID string
}

// Backend is everything cogitator asks of a terminal multiplexer.
type Backend interface {
	Name() string
	Available() bool
	FindWindowByDir(dir string) (Target, error)
	WindowProcessAlive(t Target) (bool, error)
	RelaunchInWindow(t Target, argv []string) error
	EnsureWindow(dir, name string, argv []string, mode LaunchMode) (Target, error)
	Select(t Target, mode LaunchMode, client string) error
	SelectPane(paneID, client string) error
	Kill(t Target, mode LaunchMode) error
	ListDirs() (map[string]bool, error)
	ListPanes() ([]Pane, error)
	Capture(target string, lines int) (string, error)
	Message(client, text string) error
	SelfPaneID() string
	CurrentLocation() (Location, error)
}

// Badger is the optional capability of publishing per-window status badges
// as multiplexer options.
type Badger interface {
	ListClientFocus() ([]ClientFocus, error)
	ReadGlobalOptions(names []string) (map[string]string, error)
	ApplyOptions(writes []OptionWrite) error
}
