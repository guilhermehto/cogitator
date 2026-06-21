package omp

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/hookipc"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// InstanceID is the synthetic instance identifier used for all omp sessions.
// It is a stable constant so the (provider, sessionID) dedup key never collides
// with opencode's "host:port" instance ids.
const InstanceID = "omp"

// hookOverlay holds the hook-driven attention state for one session. It is
// overlaid on top of the poll-derived base fields when emitting updates.
type hookOverlay struct {
	// statusType is the hook-driven status ("busy", "idle"). Empty means "no
	// hook override" — the poll value is used.
	statusType string

	// hasQuestion is true when the agent invoked the "ask" tool and has not
	// yet received an answer.
	hasQuestion bool

	// lastError is set when a tool_result reports a failure.
	lastError time.Time

	// lastActivity is the time of the most recent hook event for this session.
	lastActivity time.Time
}

// Provider polls the omp agent directory on a configurable interval and emits
// provider.SessionUpdates into a Sink. A Unix-socket hook listener overlays
// real-time attention state fed by the shipped omp extension. It implements
// provider.Provider.
//
// The package is intentionally free of internal/ui, bubbletea, internal/oc,
// and internal/state — it is a pure filesystem-in / sink-out adapter.
type Provider struct {
	ompHome       string
	pollInterval  time.Duration
	recencyWindow time.Duration
	logger        *slog.Logger

	// mu guards sessions and overlays.
	mu       sync.Mutex
	sessions map[string]Session     // keyed by session ID
	overlays map[string]hookOverlay // keyed by session ID
}

// NewProvider constructs an omp Provider. ompHome may be empty (defaults to
// the resolved omp agent directory via ReadSessions). pollInterval and
// recencyWindow must be positive; callers should pass values from config.Config.
func NewProvider(ompHome string, pollInterval, recencyWindow time.Duration, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		ompHome:       ompHome,
		pollInterval:  pollInterval,
		recencyWindow: recencyWindow,
		logger:        logger,
		sessions:      make(map[string]Session),
		overlays:      make(map[string]hookOverlay),
	}
}

// Kind implements provider.Provider.
func (p *Provider) Kind() harness.Kind { return harness.KindOmp }

// Start implements provider.Provider. It starts the IPC hook listener (if the
// socket is available) and polls the omp agent directory on each tick. It
// blocks until ctx is cancelled.
func (p *Provider) Start(ctx context.Context, sink provider.Sink) error {
	sockPath := HookSocketPath()
	cleanup, listenErr := hookipc.Listen(ctx, sockPath, func(raw []byte) {
		p.handleHookFrame(raw, sink)
	}, p.logger)

	if listenErr != nil {
		if errors.Is(listenErr, hookipc.ErrListenerOwned) {
			p.logger.Info("omp hook: another cogitator instance owns the socket; running without live hook attention",
				"path", sockPath)
		} else {
			p.logger.Warn("omp hook: failed to start listener; running without live hook attention",
				"path", sockPath, "err", listenErr)
		}
	} else {
		defer cleanup()
		p.logger.Debug("omp hook: listener started", "path", sockPath)
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
// Poll-vs-hook merge strategy mirrors the Codex provider:
//   - The poll refreshes title/dir/lastActivity/existence from disk.
//   - The hook overlay (statusType, hasQuestion, lastError) is preserved
//     across poll cycles — a poll tick NEVER wipes hook-driven attention.
//   - A session is pruned only when it is BOTH absent from disk AND has no
//     live hook overlay. This prevents a hook that arrives before the session
//     file is flushed from being wiped by the next poll.
func (p *Provider) poll(sink provider.Sink) {
	sessions, err := ReadSessions(p.ompHome)
	if err != nil {
		p.logger.Warn("omp: failed to read sessions", "err", err)
		return
	}
	p.pollOnce(sink, sessions)
}

// pollOnce is the testable core of poll. It accepts the already-read session
// slice so tests can call it deterministically without touching the filesystem.
func (p *Provider) pollOnce(sink provider.Sink, sessions []Session) {
	p.mu.Lock()

	current := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		current[s.ID] = struct{}{}
		p.sessions[s.ID] = s
	}

	// Prune sessions that have disappeared from disk, but only when they also
	// have no live hook overlay.
	for id := range p.sessions {
		if _, onDisk := current[id]; onDisk {
			continue
		}
		ov := p.overlays[id]
		if ov.lastActivity.IsZero() && !ov.hasQuestion && ov.statusType == "" && ov.lastError.IsZero() {
			delete(p.sessions, id)
			delete(p.overlays, id)
		}
	}

	type mergedEntry struct {
		session Session
		overlay hookOverlay
	}
	merged := make([]mergedEntry, 0, len(p.sessions))
	for id, s := range p.sessions {
		merged = append(merged, mergedEntry{session: s, overlay: p.overlays[id]})
	}

	p.mu.Unlock()

	now := time.Now()
	updates := make([]provider.SessionUpdate, 0, len(merged))
	for _, e := range merged {
		updates = append(updates, p.mergeToUpdate(e.session, e.overlay, now))
	}
	sink.ReplaceProviderInstance(harness.KindOmp, InstanceID, updates)
}

// handleHookFrame parses a raw hook frame, updates the in-memory overlay for
// the affected session, and emits a SessionUpdate immediately so the UI
// reflects the change without waiting for the next poll tick.
func (p *Provider) handleHookFrame(raw []byte, sink provider.Sink) {
	ev, err := ParseHookEvent(raw)
	if err != nil {
		p.logger.Warn("omp hook: parse event", "err", err)
		return
	}
	if ev.EventName == "" {
		p.logger.Debug("omp hook: ignoring event with no name")
		return
	}

	sessionID := ev.SessionID
	if sessionID == "" && ev.CWD != "" {
		sessionID = p.sessionIDForCWD(ev.CWD)
	}
	if sessionID == "" {
		p.logger.Debug("omp hook: cannot resolve session id; ignoring event", "event", ev.EventName)
		return
	}

	now := time.Now()

	p.mu.Lock()
	ov := p.overlays[sessionID]
	ov.lastActivity = now

	switch ev.EventName {
	case "session_start", "turn_start", "agent_start":
		ov.statusType = "busy"
		ov.hasQuestion = false

	case "tool_call":
		ov.statusType = "busy"
		if ev.ToolName == "ask" {
			ov.hasQuestion = true
		}

	case "tool_result":
		// Answering the ask tool clears the pending question; other tool
		// results leave the busy/idle status untouched.
		if ev.ToolName == "ask" {
			ov.hasQuestion = false
		}

	case "turn_end", "agent_end", "session_shutdown":
		ov.statusType = "idle"
		ov.hasQuestion = false
	}

	if ev.IsError {
		ov.lastError = now
	}

	p.overlays[sessionID] = ov

	// Seed a minimal p.sessions entry when the hook arrives before the session
	// file is flushed to disk, so the next poll merges rather than drops it.
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

// mergeToUpdate builds a provider.SessionUpdate from a poll-derived Session and
// its hook overlay. The hook overlay's statusType/hasQuestion/lastError take
// precedence over the poll-derived defaults.
func (p *Provider) mergeToUpdate(s Session, ov hookOverlay, now time.Time) provider.SessionUpdate {
	src := "recent"
	if p.recencyWindow > 0 && now.Sub(s.LastActivity) <= p.recencyWindow {
		src = "live"
	}
	if !ov.lastActivity.IsZero() && p.recencyWindow > 0 && now.Sub(ov.lastActivity) <= p.recencyWindow {
		src = "live"
	}

	statusType := ov.statusType // hook overlay wins
	if statusType == "" && src == "live" {
		// No hook override — a recently-active session is shown as busy.
		statusType = "busy"
	}

	lastActivity := s.LastActivity
	if ov.lastActivity.After(lastActivity) {
		lastActivity = ov.lastActivity
	}

	return provider.SessionUpdate{
		Provider:     harness.KindOmp,
		InstanceID:   InstanceID,
		InstanceName: InstanceID,
		SessionID:    s.ID,
		Title:        s.Title,
		Directory:    s.Dir,
		StatusType:   statusType,
		HasQuestion:  ov.hasQuestion,
		LastError:    ov.lastError,
		LastActivity: lastActivity,
		Created:      s.Created,
		Source:       src,
	}
}
