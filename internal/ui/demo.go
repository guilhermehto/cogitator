package ui

import (
	"log/slog"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/state"
)

// RunDemo starts the TUI populated with a curated synthetic worktree roster —
// the merged worktree/tmux view that is cogitator's headline feature. No mDNS
// discovery and no git/tmux shell-outs: the workspace rows are injected
// directly and the background row build is suppressed (the model.demo flag)
// so the capture is deterministic. Intended for the README screenshot /
// asciinema captures.
func RunDemo(cfg *config.Config, logger *slog.Logger) error {
	if cfg == nil {
		cfg = config.Default()
	}
	if logger == nil {
		logger = slog.Default()
	}

	// vhs/ttyd records through a pipeline where termenv can't detect colour
	// support (no tty / bare TERM), so lipgloss falls back to the no-colour
	// profile and the capture renders monochrome with no selection band. The
	// demo exists only for screenshots, so force truecolour unconditionally.
	lipgloss.SetColorProfile(termenv.TrueColor)

	now := time.Now()
	rows := demoWorktrees(now)

	// The snapshot only feeds the header's "N live" count and the timestamp;
	// the worktree rows below are what the capture showcases. Derive the live
	// sessions from the running rows so the header stays in sync with the view.
	snap := state.Snapshot{Sessions: liveSessionsFor(rows), UpdatedAt: now}

	// Buffered channel + a single send: the model reads the snapshot once and
	// then blocks, so the display settles and stays put for the capture.
	snaps := make(chan state.Snapshot, 1)
	snaps <- snap

	m := newDemoModel(snaps, cfg, rows, demoAgents(now))

	logger.Info("running demo mode", "worktrees", len(rows))

	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// newDemoModel builds the --demo model: the curated rows and agents injected
// directly, background builds suppressed, and the Repos view selected — the
// capture showcases the worktree roster, not the (empty) Workspaces view
// newModel opens on. The demo has no agent feed, so nothing rebuilds the
// agents.
func newDemoModel(snaps <-chan state.Snapshot, cfg *config.Config, rows []settings.Row, roster []agents.Agent) model {
	m := newModel(snaps, cfg, false, false)
	m.demo = true
	m.view = viewSessions
	m.workspaceRows = rows
	m.agents = roster
	return m
}

// demoAgents returns the Agents tab roster for --demo: the demo worktrees'
// sessions plus a workspace session, so every section, status glyph and row
// shape shows.
func demoAgents(now time.Time) []agents.Agent {
	mins := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Minute) }
	hours := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Hour) }

	const cog = "~/src/cogitator"
	const api = "~/src/api-gateway"

	worktree := func(repo, branch, harness, title string, group agents.Group, attn state.Attention, st settings.RowState, last time.Time) agents.Agent {
		dir := repo + "/.wt/" + strings.ReplaceAll(branch, "/", "-")
		return agents.Agent{
			ID: dir, Dir: dir, Kind: agents.KindWorktree, Repo: repo, Branch: branch,
			Harness: harness, Title: title, Group: group, Attention: attn, State: st,
			LastActivity: last, OrderedAt: last,
		}
	}

	return []agents.Agent{
		worktree(cog, "feat/tmux-launcher", "opencode", "Launch worktrees into tmux windows",
			agents.GroupNeedsYou, state.AttnPermissionPending, settings.StateRunning, mins(0)),
		worktree(cog, "fix/mdns-race", "codex", "Resolve discovery race on darwin-arm64",
			agents.GroupNeedsYou, state.AttnQuestionPending, settings.StateRunning, mins(1)),
		worktree(api, "feat/oauth-pkce", "claude-code", "Implement OAuth2 PKCE flow",
			agents.GroupNeedsYou, state.AttnErrored, settings.StateRunning, mins(4)),
		worktree(cog, "main", "opencode", "Watch local opencode instances",
			agents.GroupWorking, state.AttnActive, settings.StateRunning, mins(6)),
		{
			ID: "~/workspaces/checkout/pkce-rollout", Dir: "~/workspaces/checkout/pkce-rollout",
			Kind: agents.KindWorkspace, Workspace: "checkout", SessionName: "pkce-rollout",
			Harness: "claude-code", Tab: "LEX-42", Title: "Roll PKCE out across services",
			Group: agents.GroupWorking, Attention: state.AttnActive, State: settings.StateRunning,
			LastActivity: mins(9), OrderedAt: mins(9),
		},
		worktree(cog, "chore/release", "opencode", "Publish to homebrew tap via goreleaser",
			agents.GroupDone, state.AttnFinished, settings.StateRunning, mins(2)),
		worktree(api, "main", "claude-code", "Gateway service baseline",
			agents.GroupIdle, state.AttnInactive, settings.StateRunning, hours(1)),
		worktree(cog, "spike/bell-ratelimit", "opencode", "Terminal-bell rate limiter",
			agents.GroupStopped, "", settings.StateStopped, hours(3)),
		worktree(api, "fix/rate-limit", "", "",
			agents.GroupIdle, "", settings.StateUnknown, hours(52)),
	}
}

// demoWorktrees returns a curated worktree roster across two repos that
// exercises every row state the view renders: the repo base, running agents
// across each attention badge (active / permission / question / finished /
// error), a stopped worktree, and one whose status is unknown (a tmux window
// exists but the harness can't be probed). Branches and titles read like real
// in-flight work so the screenshot tells a story.
func demoWorktrees(now time.Time) []settings.Row {
	mins := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Minute) }
	hours := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Hour) }

	const cog = "~/src/cogitator"
	const api = "~/src/api-gateway"

	return []settings.Row{
		{
			Repo: cog, Worktree: cog, Branch: "main", IsRoot: true,
			Harness: "opencode", Title: "Watch local opencode instances",
			SessionID: "ses_main", State: settings.StateRunning,
			Attention: state.AttnActive, LastActivity: mins(0),
		},
		{
			Repo: cog, Worktree: cog + "/.wt/tmux-launcher", Branch: "feat/tmux-launcher",
			Harness: "opencode", Title: "Launch worktrees into tmux windows",
			SessionID: "ses_w1", State: settings.StateRunning,
			Attention: state.AttnPermissionPending, LastActivity: mins(0),
		},
		{
			Repo: cog, Worktree: cog + "/.wt/mdns-race", Branch: "fix/mdns-race",
			Harness: "codex", Title: "Resolve discovery race on darwin-arm64",
			SessionID: "ses_w2", State: settings.StateRunning,
			Attention: state.AttnQuestionPending, LastActivity: mins(1),
		},
		{
			Repo: cog, Worktree: cog + "/.wt/release", Branch: "chore/release",
			Harness: "opencode", Title: "Publish to homebrew tap via goreleaser",
			SessionID: "ses_w3", State: settings.StateRunning,
			Attention: state.AttnFinished, LastActivity: mins(2),
		},
		{
			Repo: cog, Worktree: cog + "/.wt/bell", Branch: "spike/bell-ratelimit",
			Harness: "opencode", Title: "Terminal-bell rate limiter",
			SessionID: "ses_w4", State: settings.StateStopped, LastActivity: hours(3),
		},
		{
			Repo: api, Worktree: api, Branch: "main", IsRoot: true,
			Harness: "claude-code", Title: "Gateway service baseline",
			SessionID: "ses_g0", State: settings.StateRunning,
			Attention: state.AttnActive, LastActivity: mins(1),
		},
		{
			Repo: api, Worktree: api + "/.wt/oauth-pkce", Branch: "feat/oauth-pkce",
			Harness: "claude-code", Title: "Implement OAuth2 PKCE flow",
			SessionID: "ses_g1", State: settings.StateRunning,
			Attention: state.AttnErrored, LastActivity: mins(4),
		},
		{
			Repo: api, Worktree: api + "/.wt/rate-limit", Branch: "fix/rate-limit",
			State: settings.StateUnknown, LastActivity: hours(6),
		},
	}
}

// liveSessionsFor synthesises one live SessionView per running worktree so the
// header's live count matches the rendered roster. These never appear in the
// worktree view itself; they exist only to populate the header summary.
func liveSessionsFor(rows []settings.Row) []state.SessionView {
	var sessions []state.SessionView
	for _, r := range rows {
		if r.State != settings.StateRunning {
			continue
		}
		sessions = append(sessions, state.SessionView{
			SessionID:    r.SessionID,
			Source:       state.SourceLive,
			Attention:    r.Attention,
			LastActivity: r.LastActivity,
		})
	}
	return sessions
}
