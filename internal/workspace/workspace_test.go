package workspace_test

import (
	"encoding/json"
	"testing"

	"github.com/guilhermehto/cogitator/internal/workspace"
)

// TestMemberRepo_MissingFieldNotPersisted verifies that MemberRepo.Missing is
// excluded from the JSON wire format (json:"-"): it is a load-time computed
// flag, not durable state, mirroring settings.RepoConfig.Missing.
func TestMemberRepo_MissingFieldNotPersisted(t *testing.T) {
	ws := workspace.Workspace{
		Name: "demo",
		Members: []workspace.MemberRepo{
			{Path: "/repo/a", Missing: true},
		},
	}

	data, err := json.Marshal(ws)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var round workspace.Workspace
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if round.Members[0].Missing {
		t.Error("Missing should not survive a JSON round trip")
	}
	if round.Members[0].Path != "/repo/a" {
		t.Errorf("Path = %q, want %q", round.Members[0].Path, "/repo/a")
	}
}

// TestWorkspace_ZeroValue verifies that a zero-value Workspace and Session are
// safe to use in test literals without initializing their slice fields.
func TestWorkspace_ZeroValue(t *testing.T) {
	var ws workspace.Workspace
	if ws.Name != "" || len(ws.Members) != 0 || len(ws.Sessions) != 0 {
		t.Error("zero-value Workspace should be empty")
	}

	var sess workspace.Session
	if sess.Name != "" || len(sess.Members) != 0 {
		t.Error("zero-value Session should be empty")
	}
}
