package agentfeed

import (
	"encoding/json"
	"time"

	"github.com/guilhermehto/cogitator/internal/agents"
)

// ExportVersion is the wire version of the `cogitator agents list` output.
const ExportVersion = 1

// ExportList carries home so consumers can abbreviate agent paths to "~" the
// way the TUI does.
type ExportList struct {
	Version int           `json:"version"`
	Home    string        `json:"home,omitempty"`
	Agents  []ExportAgent `json:"agents"`
}

type ExportAgent struct {
	ID           string `json:"id"`
	Dir          string `json:"dir"`
	Kind         string `json:"kind"`
	Repo         string `json:"repo"`
	Branch       string `json:"branch"`
	Workspace    string `json:"workspace"`
	SessionName  string `json:"session_name"`
	Harness      string `json:"harness"`
	Title        string `json:"title"`
	Tab          string `json:"tab"`
	Attention    string `json:"attention"`
	State        string `json:"state"`
	Group        string `json:"group"`
	LastActivity string `json:"last_activity"`
	MuxSession   string `json:"mux_session"`
	WindowID     string `json:"window_id"`
	PaneID       string `json:"pane_id"`
	// SessionOwned lets consumers treat a client anywhere in MuxSession as
	// being at this agent, matching agents.AgentAt.
	SessionOwned bool `json:"session_owned"`
}

func ExportJSON(list []agents.Agent, home string) ([]byte, error) {
	out := ExportList{Version: ExportVersion, Home: home, Agents: make([]ExportAgent, 0, len(list))}
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
		Dir:          a.Dir,
		Kind:         string(a.Kind),
		Repo:         a.Repo,
		Branch:       a.Branch,
		Workspace:    a.Workspace,
		SessionName:  a.SessionName,
		Harness:      a.Harness,
		Title:        a.Title,
		Tab:          a.Tab,
		Attention:    string(a.Attention),
		State:        string(a.State),
		Group:        groupName(a.Group),
		LastActivity: last,
		MuxSession:   a.MuxSessionID,
		WindowID:     a.WindowID,
		PaneID:       a.PaneID,
		SessionOwned: a.SessionOwned,
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
