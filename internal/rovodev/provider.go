package rovodev

import (
	"context"
	"log/slog"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// InstanceID is the synthetic instance identifier used for all Rovo Dev
// sessions. It is a stable constant so the (provider, sessionID) dedup key in
// the store never collides with opencode's "host:port" instance ids or the
// other single-instance harnesses.
const InstanceID = "rovodev"

// Provider polls the Rovo Dev home directory on a configurable interval and
// emits provider.SessionUpdates into a Sink. It implements provider.Provider.
//
// Rovo Dev exposes no external command-hook that cogitator can wire for live
// attention, so this provider is poll-only: session activity is derived from
// file recency alone (see sessionToUpdate). It is intentionally free of
// internal/ui, bubbletea, internal/oc, and internal/state — a pure
// filesystem-in / sink-out adapter.
type Provider struct {
	rovodevHome   string
	pollInterval  time.Duration
	recencyWindow time.Duration
	logger        *slog.Logger
}

// NewProvider constructs a Rovo Dev Provider. rovodevHome may be empty (defaults
// to ~/.rovodev via ReadSessions). pollInterval and recencyWindow must be
// positive; callers should pass values from config.Config.
func NewProvider(rovodevHome string, pollInterval, recencyWindow time.Duration, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		rovodevHome:   rovodevHome,
		pollInterval:  pollInterval,
		recencyWindow: recencyWindow,
		logger:        logger,
	}
}

// Kind implements provider.Provider.
func (p *Provider) Kind() harness.Kind { return harness.KindRovodev }

// Start implements provider.Provider. It polls the Rovo Dev home on each tick
// and blocks until ctx is cancelled.
func (p *Provider) Start(ctx context.Context, sink provider.Sink) error {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	// Poll immediately on startup so sessions appear without waiting one full
	// interval.
	p.poll(sink)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.poll(sink)
		}
	}
}

// poll reads the current session set from disk and pushes a full snapshot into
// sink.
func (p *Provider) poll(sink provider.Sink) {
	sessions, err := ReadSessions(p.rovodevHome)
	if err != nil {
		p.logger.Warn("rovodev: failed to read sessions", "err", err)
		return
	}
	p.pollOnce(sink, sessions)
}

// pollOnce is the testable core of poll. It accepts the already-read session
// slice so tests can drive a single cycle deterministically without touching the
// filesystem.
func (p *Provider) pollOnce(sink provider.Sink, sessions []Session) {
	now := time.Now()
	updates := make([]provider.SessionUpdate, 0, len(sessions))
	for _, s := range sessions {
		updates = append(updates, p.sessionToUpdate(s, now))
	}
	// Emit the full snapshot atomically so the UI never sees a blank
	// intermediate state; an empty slice clears the instance without a flash.
	sink.ReplaceProviderInstance(harness.KindRovodev, InstanceID, updates)
}

// sessionToUpdate builds a provider.SessionUpdate from a poll-derived Session.
//
// Status is recency-derived (poll-only, no hooks): a session whose files were
// written within recencyWindow is treated as live and reported "busy" so the
// row surfaces as active; older sessions fall back to "recent"/inactive and are
// eventually hidden by the inactive-hide rule. This is the same base behaviour
// the hook-capable harnesses fall back to when no hook has fired, and it means
// an active Rovo Dev session (which rewrites its context file on every step)
// stays visible while a finished one fades within the window.
func (p *Provider) sessionToUpdate(s Session, now time.Time) provider.SessionUpdate {
	src := "recent"
	if p.recencyWindow > 0 && now.Sub(s.LastActivity) <= p.recencyWindow {
		src = "live"
	}

	statusType := ""
	if src == "live" {
		statusType = "busy"
	}

	return provider.SessionUpdate{
		Provider:     harness.KindRovodev,
		InstanceID:   InstanceID,
		InstanceName: InstanceID,
		SessionID:    s.ID,
		Title:        s.Title,
		Directory:    s.Dir,
		StatusType:   statusType,
		LastActivity: s.LastActivity,
		Created:      s.Created,
		Source:       src,
	}
}
