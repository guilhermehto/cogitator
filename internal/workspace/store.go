package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
)

// workspacesFile is the on-disk JSON representation of the workspace set.
type workspacesFile struct {
	Workspaces []Workspace `json:"workspaces"`
}

// Store is the single writer of workspaces.json. Every mutator takes Store's
// mutex and performs a full load-modify-save cycle, so two callers mutating
// concurrently (e.g. a create-session tea.Cmd and an attach-repo tea.Cmd,
// each its own goroutine) can never silently drop one another's update — the
// same problem internal/settings.Recorder solves for the roster with a
// documented single writer. Construct one Store per process (e.g. in RunTUI)
// and share it.
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore creates a Store backed by workspaces.json in the same directory as
// config.json.
func NewStore() (*Store, error) {
	configPath, err := settings.ConfigPath()
	if err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(filepath.Dir(configPath), "workspaces.json")}, nil
}

// LoadWorkspaces reads workspaces.json and returns the parsed set. If the
// file does not exist, LoadWorkspaces returns an empty slice (no error),
// mirroring settings.LoadConfig's tolerance for a missing file. Each member
// repo path is canonicalized; one absent from disk has Missing set rather
// than failing the load, mirroring settings.RepoConfig.Missing.
func (s *Store) LoadWorkspaces() ([]Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// SaveWorkspaces writes workspaces to workspaces.json atomically (temp file +
// rename), mirroring the pattern in internal/settings/roster.go, so a crash
// or a failed write mid-save never leaves the previous file corrupt.
func (s *Store) SaveWorkspaces(workspaces []Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(workspaces)
}

// AddWorkspace creates an empty workspace named name and persists it. It
// returns an error naming the conflict if a workspace with that name already
// exists.
func (s *Store) AddWorkspace(name string) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	workspaces, err := s.load()
	if err != nil {
		return Workspace{}, err
	}
	if findWorkspaceIndex(workspaces, name) >= 0 {
		return Workspace{}, fmt.Errorf("workspace %q already exists", name)
	}

	ws := Workspace{Name: name}
	workspaces = append(workspaces, ws)
	if err := s.save(workspaces); err != nil {
		return Workspace{}, err
	}
	return ws, nil
}

// RemoveWorkspace deletes the workspace named name and persists the change.
// It returns an error if no such workspace exists.
func (s *Store) RemoveWorkspace(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	workspaces, err := s.load()
	if err != nil {
		return err
	}
	idx := findWorkspaceIndex(workspaces, name)
	if idx < 0 {
		return fmt.Errorf("workspace %q does not exist", name)
	}
	workspaces = append(workspaces[:idx], workspaces[idx+1:]...)
	return s.save(workspaces)
}

// AddSession appends session to the workspace named workspaceName and
// persists the change. It returns an error if the workspace does not exist or
// if a session with the same name already exists in it.
func (s *Store) AddSession(workspaceName string, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	workspaces, err := s.load()
	if err != nil {
		return err
	}
	idx := findWorkspaceIndex(workspaces, workspaceName)
	if idx < 0 {
		return fmt.Errorf("workspace %q does not exist", workspaceName)
	}
	for _, existing := range workspaces[idx].Sessions {
		if existing.Name == session.Name {
			return fmt.Errorf("session %q already exists in workspace %q", session.Name, workspaceName)
		}
	}
	workspaces[idx].Sessions = append(workspaces[idx].Sessions, session)
	return s.save(workspaces)
}

// RemoveSession deletes the session named sessionName from the workspace
// named workspaceName and persists the change. It returns an error if the
// workspace or the session does not exist.
func (s *Store) RemoveSession(workspaceName, sessionName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	workspaces, err := s.load()
	if err != nil {
		return err
	}
	wIdx := findWorkspaceIndex(workspaces, workspaceName)
	if wIdx < 0 {
		return fmt.Errorf("workspace %q does not exist", workspaceName)
	}
	sessions := workspaces[wIdx].Sessions
	sIdx := findSessionIndex(sessions, sessionName)
	if sIdx < 0 {
		return fmt.Errorf("session %q does not exist in workspace %q", sessionName, workspaceName)
	}
	workspaces[wIdx].Sessions = append(sessions[:sIdx], sessions[sIdx+1:]...)
	return s.save(workspaces)
}

// AttachRepo canonicalizes repoPath and adds it as a member of the workspace
// named workspaceName, then persists the change. It returns an error if the
// workspace does not exist or repoPath is already a member.
func (s *Store) AttachRepo(workspaceName, repoPath string) error {
	canonical, err := pathnorm.Canonical(repoPath)
	if err != nil {
		return fmt.Errorf("canonicalize repo path %q: %w", repoPath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	workspaces, err := s.load()
	if err != nil {
		return err
	}
	idx := findWorkspaceIndex(workspaces, workspaceName)
	if idx < 0 {
		return fmt.Errorf("workspace %q does not exist", workspaceName)
	}
	for _, m := range workspaces[idx].Members {
		if m.Path == canonical {
			return fmt.Errorf("repo %q is already a member of workspace %q", canonical, workspaceName)
		}
	}
	workspaces[idx].Members = append(workspaces[idx].Members, MemberRepo{Path: canonical})
	return s.save(workspaces)
}

// DetachRepo canonicalizes repoPath and removes it from the membership of the
// workspace named workspaceName, then persists the change. It returns an
// error if the workspace does not exist or repoPath is not a member.
func (s *Store) DetachRepo(workspaceName, repoPath string) error {
	canonical, err := pathnorm.Canonical(repoPath)
	if err != nil {
		return fmt.Errorf("canonicalize repo path %q: %w", repoPath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	workspaces, err := s.load()
	if err != nil {
		return err
	}
	idx := findWorkspaceIndex(workspaces, workspaceName)
	if idx < 0 {
		return fmt.Errorf("workspace %q does not exist", workspaceName)
	}
	members := workspaces[idx].Members
	mIdx := -1
	for i, m := range members {
		if m.Path == canonical {
			mIdx = i
			break
		}
	}
	if mIdx < 0 {
		return fmt.Errorf("repo %q is not a member of workspace %q", canonical, workspaceName)
	}
	workspaces[idx].Members = append(members[:mIdx], members[mIdx+1:]...)
	return s.save(workspaces)
}

// load reads and parses workspaces.json. Callers must hold s.mu.
func (s *Store) load() ([]Workspace, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Workspace{}, nil
		}
		return nil, fmt.Errorf("read workspaces %s: %w", s.path, err)
	}

	var raw workspacesFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse workspaces %s: %w", s.path, err)
	}

	workspaces := make([]Workspace, 0, len(raw.Workspaces))
	for _, ws := range raw.Workspaces {
		resolved, err := resolveMembers(ws.Members)
		if err != nil {
			return nil, err
		}
		ws.Members = resolved
		workspaces = append(workspaces, ws)
	}
	return workspaces, nil
}

// save writes workspaces to s.path atomically (temp file + rename). Callers
// must hold s.mu. The parent directory is created if it does not exist.
func (s *Store) save(workspaces []Workspace) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create workspaces dir %s: %w", dir, err)
	}

	raw := workspacesFile{Workspaces: workspaces}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspaces: %w", err)
	}

	// Write to a temp file in the same directory so the rename is atomic on
	// POSIX systems (same filesystem, single syscall) — mirrors
	// internal/settings/roster.go's Save.
	tmp, err := os.CreateTemp(dir, "workspaces-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp workspaces file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp workspaces file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp workspaces file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename workspaces file: %w", err)
	}
	return nil
}

// resolveMembers canonicalizes each member's path and flags it Missing when
// absent from disk, mirroring settings.LoadConfig's treatment of
// RepoConfig.Missing.
func resolveMembers(members []MemberRepo) ([]MemberRepo, error) {
	resolved := make([]MemberRepo, 0, len(members))
	for _, m := range members {
		canonical, err := pathnorm.Canonical(m.Path)
		if err != nil {
			return nil, fmt.Errorf("canonicalize member repo path %q: %w", m.Path, err)
		}
		_, statErr := os.Stat(canonical)
		missing := statErr != nil && errors.Is(statErr, os.ErrNotExist)
		resolved = append(resolved, MemberRepo{Path: canonical, Missing: missing})
	}
	return resolved, nil
}

// findWorkspaceIndex returns the index of the workspace named name in
// workspaces, or -1 if none matches.
func findWorkspaceIndex(workspaces []Workspace, name string) int {
	for i, ws := range workspaces {
		if ws.Name == name {
			return i
		}
	}
	return -1
}

// findSessionIndex returns the index of the session named name in sessions,
// or -1 if none matches.
func findSessionIndex(sessions []Session, name string) int {
	for i, sess := range sessions {
		if sess.Name == name {
			return i
		}
	}
	return -1
}
