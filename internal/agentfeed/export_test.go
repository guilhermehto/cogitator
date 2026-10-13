package agentfeed

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
)

func decodeExport(t *testing.T, raw []byte) ExportList {
	t.Helper()

	var out ExportList
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode export: %v", err)
	}

	return out
}

func TestExportJSON_MapsEveryField(t *testing.T) {
	a := agents.Agent{
		ID:           "/repo/wt",
		Kind:         agents.KindWorktree,
		Repo:         "repo",
		Branch:       "feat",
		Workspace:    "ws",
		SessionName:  "sess",
		Harness:      "omp",
		Title:        "title",
		Attention:    state.Attention("permission"),
		State:        settings.StateRunning,
		Group:        agents.GroupNeedsYou,
		LastActivity: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600)),
		MuxSessionID: "mux",
		WindowID:     "@1",
		PaneID:       "%2",
	}

	raw, err := ExportJSON([]agents.Agent{a}, "/home/me")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	got := decodeExport(t, raw)
	want := ExportAgent{
		ID: "/repo/wt", Kind: "worktree", Repo: "repo", Branch: "feat", Workspace: "ws",
		SessionName: "sess", Harness: "omp", Title: "title", Attention: "permission", State: "running",
		Group: "needs_you", LastActivity: "2026-01-02T02:04:05Z", MuxSession: "mux",
		WindowID: "@1", PaneID: "%2",
	}
	if got.Version != 1 || got.Home != "/home/me" || len(got.Agents) != 1 || got.Agents[0] != want {
		t.Fatalf("export = %+v, want version 1 with %+v", got, want)
	}

	for _, key := range []string{`"session_name"`, `"last_activity"`, `"mux_session"`, `"window_id"`, `"pane_id"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("export missing key %s: %s", key, raw)
		}
	}
}

func TestExportJSON_NamesEveryGroup(t *testing.T) {
	cases := map[agents.Group]string{
		agents.GroupNeedsYou: "needs_you",
		agents.GroupDone:     "done",
		agents.GroupWorking:  "working",
		agents.GroupIdle:     "idle",
		agents.GroupStopped:  "stopped",
	}
	for group, name := range cases {
		raw, err := ExportJSON([]agents.Agent{{ID: "/a", Group: group}}, "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}

		if got := decodeExport(t, raw).Agents[0].Group; got != name {
			t.Errorf("group %d = %q, want %q", group, got, name)
		}
	}
}

func TestExportJSON_LeavesZeroLastActivityEmpty(t *testing.T) {
	raw, err := ExportJSON([]agents.Agent{{ID: "/a"}}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if !strings.Contains(string(raw), `"last_activity":""`) {
		t.Fatalf("export = %s, want empty last_activity", raw)
	}
}

func TestExportJSON_EmptyListIsEmptyArray(t *testing.T) {
	for _, list := range [][]agents.Agent{nil, {}} {
		raw, err := ExportJSON(list, "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}

		if string(raw) != `{"version":1,"agents":[]}` {
			t.Fatalf("export = %s, want empty agents array", raw)
		}
	}
}

func TestExportJSON_RoundTripsThroughLiveFeed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sock := shortSockPath(t)
	holder := &Holder{}
	holder.Set([]agents.Agent{{ID: "/repo/wt", Kind: agents.KindStray, Group: agents.GroupWorking}})
	cleanup, err := Serve(ctx, sock, holder, make(chan Action, 1), nil)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer cleanup()

	list, err := NewClientAt(sock).List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	raw, err := ExportJSON(list, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	got := decodeExport(t, raw)
	if len(got.Agents) != 1 || got.Agents[0].ID != "/repo/wt" || got.Agents[0].Group != "working" || got.Agents[0].Kind != "stray" {
		t.Fatalf("export = %s, want the served agent", raw)
	}
}
