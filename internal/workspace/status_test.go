package workspace_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// mustStatusDir creates a temporary subdirectory and returns its canonical
// path, so assertions can compare against Session.Dir / SessionStatus values.
func mustStatusDir(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	canonical, err := pathnorm.Canonical(dir)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", dir, err)
	}
	return canonical
}

// makeStatusSession builds a workspace.Session with the given canonical dir.
func makeStatusSession(name, dir string) workspace.Session {
	return workspace.Session{Name: name, Dir: dir, Branch: name, Harness: "opencode"}
}

// makeLiveView builds a state.SessionView for testing, with Provider
// defaulting to "opencode" and ParentID empty (top-level, as the liveTopLevel
// contract requires).
func makeLiveView(dir, sessionID, title string, src state.Source, activity time.Time) state.SessionView {
	return state.SessionView{
		SessionID:    sessionID,
		Title:        title,
		Directory:    dir,
		Source:       src,
		Attention:    state.AttnInactive,
		LastActivity: activity,
		Provider:     harness.Kind("opencode"),
	}
}

// findStatus returns the SessionStatus for the session at dir, or nil.
func findStatus(statuses []workspace.WorkspaceStatus, dir string) *workspace.SessionStatus {
	for i := range statuses {
		for j := range statuses[i].Sessions {
			if statuses[i].Sessions[j].Session.Dir == dir {
				return &statuses[i].Sessions[j]
			}
		}
	}
	return nil
}

func TestMergeStatus_LiveSessionReportsRunning(t *testing.T) {
	tmp := t.TempDir()
	dir := mustStatusDir(t, tmp, "sess-running")
	ws := workspace.Workspace{Name: "demo", Sessions: []workspace.Session{makeStatusSession("running", dir)}}

	activity := time.Now()
	live := makeLiveView(dir, "sid-live", "Live Title", state.SourceLive, activity)
	live.Attention = state.AttnQuestionPending

	got := workspace.MergeStatus([]workspace.Workspace{ws}, map[string]settings.RosterEntry{}, []state.SessionView{live})

	st := findStatus(got, dir)
	if st == nil {
		t.Fatalf("session %s not found in result", dir)
	}
	if st.State != settings.StateRunning {
		t.Errorf("State = %v, want %v", st.State, settings.StateRunning)
	}
	if st.Title != "Live Title" {
		t.Errorf("Title = %q, want %q", st.Title, "Live Title")
	}
	if st.SessionID != "sid-live" {
		t.Errorf("SessionID = %q, want %q", st.SessionID, "sid-live")
	}
	if st.Attention != state.AttnQuestionPending {
		t.Errorf("Attention = %v, want %v", st.Attention, state.AttnQuestionPending)
	}
}

func TestMergeStatus_RosterOnlyReportsStopped(t *testing.T) {
	tmp := t.TempDir()
	dir := mustStatusDir(t, tmp, "sess-stopped")
	ws := workspace.Workspace{Name: "demo", Sessions: []workspace.Session{makeStatusSession("stopped", dir)}}

	activity := time.Now().Add(-time.Hour)
	roster := map[string]settings.RosterEntry{
		dir: {Dir: dir, Harness: "opencode", Title: "Roster Title", SessionID: "sid-roster", LastActivity: activity},
	}

	got := workspace.MergeStatus([]workspace.Workspace{ws}, roster, nil)

	st := findStatus(got, dir)
	if st == nil {
		t.Fatalf("session %s not found in result", dir)
	}
	if st.State != settings.StateStopped {
		t.Errorf("State = %v, want %v", st.State, settings.StateStopped)
	}
	if st.Title != "Roster Title" {
		t.Errorf("Title = %q, want %q", st.Title, "Roster Title")
	}
	if !st.LastActivity.Equal(activity) {
		t.Errorf("LastActivity = %v, want %v", st.LastActivity, activity)
	}
}

func TestMergeStatus_MissingDirectoryReportsMissing(t *testing.T) {
	tmp := t.TempDir()
	// Never created on disk, so the existence check fails.
	dir := filepath.Join(tmp, "sess-gone")
	ws := workspace.Workspace{Name: "demo", Sessions: []workspace.Session{makeStatusSession("gone", dir)}}

	got := workspace.MergeStatus([]workspace.Workspace{ws}, map[string]settings.RosterEntry{}, nil)

	st := findStatus(got, dir)
	if st == nil {
		t.Fatalf("session %s not found in result", dir)
	}
	if st.State != settings.StateMissing {
		t.Errorf("State = %v, want %v", st.State, settings.StateMissing)
	}
}

func TestMergeStatus_SameDirLiveBeatsRecent(t *testing.T) {
	tmp := t.TempDir()
	dir := mustStatusDir(t, tmp, "sess-collapse")
	ws := workspace.Workspace{Name: "demo", Sessions: []workspace.Session{makeStatusSession("collapse", dir)}}

	now := time.Now()
	recent := makeLiveView(dir, "sid-recent", "Recent Title", state.SourceRecent, now)
	live := makeLiveView(dir, "sid-live", "Live Title", state.SourceLive, now.Add(-time.Hour))

	got := workspace.MergeStatus([]workspace.Workspace{ws}, map[string]settings.RosterEntry{}, []state.SessionView{recent, live})

	st := findStatus(got, dir)
	if st == nil {
		t.Fatalf("session %s not found in result", dir)
	}
	if st.State != settings.StateRunning || st.SessionID != "sid-live" {
		t.Errorf("got State=%v SessionID=%q, want StateRunning/sid-live (live beats recent)", st.State, st.SessionID)
	}
}

func TestMergeStatus_SameSourceNewestActivityWins(t *testing.T) {
	tmp := t.TempDir()
	dir := mustStatusDir(t, tmp, "sess-tiebreak")
	ws := workspace.Workspace{Name: "demo", Sessions: []workspace.Session{makeStatusSession("tiebreak", dir)}}

	older := makeLiveView(dir, "sid-older", "Older", state.SourceLive, time.Now().Add(-time.Hour))
	newer := makeLiveView(dir, "sid-newer", "Newer", state.SourceLive, time.Now())

	got := workspace.MergeStatus([]workspace.Workspace{ws}, map[string]settings.RosterEntry{}, []state.SessionView{older, newer})

	st := findStatus(got, dir)
	if st == nil {
		t.Fatalf("session %s not found in result", dir)
	}
	if st.SessionID != "sid-newer" {
		t.Errorf("SessionID = %q, want %q (newest LastActivity should win)", st.SessionID, "sid-newer")
	}
}

func TestMergeStatus_NoLiveOrRosterStillAppearsWithEmptyTitle(t *testing.T) {
	tmp := t.TempDir()
	dir := mustStatusDir(t, tmp, "sess-bare")
	ws := workspace.Workspace{Name: "demo", Sessions: []workspace.Session{makeStatusSession("bare", dir)}}

	got := workspace.MergeStatus([]workspace.Workspace{ws}, map[string]settings.RosterEntry{}, nil)

	st := findStatus(got, dir)
	if st == nil {
		t.Fatalf("session %s not found in result", dir)
	}
	if st.Title != "" {
		t.Errorf("Title = %q, want empty", st.Title)
	}
}
