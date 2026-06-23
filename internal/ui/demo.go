package ui

import (
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// RunDemo starts the TUI populated with a curated synthetic worktree roster —
// the merged worktree/tmux view that is cogitator's headline feature. No mDNS
// discovery, no git/tmux shell-outs, and no Taskwarrior pane: the workspace
// rows are injected directly and the background row build is suppressed (the
// model.demo flag) so the capture is deterministic. Intended for the README
// screenshot / asciinema captures.
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

	// nil tw suppresses the Tasks pane (twAvail=false).
	m := newModel(snaps, cfg, false, false, nil)
	m.demo = true
	m.workspaceRows = rows

	logger.Info("running demo mode", "worktrees", len(rows))

	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// demoWorktrees returns a curated worktree roster across two repos that
// exercises every row state the view renders: the repo base, running agents
// across each attention badge (active / permission / question / finished /
// error), a stopped worktree, and one whose status is unknown (a tmux window
// exists but the harness can't be probed). Branches and titles read like real
// in-flight work so the screenshot tells a story.
func demoWorktrees(now time.Time) []workspace.Row {
	mins := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Minute) }
	hours := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Hour) }

	const cog = "~/src/cogitator"
	const api = "~/src/api-gateway"

	return []workspace.Row{
		{
			Repo: cog, Worktree: cog, Branch: "main", IsRoot: true,
			Harness: "opencode", Title: "Watch local opencode instances",
			SessionID: "ses_main", State: workspace.StateRunning,
			Attention: state.AttnActive, LastActivity: mins(0),
		},
		{
			Repo: cog, Worktree: cog + "/.wt/tmux-launcher", Branch: "feat/tmux-launcher",
			Harness: "opencode", Title: "Launch worktrees into tmux windows",
			SessionID: "ses_w1", State: workspace.StateRunning,
			Attention: state.AttnPermissionPending, LastActivity: mins(0),
		},
		{
			Repo: cog, Worktree: cog + "/.wt/mdns-race", Branch: "fix/mdns-race",
			Harness: "codex", Title: "Resolve discovery race on darwin-arm64",
			SessionID: "ses_w2", State: workspace.StateRunning,
			Attention: state.AttnQuestionPending, LastActivity: mins(1),
		},
		{
			Repo: cog, Worktree: cog + "/.wt/release", Branch: "chore/release",
			Harness: "opencode", Title: "Publish to homebrew tap via goreleaser",
			SessionID: "ses_w3", State: workspace.StateRunning,
			Attention: state.AttnFinished, LastActivity: mins(2),
		},
		{
			Repo: cog, Worktree: cog + "/.wt/bell", Branch: "spike/bell-ratelimit",
			Harness: "opencode", Title: "Terminal-bell rate limiter",
			SessionID: "ses_w4", State: workspace.StateStopped, LastActivity: hours(3),
		},
		{
			Repo: api, Worktree: api, Branch: "main", IsRoot: true,
			Harness: "claude-code", Title: "Gateway service baseline",
			SessionID: "ses_g0", State: workspace.StateRunning,
			Attention: state.AttnActive, LastActivity: mins(1),
		},
		{
			Repo: api, Worktree: api + "/.wt/oauth-pkce", Branch: "feat/oauth-pkce",
			Harness: "claude-code", Title: "Implement OAuth2 PKCE flow",
			SessionID: "ses_g1", State: workspace.StateRunning,
			Attention: state.AttnErrored, LastActivity: mins(4),
		},
		{
			Repo: api, Worktree: api + "/.wt/rate-limit", Branch: "fix/rate-limit",
			State: workspace.StateUnknown, LastActivity: hours(6),
		},
	}
}

// liveSessionsFor synthesises one live SessionView per running worktree so the
// header's live count matches the rendered roster. These never appear in the
// worktree view itself; they exist only to populate the header summary.
func liveSessionsFor(rows []workspace.Row) []state.SessionView {
	var sessions []state.SessionView
	for _, r := range rows {
		if r.State != workspace.StateRunning {
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
