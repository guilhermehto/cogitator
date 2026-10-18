package claudecode

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/pathnorm"
	"github.com/guilhermehto/cogitator/internal/provider"
	"github.com/guilhermehto/cogitator/internal/sessioncache"
)

// InstanceID is the synthetic instance identifier used for all Claude Code
// sessions. It is a stable constant so the (provider, sessionID) dedup key in
// the store never collides with opencode's "host:port" instance ids or codex's
// "codex" id.
const InstanceID = "claude-code"

// hookOverlay holds the hook-driven attention state for one session.
// It is overlaid on top of the poll-derived base fields when emitting updates.
type hookOverlay struct {
	// statusType is the hook-driven status ("busy", "idle", etc.).
	// Empty means "no hook override" — the poll value is used.
	statusType string

	// hasPermission is true when a PermissionRequest hook has fired and has
	// not yet been cleared by a subsequent Stop/SessionStart.
	hasPermission bool

	// hasQuestion is true when a PreToolUse/AskUserQuestion hook has fired and
	// has not yet been cleared by a subsequent UserPromptSubmit/Stop/SessionEnd.
	// When hasQuestion is true, a concurrent Notification/permission_prompt is
	// the question's own paired frame — it must NOT set hasPermission.
	hasQuestion bool

	// lastActivity is the time of the most recent hook event for this session.
	lastActivity time.Time

	// endedAt is the time the session left its process: a SessionEnd hook, or
	// another session claiming its pane. Zero when it has not ended.
	endedAt time.Time

	// paneID is the multiplexer pane the latest stamped hook ran in.
	paneID string
}

// Provider polls ~/.claude on a configurable interval and emits
// provider.SessionUpdates into a Sink. It implements provider.Provider.
//
// The package is intentionally free of internal/ui, bubbletea, internal/oc,
// and internal/state — it is a pure filesystem-in / sink-out adapter.
type Provider struct {
	claudeHome    string
	pollInterval  time.Duration
	recencyWindow time.Duration
	logger        *slog.Logger
	transcripts   sessioncache.Cache[Session]

	// mu guards sessions and overlays.
	mu       sync.Mutex
	sessions map[string]Session     // keyed by session ID
	overlays map[string]hookOverlay // keyed by session ID
}

// NewProvider constructs a Claude Code Provider. claudeHome may be empty
// (defaults to ~/.claude via ReadSessions). pollInterval and recencyWindow must
// be positive; callers should pass values from config.Config.
func NewProvider(claudeHome string, pollInterval, recencyWindow time.Duration, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		claudeHome:    claudeHome,
		pollInterval:  pollInterval,
		recencyWindow: recencyWindow,
		logger:        logger,
		sessions:      make(map[string]Session),
		overlays:      make(map[string]hookOverlay),
	}
}

// Kind implements provider.Provider.
func (p *Provider) Kind() harness.Kind { return harness.KindClaudeCode }

// Start implements provider.Provider. It starts the IPC hook listener (if the
// socket is available) and polls ~/.claude on each tick. It blocks until ctx
// is cancelled.
func (p *Provider) Start(ctx context.Context, sink provider.Sink) error {
	// Attempt to start the hook listener. If another cogitator instance already
	// owns the socket, log and continue without live hook attention.
	sockPath := HookSocketPath()
	cleanup, listenErr := ListenHooks(ctx, func(raw []byte) {
		p.handleHookFrame(raw, sink)
	}, p.logger)

	if listenErr != nil {
		if errors.Is(listenErr, ErrListenerOwned) {
			p.logger.Info("claude-code hook: another cogitator instance owns the socket; running without live hook attention",
				"path", sockPath)
		} else {
			p.logger.Warn("claude-code hook: failed to start listener; running without live hook attention",
				"path", sockPath, "err", listenErr)
		}
	} else {
		defer cleanup()
		p.logger.Debug("claude-code hook: listener started", "path", sockPath)
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
//   - The hook overlay (statusType, hasPermission) is preserved across poll
//     cycles — a poll tick NEVER wipes hook-driven attention.
//   - A session absent from disk is pruned once its overlay's lastActivity is
//     zero or older than recencyWindow. This prevents a hook that arrives
//     before the transcript file is flushed from being wiped by the next poll,
//     while ensuring stale phantom sessions do not leak for process lifetime.
func (p *Provider) poll(sink provider.Sink) {
	sessions, err := readSessions(p.claudeHome, &p.transcripts)
	if err != nil {
		p.logger.Warn("claude-code: failed to read sessions", "err", err)
		return
	}
	p.pollOnce(sink, sessions)
}

// pollOnce is the testable core of poll. It accepts the already-read session
// slice so tests can call it deterministically without touching the filesystem.
func (p *Provider) pollOnce(sink provider.Sink, sessions []Session) {
	now := time.Now()

	p.mu.Lock()

	// Build a set of current session IDs from disk.
	current := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		current[s.ID] = struct{}{}
		p.sessions[s.ID] = s
	}

	// Prune sessions absent from disk. A hook-seeded entry must survive until
	// its overlay goes stale (lastActivity older than recencyWindow) so that a
	// hook arriving before the transcript is flushed is not immediately wiped.
	// Once stale, the phantom session is dropped regardless of overlay flags.
	for id := range p.sessions {
		if _, onDisk := current[id]; onDisk {
			continue
		}
		ov := p.overlays[id]
		stale := ov.lastActivity.IsZero() || now.Sub(ov.lastActivity) > p.recencyWindow
		if stale {
			delete(p.sessions, id)
			delete(p.overlays, id)
		}
		// Not yet stale: keep the hook-seeded entry alive until the next disk flush.
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

	// Emit the full merged snapshot atomically so the UI never sees a blank
	// intermediate state. ReplaceProviderInstance replaces the prior snapshot
	// in one shot; if merged is empty the view is cleared without a flash.
	updates := make([]provider.SessionUpdate, 0, len(merged))
	for _, e := range merged {
		updates = append(updates, p.mergeToUpdate(e.session, e.overlay, now))
	}
	sink.ReplaceProviderInstance(harness.KindClaudeCode, InstanceID, updates)
}

// handleHookFrame parses a raw hook frame, updates the in-memory overlay for
// the affected session, and emits a SessionUpdate immediately so the UI
// reflects the change without waiting for the next poll tick.
func (p *Provider) handleHookFrame(raw []byte, sink provider.Sink) {
	ev, err := ParseHookEvent(raw)
	if err != nil {
		p.logger.Warn("claude-code hook: parse event", "err", err)
		return
	}
	if ev.EventName == "" {
		p.logger.Debug("claude-code hook: ignoring event with no name")
		return
	}

	// Resolve the session key: prefer session ID, fall back to CWD lookup.
	// CWD is canonicalized via pathnorm.Canonical before the lookup so that
	// a hook-seeded row reconciles with the worktree/merge dir regardless of
	// symlink differences (e.g. /tmp vs /private/tmp on macOS).
	sessionID := ev.SessionID
	if sessionID == "" && ev.CWD != "" {
		canonCWD, canonErr := pathnorm.Canonical(ev.CWD)
		if canonErr == nil {
			sessionID = p.sessionIDForDir(canonCWD)
		}
	}
	if sessionID == "" {
		p.logger.Debug("claude-code hook: cannot resolve session id; ignoring event", "event", ev.EventName)
		return
	}

	now := time.Now()

	p.mu.Lock()
	ov := p.overlays[sessionID]
	ov.paneID = cmp.Or(ev.PaneID, ov.paneID)
	if ev.EventName == "SessionEnd" {
		ov.endedAt = now
	} else {
		ov.lastActivity = now
	}

	switch ev.EventName {
	case "SessionStart":
		// A session starting, resuming, or clearing is NOT generating — Claude
		// is parked at the prompt waiting for the user's first input. Marking it
		// "busy" here pinned freshly opened/resumed sessions to "active" with no
		// Stop to follow (a bare resume emits no Stop), so they stuck active.
		// "active" means in-flight work only (see
		// .scriptorum/redefine-session-activity); treat start as idle and let the
		// first UserPromptSubmit/PreToolUse flip it to busy.
		ov.statusType = "idle"
		ov.hasPermission = false
		ov.hasQuestion = false

	case "PostToolUse":
		ov.statusType = "busy"
		ov.hasPermission = false
		ov.hasQuestion = false

	case "UserPromptSubmit":
		// UserPromptSubmit is the user's answer to a question — clears question state.
		ov.statusType = "busy"
		ov.hasPermission = false
		ov.hasQuestion = false

	case "PreToolUse":
		ov.statusType = "busy"
		if ev.IsQuestionTool() {
			// AskUserQuestion: signal a question pending, not a permission prompt.
			ov.hasQuestion = true
			ov.hasPermission = false
		} else {
			ov.hasPermission = false
			ov.hasQuestion = false
		}

	case "Stop", "SessionEnd":
		// Teardown: clear busy→idle. The row is NOT removed; the transcript
		// persists on disk and a subsequent poll will keep the session listed,
		// as recent rather than live once SessionEnd has ended it.
		ov.statusType = "idle"
		ov.hasPermission = false
		ov.hasQuestion = false

	case "PermissionRequest":
		ov.hasPermission = true

	case "Notification":
		// Notification with permission_prompt may be either:
		//   (a) a real permission prompt — set hasPermission=true, or
		//   (b) the paired frame from AskUserQuestion — leave as question.
		// Distinguish by whether hasQuestion is already set: if a question is
		// pending, this Notification is the question's own prompt frame.
		if ev.HasPermission() {
			if !ov.hasQuestion {
				ov.hasPermission = true
			}
			// When hasQuestion is already true, do not set hasPermission —
			// the question glyph takes precedence and the lock must not appear.
		}
		// Keep existing statusType but ensure the session is not marked as
		// actively busy if it was already idle.
		if ov.statusType == "busy" && !ev.HasPermission() {
			ov.statusType = "idle"
		}
	}

	// Write the mutated overlay back so the next poll cycle sees the updated
	// hasPermission/statusType — without this the overlay map stays permanently
	// empty and hook-driven state is wiped on every poll tick.
	p.overlays[sessionID] = ov

	// Seed a minimal p.sessions entry when the hook arrives before the
	// transcript file is flushed to disk. CWD is canonicalized so the seeded
	// Dir reconciles with worktree/merge paths (improvement over codex's
	// raw-cwd seed).
	if _, known := p.sessions[sessionID]; !known {
		dir := ev.CWD
		if dir != "" {
			if canonical, canonErr := pathnorm.Canonical(dir); canonErr == nil {
				dir = canonical
			}
		}
		p.sessions[sessionID] = Session{ID: sessionID, Dir: dir, LastActivity: now}
	}

	sess := p.sessions[sessionID]

	// A SessionEnd frame can arrive after its successor's first hook in the
	// same pane (/clear fires both at once), so only a live session claims it.
	var displaced []provider.SessionUpdate
	if ev.EventName != "SessionEnd" {
		displaced = p.endDisplacedLocked(sessionID, ev.PaneID, now)
	}
	p.mu.Unlock()

	sink.ApplyUpdate(p.mergeToUpdate(sess, ov, now))
	for _, u := range displaced {
		sink.ApplyUpdate(u)
	}
}

// endDisplacedLocked ends every session other than owner last seen in paneID
// and returns their updates. A pane runs one session, so the others have left
// it: /clear replaced them, or a reused pane id now runs a new process. Their
// pane is cleared so later hooks from owner do not end them again. Must be
// called with p.mu held.
func (p *Provider) endDisplacedLocked(owner, paneID string, now time.Time) []provider.SessionUpdate {
	if paneID == "" {
		return nil
	}

	var updates []provider.SessionUpdate
	for id, ov := range p.overlays {
		if id == owner || ov.paneID != paneID {
			continue
		}

		ov.endedAt = now
		ov.paneID = ""
		p.overlays[id] = ov
		updates = append(updates, p.mergeToUpdate(p.sessions[id], ov, now))
	}
	return updates
}

// sessionIDForDir returns the session ID whose Dir matches dir (canonical
// form), or "" if none. Must NOT be called with p.mu held.
func (p *Provider) sessionIDForDir(dir string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, s := range p.sessions {
		if s.Dir == dir {
			return id
		}
	}
	return ""
}

// mergeToUpdate builds a provider.SessionUpdate from a poll-derived Session
// and its hook overlay. The hook overlay's statusType/hasPermission take
// precedence over the poll-derived defaults.
func (p *Provider) mergeToUpdate(s Session, ov hookOverlay, now time.Time) provider.SessionUpdate {
	// Use the more recent of poll and hook lastActivity.
	lastActivity := s.LastActivity
	if ov.lastActivity.After(lastActivity) {
		lastActivity = ov.lastActivity
	}
	// An end is not activity: a session that ended after its last activity is
	// over, however recently it ended.
	ended := !ov.endedAt.IsZero() && !ov.endedAt.Before(lastActivity)

	src := "recent"
	if !ended && p.recencyWindow > 0 && now.Sub(lastActivity) <= p.recencyWindow {
		src = "live"
	}

	// StatusType is hook-driven only. Recency still drives Source (for
	// visibility and sorting), but a recently-written transcript must NEVER be
	// coerced to "busy": it means the session was touched recently, not that
	// Claude is generating right now. Coercing here made every session whose
	// last transcript line fell within recencyWindow render as "active" after a
	// cogitator restart — the in-memory overlay map starts empty, so the poll
	// fallback fired for every recent session even when idle. "active" means
	// busy/generating only (see .scriptorum/redefine-session-activity); idle
	// sessions classify as inactive once the hook overlay is absent.
	statusType := ov.statusType

	// Decay a stale "busy". A hook reports "busy" when Claude starts generating
	// and the paired Stop/SessionEnd clears it — but that clearing hook is lost
	// whenever cogitator was not the socket owner at finish time, the CLI was
	// killed mid-run, or the frame was dropped. Without decay the overlay stays
	// "busy" for the life of the process, pinning the row to "active"
	// indefinitely (observed: a session idle for 12h still rendering active). A
	// real generation streams to the transcript and fires tool hooks
	// continuously, so it always refreshes activity well inside recencyWindow;
	// once nothing — transcript mtime nor hook — has advanced within the window
	// (src != "live"), "busy" can no longer mean "currently generating", so drop
	// it and let Classify fall back to inactive.
	if statusType == "busy" && src != "live" {
		statusType = ""
	}

	return provider.SessionUpdate{
		Provider:      harness.KindClaudeCode,
		InstanceID:    InstanceID,
		InstanceName:  InstanceID,
		SessionID:     s.ID,
		Title:         s.Title,
		Directory:     s.Dir,
		StatusType:    statusType,
		HasPermission: ov.hasPermission,
		HasQuestion:   ov.hasQuestion,
		LastActivity:  lastActivity,
		Created:       s.Created,
		Source:        src,
		PaneID:        ov.paneID,
	}
}
