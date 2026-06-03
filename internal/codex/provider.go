package codex

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// InstanceID is the synthetic instance identifier used for all Codex sessions.
// It is a stable constant so the (provider, sessionID) dedup key in the store
// never collides with opencode's "host:port" instance ids.
const InstanceID = "codex"

// hookOverlay holds the hook-driven attention state for one session.
// It is overlaid on top of the poll-derived base fields when emitting updates.
type hookOverlay struct {
	// statusType is the hook-driven status ("busy", "idle", etc.).
	// Empty means "no hook override" — the poll value is used.
	statusType string

	// hasPermission is true when a PermissionRequest hook has fired and has
	// not yet been cleared by a subsequent Stop/SessionStart.
	hasPermission bool

	// lastError is set when an error-indicator hook fires.
	lastError time.Time

	// lastActivity is the time of the most recent hook event for this session.
	lastActivity time.Time
}

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

	// mu guards sessions and overlays.
	mu       sync.Mutex
	sessions map[string]Session     // keyed by session ID
	overlays map[string]hookOverlay // keyed by session ID
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
		sessions:      make(map[string]Session),
		overlays:      make(map[string]hookOverlay),
	}
}

// Kind implements provider.Provider.
func (p *Provider) Kind() harness.Kind { return harness.KindCodex }

// Start implements provider.Provider. It starts the IPC hook listener (if the
// socket is available) and polls CODEX_HOME on each tick. It blocks until ctx
// is cancelled.
func (p *Provider) Start(ctx context.Context, sink provider.Sink) error {
	// Attempt to start the hook listener. If another cogitator instance already
	// owns the socket, log and continue without live hook attention.
	sockPath := HookSocketPath()
	cleanup, listenErr := Listen(ctx, sockPath, func(raw []byte) {
		p.handleHookFrame(raw, sink)
	}, p.logger)

	if listenErr != nil {
		if errors.Is(listenErr, ErrListenerOwned) {
			p.logger.Info("codex hook: another cogitator instance owns the socket; running without live hook attention",
				"path", sockPath)
		} else {
			p.logger.Warn("codex hook: failed to start listener; running without live hook attention",
				"path", sockPath, "err", listenErr)
		}
	} else {
		defer cleanup()
		p.logger.Debug("codex hook: listener started", "path", sockPath)
	}

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

// poll reads the current session set from disk, merges with the in-memory
// overlay map, and pushes updates into sink.
//
// Poll-vs-hook merge strategy:
//   - The poll refreshes title/dir/lastActivity/existence from disk.
//   - The hook overlay (statusType, hasPermission, lastError) is preserved
//     across poll cycles — a poll tick NEVER wipes hook-driven attention.
//   - A session is pruned only when it is BOTH absent from disk AND has no
//     live hook overlay (empty overlay with zero lastActivity). This prevents
//     a hook that arrives before the rollout file is flushed from being wiped
//     by the next poll.
func (p *Provider) poll(sink provider.Sink) {
	sessions, err := ReadSessions(p.codexHome)
	if err != nil {
		p.logger.Warn("codex: failed to read sessions", "err", err)
		return
	}
	p.pollOnce(sink, sessions)
}

// pollOnce is the testable core of poll. It accepts the already-read session
// slice so tests can call it deterministically without touching the filesystem.
func (p *Provider) pollOnce(sink provider.Sink, sessions []Session) {
	p.mu.Lock()

	// Build a set of current session IDs from disk.
	current := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		current[s.ID] = struct{}{}
		p.sessions[s.ID] = s
	}

	// Prune sessions that have disappeared from disk, but only when they also
	// have no live hook overlay. A hook-seeded entry (not yet on disk) must
	// survive until its overlay goes stale or a subsequent poll finds it on disk.
	for id := range p.sessions {
		if _, onDisk := current[id]; onDisk {
			continue
		}
		ov := p.overlays[id]
		if ov.lastActivity.IsZero() && !ov.hasPermission && ov.statusType == "" && ov.lastError.IsZero() {
			delete(p.sessions, id)
			delete(p.overlays, id)
		}
		// Otherwise: keep the hook-seeded entry alive until the next disk flush.
	}

	// Snapshot the merged state for emission (under the lock).
	type mergedEntry struct {
		session Session
		overlay hookOverlay
	}
	merged := make([]mergedEntry, 0, len(p.sessions))
	for id, s := range p.sessions {
		merged = append(merged, mergedEntry{session: s, overlay: p.overlays[id]})
	}

	p.mu.Unlock()

	// Clear the prior snapshot so vanished sessions are removed from the view.
	sink.ClearProviderInstance(harness.KindCodex, InstanceID)

	now := time.Now()
	for _, e := range merged {
		sink.ApplyUpdate(p.mergeToUpdate(e.session, e.overlay, now))
	}
}

// handleHookFrame parses a raw hook frame, updates the in-memory overlay for
// the affected session, and emits a SessionUpdate immediately so the UI
// reflects the change without waiting for the next poll tick.
func (p *Provider) handleHookFrame(raw []byte, sink provider.Sink) {
	ev, err := ParseHookEvent(raw)
	if err != nil {
		p.logger.Warn("codex hook: parse event", "err", err)
		return
	}
	if ev.EventName == "" {
		p.logger.Debug("codex hook: ignoring event with no name")
		return
	}

	// Resolve the session key: prefer session ID, fall back to CWD lookup.
	sessionID := ev.SessionID
	if sessionID == "" && ev.CWD != "" {
		sessionID = p.sessionIDForCWD(ev.CWD)
	}
	if sessionID == "" {
		p.logger.Debug("codex hook: cannot resolve session id; ignoring event", "event", ev.EventName)
		return
	}

	now := time.Now()

	p.mu.Lock()
	ov := p.overlays[sessionID]
	ov.lastActivity = now

	switch ev.EventName {
	case "session_start", "user_prompt_submit", "pre_tool_use", "post_tool_use":
		ov.statusType = "busy"
		ov.hasPermission = false

	case "stopped":
		ov.statusType = "idle"
		ov.hasPermission = false

	case "permission_request":
		ov.hasPermission = true

	case "notification":
		// Notification → awaiting/attention; keep existing statusType but
		// ensure the session is not marked as actively busy.
		if ov.statusType == "busy" {
			ov.statusType = "idle"
		}
	}

	if ev.IsError {
		ov.lastError = now
	}

	p.overlays[sessionID] = ov

	// Seed a minimal p.sessions entry when the hook arrives before the rollout
	// file is flushed to disk. This ensures the next poll cycle includes the
	// session in its merge and does not drop the live hook overlay.
	if _, known := p.sessions[sessionID]; !known {
		p.sessions[sessionID] = Session{ID: sessionID, Dir: ev.CWD, LastActivity: now}
	}

	sess := p.sessions[sessionID]
	p.mu.Unlock()

	sink.ApplyUpdate(p.mergeToUpdate(sess, ov, now))
}

// sessionIDForCWD returns the session ID whose Dir matches cwd, or "" if none.
// Must NOT be called with p.mu held.
func (p *Provider) sessionIDForCWD(cwd string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, s := range p.sessions {
		if s.Dir == cwd {
			return id
		}
	}
	return ""
}

// mergeToUpdate builds a provider.SessionUpdate from a poll-derived Session
// and its hook overlay. The hook overlay's statusType/hasPermission/lastError
// take precedence over the poll-derived defaults.
func (p *Provider) mergeToUpdate(s Session, ov hookOverlay, now time.Time) provider.SessionUpdate {
	src := "recent"
	if p.recencyWindow > 0 && now.Sub(s.LastActivity) <= p.recencyWindow {
		src = "live"
	}
	// If a hook has fired recently, the session is live regardless of mtime.
	if !ov.lastActivity.IsZero() && p.recencyWindow > 0 && now.Sub(ov.lastActivity) <= p.recencyWindow {
		src = "live"
	}

	statusType := ov.statusType // hook overlay wins
	if statusType == "" {
		// No hook override — derive from recency (matches Phase B behaviour).
		if src == "live" {
			statusType = "busy"
		}
	}

	// Use the more recent of poll and hook lastActivity.
	lastActivity := s.LastActivity
	if ov.lastActivity.After(lastActivity) {
		lastActivity = ov.lastActivity
	}

	return provider.SessionUpdate{
		Provider:      harness.KindCodex,
		InstanceID:    InstanceID,
		InstanceName:  InstanceID,
		SessionID:     s.ID,
		Title:         s.Title,
		Directory:     s.Dir,
		StatusType:    statusType,
		HasPermission: ov.hasPermission,
		LastError:     ov.lastError,
		LastActivity:  lastActivity,
		Created:       s.Created,
		Source:        src,
	}
}
