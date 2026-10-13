package agentfeed

import (
	"encoding/json"
	"time"

	"github.com/guilhermehto/cogitator/internal/agents"
)

// ExportVersion is the wire version of the `cogitator agents list` output.
const ExportVersion = 1

type ExportList struct {
	Version int           `json:"version"`
	Agents  []ExportAgent `json:"agents"`
}

type ExportAgent struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Repo         string `json:"repo"`
	Branch       string `json:"branch"`
	Workspace    string `json:"workspace"`
	SessionName  string `json:"session_name"`
	Harness      string `json:"harness"`
	Title        string `json:"title"`
	Attention    string `json:"attention"`
	Group        string `json:"group"`
	LastActivity string `json:"last_activity"`
	MuxSession   string `json:"mux_session"`
	WindowID     string `json:"window_id"`
	PaneID       string `json:"pane_id"`
}

func ExportJSON(list []agents.Agent) ([]byte, error) {
	out := ExportList{Version: ExportVersion, Agents: make([]ExportAgent, 0, len(list))}
	for _, a := range list {
		out.Agents = append(out.Agents, exportAgent(a))
	}

	return json.Marshal(out)
}

func exportAgent(a agents.Agent) ExportAgent {
	last := ""
	if !a.LastActivity.IsZero() {
		last = a.LastActivity.UTC().Format(time.RFC3339)
	}

	return ExportAgent{
		ID:           a.ID,
		Kind:         string(a.Kind),
		Repo:         a.Repo,
		Branch:       a.Branch,
		Workspace:    a.Workspace,
		SessionName:  a.SessionName,
		Harness:      a.Harness,
		Title:        a.Title,
		Attention:    string(a.Attention),
		Group:        groupName(a.Group),
		LastActivity: last,
		MuxSession:   a.MuxSessionID,
		WindowID:     a.WindowID,
		PaneID:       a.PaneID,
	}
}

func groupName(g agents.Group) string {
	switch g {
	case agents.GroupNeedsYou:
		return "needs_you"
	case agents.GroupDone:
		return "done"
	case agents.GroupWorking:
		return "working"
	case agents.GroupIdle:
		return "idle"
	case agents.GroupStopped:
		return "stopped"
	}

	return ""
}
