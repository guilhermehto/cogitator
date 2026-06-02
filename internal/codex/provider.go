package codex

import (
	"context"
	"log/slog"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// InstanceID is the synthetic instance identifier used for all Codex sessions.
// It is a stable constant so the (provider, sessionID) dedup key in the store
// never collides with opencode's "host:port" instance ids.
const InstanceID = "codex"

// Provider polls CODEX_HOME on a configurable interval and emits
// provider.SessionUpdates into a Sink. It implements provider.Provider.
//
// The package is intentionally free of internal/ui, bubbletea, internal/oc,
// and internal/state — it is a pure filesystem-in / sink-out adapter.
type Provider struct {
	codexHome     string
	pollInterval  time.Duration
	recencyWindow time.Duration
	logger        *slog.Logger
}

// NewProvider constructs a Codex Provider. codexHome may be empty (defaults to
// ~/.codex via ReadSessions). pollInterval and recencyWindow must be positive;
// callers should pass values from config.Config.
func NewProvider(codexHome string, pollInterval, recencyWindow time.Duration, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		codexHome:     codexHome,
		pollInterval:  pollInterval,
		recencyWindow: recencyWindow,
		logger:        logger,
	}
}

// Kind implements provider.Provider.
func (p *Provider) Kind() harness.Kind { return harness.KindCodex }

// Start implements provider.Provider. It polls CODEX_HOME on each tick,
// clears the prior Codex instance from the sink, and re-applies the current
// session set. It blocks until ctx is cancelled.
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

// poll reads the current session set and pushes updates into sink. It clears
// the prior Codex instance first so vanished sessions are removed.
func (p *Provider) poll(sink provider.Sink) {
	sessions, err := ReadSessions(p.codexHome)
	if err != nil {
		p.logger.Warn("codex: failed to read sessions", "err", err)
		return
	}

	// Clear the prior snapshot so sessions that have been deleted from disk
	// are removed from the view. Re-apply the current set below.
	sink.ClearProviderInstance(harness.KindCodex, InstanceID)

	now := time.Now()
	for _, s := range sessions {
		sink.ApplyUpdate(sessionToUpdate(s, now, p.recencyWindow))
	}
}

// sessionToUpdate converts a parsed Session into a provider.SessionUpdate.
// Source is "live" when the session's last activity is within the recency
// window, else "recent". The string values match state.SourceLive /
// state.SourceRecent; they are inlined here to keep internal/codex free of
// internal/state (the store validates/maps them on ingest).
func sessionToUpdate(s Session, now time.Time, recencyWindow time.Duration) provider.SessionUpdate {
	src := "recent"
	if recencyWindow > 0 && now.Sub(s.LastActivity) <= recencyWindow {
		src = "live"
	}
	return provider.SessionUpdate{
		Provider:     harness.KindCodex,
		InstanceID:   InstanceID,
		InstanceName: InstanceID,
		SessionID:    s.ID,
		Title:        s.Title,
		Directory:    s.Dir,
		LastActivity: s.LastActivity,
		Created:      s.Created,
		Source:       src,
	}
}
