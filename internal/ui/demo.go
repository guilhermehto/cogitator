package ui

import (
	"context"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/taskwarrior"
)

// RunDemo starts the TUI populated with a curated synthetic snapshot and a
// fake Taskwarrior backend. No mDNS discovery is performed; no shell-outs to
// the `task` binary happen. Intended for screenshots / asciinema captures
// where on-screen content must be deterministic and exercise every visual
// state the application can render.
//
// Mutations driven through the Tasks pane (a / e / d / D / s / U) are
// accepted by the fake client but silently dropped; the snapshot is never
// refreshed so the TUI stays visually stable across any key path needed
// during the capture.
func RunDemo(cfg *config.Config, logger *slog.Logger) error {
	if cfg == nil {
		cfg = config.Default()
	}
	if logger == nil {
		logger = slog.Default()
	}

	now := time.Now()
	snap := state.Snapshot{
		Sessions:  demoSessions(now),
		UpdatedAt: now,
	}

	// Buffered channel + a single send. The model reads the snapshot on the
	// first waitSnapshot tick and then blocks on subsequent reads. That is
	// exactly the behaviour we want for a static capture — the display
	// settles once and stays put.
	snaps := make(chan state.Snapshot, 1)
	snaps <- snap

	fakeTW := &demoClient{tasks: demoTasks(now)}

	m := newModel(snaps, cfg, false, false, fakeTW)
	// Expand the Recent group so the section is visible in the capture.
	m.recentCollapsed = false

	logger.Info("running demo mode",
		"sessions", len(snap.Sessions),
		"tasks", len(fakeTW.tasks))

	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// demoClient is the synthetic ClientAPI used by RunDemo. Available() returns
// true so the Tasks pane renders; Export() returns the curated fixture; all
// mutations succeed without changing state so the snapshot does not churn
// while keys are exercised for a capture.
type demoClient struct {
	tasks []taskwarrior.TaskView
}

func (d *demoClient) Available() bool { return true }

func (d *demoClient) Export(_ context.Context) ([]taskwarrior.TaskView, error) {
	return d.tasks, nil
}

func (d *demoClient) Add(_ context.Context, _ string) error          { return nil }
func (d *demoClient) Modify(_ context.Context, _, _ string) error    { return nil }
func (d *demoClient) Done(_ context.Context, _ string) error         { return nil }
func (d *demoClient) Delete(_ context.Context, _ string) error       { return nil }
func (d *demoClient) Start(_ context.Context, _ string) error        { return nil }
func (d *demoClient) Stop(_ context.Context, _ string) error         { return nil }
func (d *demoClient) Undo(_ context.Context) error                   { return nil }

// demoSessions returns a fixture covering each attention state, both Source
// kinds, parent/child rendering, multiple agents (for palette colour
// variety), and a mix of directories so the home-prefix shortening shows up.
func demoSessions(now time.Time) []state.SessionView {
	mins := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Minute) }
	hours := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Hour) }

	return []state.SessionView{
		// Live, busy, active.
		{
			InstanceID:   "demo-laptop",
			InstanceName: "laptop",
			SessionID:    "ses_001",
			Title:        "Refactor session-store pagination",
			Agent:        "fabricator",
			Directory:    "~/src/cogitator",
			StatusType:   "busy",
			Source:       state.SourceLive,
			Attention:    state.AttnActive,
			LastActivity: mins(0),
			Created:      hours(2),
			Provider:     harness.Kind("opencode"),
		},
		// Live root waiting on permission, with a generating child.
		{
			InstanceID:   "demo-laptop",
			InstanceName: "laptop",
			SessionID:    "ses_002",
			Title:        "Investigate flaky CI run on darwin-arm64",
			Agent:        "magos-iterator",
			Directory:    "~/src/cogitator",
			Source:       state.SourceLive,
			Attention:    state.AttnPermissionPending,
			LastActivity: mins(1),
			Created:      hours(1),
			Provider:     harness.Kind("opencode"),
		},
		{
			InstanceID:   "demo-laptop",
			InstanceName: "laptop",
			SessionID:    "ses_003",
			ParentID:     "ses_002",
			Title:        "Run go test ./internal/state/...",
			Agent:        "enginseer",
			StatusType:   "generating",
			Source:       state.SourceLive,
			Attention:    state.AttnActive,
			LastActivity: mins(0),
			Created:      mins(40),
			Provider:     harness.Kind("opencode"),
		},
		// Live, awaiting an answer (codex — exercises the provider badge).
		{
			InstanceID:   "demo-server",
			InstanceName: "server",
			SessionID:    "ses_004",
			Title:        "Draft release notes for v0.4.0",
			Agent:        "scribe",
			Directory:    "~/work/notes",
			Source:       state.SourceLive,
			Attention:    state.AttnQuestionPending,
			LastActivity: mins(2),
			Created:      hours(3),
			Provider:     harness.Kind("codex"),
		},
		// Live, errored.
		{
			InstanceID:   "demo-server",
			InstanceName: "server",
			SessionID:    "ses_005",
			Title:        "Triage opencode#412 (mDNS race)",
			Agent:        "logis",
			Directory:    "~/src/opencode",
			Source:       state.SourceLive,
			Attention:    state.AttnErrored,
			LastActivity: mins(5),
			Created:      hours(4),
			Provider:     harness.Kind("opencode"),
		},
		// Live, finished — agent completed a requested task; awaiting your return.
		{
			InstanceID:   "demo-server",
			InstanceName: "server",
			SessionID:    "ses_009",
			Title:        "Add --bell flag docs to README",
			Agent:        "scribe",
			Directory:    "~/src/cogitator",
			StatusType:   "idle",
			Source:       state.SourceLive,
			Attention:    state.AttnFinished,
			LastActivity: mins(3),
			Created:      hours(1),
			Provider:     harness.Kind("opencode"),
		},
		// Live, inactive (idle long enough to dim).
		{
			InstanceID:   "demo-server",
			InstanceName: "server",
			SessionID:    "ses_006",
			Title:        "Sweep TODO comments under internal/oc",
			Agent:        "servitor",
			Directory:    "~/src/cogitator",
			Source:       state.SourceLive,
			Attention:    state.AttnInactive,
			LastActivity: mins(35),
			Created:      hours(6),
			Provider:     harness.Kind("opencode"),
		},
		// Recent (collapsed group expanded in RunDemo for visibility).
		{
			InstanceID:   "demo-laptop",
			InstanceName: "laptop",
			SessionID:    "ses_007",
			Title:        "Spike: terminal-bell rate limiter",
			Agent:        "fabricator",
			Directory:    "~/src/cogitator",
			Source:       state.SourceRecent,
			Attention:    state.AttnInactive,
			LastActivity: mins(45),
			Created:      hours(5),
			Provider:     harness.Kind("opencode"),
		},
		{
			InstanceID:   "demo-laptop",
			InstanceName: "laptop",
			SessionID:    "ses_008",
			Title:        "Generate OpenAPI types",
			Agent:        "enginseer",
			Directory:    "~/src/cogitator",
			Source:       state.SourceRecent,
			Attention:    state.AttnInactive,
			LastActivity: mins(58),
			Created:      hours(7),
			Provider:     harness.Kind("opencode"),
		},
	}
}

// demoTasks returns a fixture exercising each priority tier, one running
// task (bold green + play glyph), and a spread of projects / tags / due
// dates so the column widths look populated in the capture.
func demoTasks(now time.Time) []taskwarrior.TaskView {
	days := func(n int) time.Time { return now.AddDate(0, 0, n) }

	return []taskwarrior.TaskView{
		// Currently running (Start non-zero) → play glyph + bold row.
		{
			ID:          "1",
			Description: "Wire start/stop key into Tasks pane",
			Project:     "cogitator",
			Tags:        []string{"ui", "tui"},
			Priority:    "H",
			Start:       now.Add(-25 * time.Minute),
			Urgency:     12.3,
			Status:      "pending",
		},
		{
			ID:          "2",
			Description: "Capture demo screenshot for README",
			Project:     "cogitator",
			Tags:        []string{"docs"},
			Priority:    "H",
			Due:         days(0),
			Urgency:     11.0,
			Status:      "pending",
		},
		{
			ID:          "3",
			Description: "Add macOS code signing to release pipeline",
			Project:     "release",
			Tags:        []string{"build", "ci"},
			Priority:    "M",
			Due:         days(7),
			Urgency:     8.4,
			Status:      "pending",
		},
		{
			ID:          "4",
			Description: "Investigate mDNS discovery on Linux",
			Project:     "discovery",
			Tags:        []string{"linux"},
			Priority:    "M",
			Urgency:     6.1,
			Status:      "pending",
		},
		{
			ID:          "5",
			Description: "Polish help-line copy",
			Project:     "cogitator",
			Tags:        []string{"polish"},
			Priority:    "L",
			Urgency:     2.8,
			Status:      "pending",
		},
		{
			ID:          "6",
			Description: "Read up on bubbletea v2 migration plan",
			Project:     "learning",
			Tags:        []string{"reading"},
			Urgency:     1.2,
			Status:      "pending",
		},
	}
}
