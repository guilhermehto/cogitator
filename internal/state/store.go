// Package state holds the in-memory model the TUI renders. Sessions enter
// the view from two sources:
//
//   - "live": observed via an SSE event (or pending-permission poll) during
//     this cogitator run. These get the full attention classification.
//   - "recent": imported from /session because they were touched
//     within the recency window. Treated as discovery context only — they
//     are still discoverable even when not actively working. Promoted to "live"
//     the moment any event arrives for them.
package state

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/discovery"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/oc"
	"github.com/guilhermehto/cogitator/internal/provider"
)

type Source string

const (
	SourceLive   Source = "live"
	SourceRecent Source = "recent"
)

type SessionView struct {
	InstanceID   string
	InstanceName string
	SessionID    string
	Title        string
	Slug         string
	Directory    string
	ParentID     string
	Agent        string
	StatusType   string
	Source       Source
	Attention    Attention
	LastActivity time.Time
	// Created is the session's initiation time, set once when opencode
	// creates the session. Used by the TUI to impose a stable row order
	// that doesn't shuffle on every message tick. Zero until the
	// /session/{id} fetch resolves for an SSE-discovered session.
	Created time.Time
	// Provider identifies which coding-agent harness produced this session.
	// Defaults to "opencode" for sessions discovered via the opencode SSE path.
	Provider harness.Kind
}

type Snapshot struct {
	Sessions             []SessionView
	UnreachableInstances []InstanceFailure
	UpdatedAt            time.Time
}

type InstanceFailure struct {
	InstanceID          string
	Host                string
	Port                int
	ConsecutiveFailures int
	LastError           time.Time
}

type sessionRow struct {
	info         oc.Session
	status       oc.Status
	hasPerm      bool
	hasQuestion  bool
	source       Source
	lastError    time.Time
	lastActivity time.Time
	// wasActive records that the session has been AttnActive since the user
	// last viewed it — i.e. the user requested something and the agent began
	// working. finished records that such an active session has since gone
	// idle. Together they drive AttnFinished: it shows only after a real
	// active→idle transition and clears when the user views the session
	// (MarkViewed) or a fresher attention label supersedes it.
	wasActive bool
	finished  bool
}

type instanceState struct {
	id                  string
	name                string
	host                string
	port                int
	client              *oc.Client
	sessions            map[string]*sessionRow
	perms               map[string]string
	questions           map[string]string
	lastError           time.Time
	consecutiveFailures int
}

// providerSessionKey is the collision-safe dedup key for provider-sourced rows.
// It combines the provider kind and the provider-scoped session id so that a
// Codex session id that happens to match an opencode session id does not shadow
// either row.
type providerSessionKey struct {
	provider  harness.Kind
	sessionID string
}

// providerRow holds the neutral state for one provider-sourced session.
// wasActive/finished mirror the sessionRow fields and drive AttnFinished for
// provider-sourced rows, derived from StatusType transitions across updates.
type providerRow struct {
	update    provider.SessionUpdate
	wasActive bool
	finished  bool
}

// providerInstanceKey identifies one instance within a provider.
type providerInstanceKey struct {
	provider   harness.Kind
	instanceID string
}

type Store struct {
	mu        sync.Mutex
	instances map[string]*instanceState
	// providerSessions holds sessions ingested via ApplyUpdate (the neutral
	// provider seam). Keyed by (provider, sessionID) for collision safety.
	providerSessions map[providerSessionKey]*providerRow
	listeners        []chan Snapshot
	now              func() time.Time
	lookupCtx        context.Context
	cfg              *config.Config
	logger           *slog.Logger
}

func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) *Store {
	if cfg == nil {
		cfg = config.Default()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{
		instances:        map[string]*instanceState{},
		providerSessions: map[providerSessionKey]*providerRow{},
		now:              time.Now,
		lookupCtx:        ctx,
		cfg:              cfg,
		logger:           logger,
	}
}

func (s *Store) Subscribe() <-chan Snapshot {
	ch := make(chan Snapshot, 4)
	s.mu.Lock()
	s.listeners = append(s.listeners, ch)
	s.mu.Unlock()
	s.publish()
	return ch
}

func (s *Store) AddInstance(inst discovery.Instance) {
	s.mu.Lock()
	if _, ok := s.instances[inst.ID]; !ok {
		s.instances[inst.ID] = &instanceState{
			id:        inst.ID,
			name:      inst.ID,
			host:      inst.Host,
			port:      inst.Port,
			client:    oc.NewClient(inst.BaseURL(), s.cfg),
			sessions:  map[string]*sessionRow{},
			perms:     map[string]string{},
			questions: map[string]string{},
		}
	}
	s.mu.Unlock()
	s.publish()
}

func (s *Store) RemoveInstance(id string) {
	s.mu.Lock()
	delete(s.instances, id)
	s.mu.Unlock()
	s.publish()
}

func (s *Store) RecordInstanceError(id string, err error) {
	if err == nil {
		return
	}
	now := s.now()
	changed := false
	s.mu.Lock()
	if inst := s.instances[id]; inst != nil {
		inst.consecutiveFailures++
		inst.lastError = now
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func (s *Store) RecordInstanceSuccess(id string) {
	changed := false
	s.mu.Lock()
	if inst := s.instances[id]; inst != nil {
		if inst.consecutiveFailures != 0 {
			inst.consecutiveFailures = 0
			changed = true
		}
		if !inst.lastError.IsZero() {
			inst.lastError = time.Time{}
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

// SyncRecent imports sessions from a recency-window /session
// fetch. New rows land as "recent"; rows already present (live OR recent)
// just get fresher metadata merged in.
func (s *Store) SyncRecent(instanceID string, sessions []oc.Session) {
	s.mu.Lock()
	inst := s.instances[instanceID]
	if inst == nil {
		s.mu.Unlock()
		return
	}
	changed := false
	seen := map[string]bool{}
	for _, info := range sessions {
		seen[info.ID] = true
		row, exists := inst.sessions[info.ID]
		if !exists {
			row = &sessionRow{source: SourceRecent}
			inst.sessions[info.ID] = row
			changed = true
		}
		if mergeSessionInfo(&row.info, info) {
			changed = true
		}
		// Seed a baseline lastActivity from the server's update timestamp so
		// the pane sorts sensibly even before any event arrives.
		if info.Time.Updated > 0 {
			ts := time.UnixMilli(info.Time.Updated)
			if ts.After(row.lastActivity) {
				row.lastActivity = ts
				changed = true
			}
		}
	}
	// Prune rows that only exist because they were previously "recent" but no
	// longer fall in the recency window.
	for sid, row := range inst.sessions {
		if row.source == SourceRecent && !seen[sid] {
			delete(inst.sessions, sid)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func (s *Store) SyncPermissions(instanceID string, perms []oc.PermissionRequest) {
	s.mu.Lock()
	inst := s.instances[instanceID]
	if inst == nil {
		s.mu.Unlock()
		return
	}
	changed := false
	newPerms := map[string]string{}
	wantSessions := map[string]bool{}
	for _, p := range perms {
		newPerms[p.ID] = p.SessionID
		wantSessions[p.SessionID] = true
	}
	if !equalStringMaps(inst.perms, newPerms) {
		inst.perms = newPerms
		changed = true
	}
	for sid := range wantSessions {
		// A pending permission promotes a session to "live" — it needs
		// attention right now regardless of how we first heard of it.
		row, created := s.touchLocked(inst, sid)
		if created {
			changed = true
		}
		if row != nil && row.source != SourceLive {
			row.source = SourceLive
			changed = true
		}
	}
	for _, row := range inst.sessions {
		hasPerm := sessionHasPermission(inst, row.info.ID)
		if row.hasPerm != hasPerm {
			row.hasPerm = hasPerm
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func (s *Store) Republish() { s.publish() }

// ApplyUpdate implements provider.Sink. It ingests a neutral SessionUpdate,
// builds a SessionView via Classify, and publishes a new snapshot if anything
// changed. The dedup key is (provider, sessionID) so sessions from different
// providers with colliding ids never shadow each other.
func (s *Store) ApplyUpdate(u provider.SessionUpdate) {
	key := providerSessionKey{provider: u.Provider, sessionID: u.SessionID}
	s.mu.Lock()
	existing, ok := s.providerSessions[key]
	updateChanged := !ok || existing.update != u
	row := &providerRow{update: u}
	if ok {
		row.wasActive = existing.wasActive
		row.finished = existing.finished
	}
	finChanged := advanceFinished(&row.wasActive, &row.finished, u.StatusType, u.HasPermission, u.HasQuestion, u.LastError, u.LastActivity)
	changed := updateChanged || finChanged
	if changed {
		s.providerSessions[key] = row
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

// RemoveProviderSession implements provider.Sink. It removes a single session
// from the provider-sourced store. A no-op if the session is not present.
func (s *Store) RemoveProviderSession(providerKind harness.Kind, instanceID, sessionID string) {
	key := providerSessionKey{provider: providerKind, sessionID: sessionID}
	s.mu.Lock()
	_, ok := s.providerSessions[key]
	if ok {
		delete(s.providerSessions, key)
	}
	s.mu.Unlock()
	if ok {
		s.publish()
	}
}

// ClearProviderInstance implements provider.Sink. It removes all sessions for
// a provider instance (e.g. when an opencode process disappears from mDNS).
func (s *Store) ClearProviderInstance(providerKind harness.Kind, instanceID string) {
	s.mu.Lock()
	changed := false
	for key, row := range s.providerSessions {
		if key.provider == providerKind && row.update.InstanceID == instanceID {
			delete(s.providerSessions, key)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

// ReplaceProviderInstance implements provider.Sink. It atomically replaces the
// full session set for (providerKind, instanceID) with updates, publishing
// exactly one snapshot when the set changes and zero when it is identical.
// Sessions belonging to other providers or instances are never touched.
func (s *Store) ReplaceProviderInstance(providerKind harness.Kind, instanceID string, updates []provider.SessionUpdate) {
	// Build the desired set indexed by sessionID for O(1) lookup.
	desired := make(map[string]provider.SessionUpdate, len(updates))
	for _, u := range updates {
		desired[u.SessionID] = u
	}

	s.mu.Lock()
	changed := false

	// Remove rows that belong to this (provider, instance) but are absent
	// from the desired set.
	for key, row := range s.providerSessions {
		if key.provider != providerKind || row.update.InstanceID != instanceID {
			continue
		}
		if _, keep := desired[key.sessionID]; !keep {
			delete(s.providerSessions, key)
			changed = true
		}
	}

	// Upsert rows that differ from what is already stored. Carry the
	// finished/wasActive transition memory forward across replacements so a
	// session that finished does not lose its badge on the next full refresh.
	for sid, u := range desired {
		key := providerSessionKey{provider: providerKind, sessionID: sid}
		existing, ok := s.providerSessions[key]
		row := &providerRow{update: u}
		if ok {
			row.wasActive = existing.wasActive
			row.finished = existing.finished
		}
		finChanged := advanceFinished(&row.wasActive, &row.finished, u.StatusType, u.HasPermission, u.HasQuestion, u.LastError, u.LastActivity)
		if !ok || existing.update != u || finChanged {
			s.providerSessions[key] = row
			changed = true
		}
	}

	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func (s *Store) ApplyEvent(instanceID string, evt oc.Event) {
	s.mu.Lock()
	inst := s.instances[instanceID]
	if inst == nil {
		s.mu.Unlock()
		return
	}
	now := s.now()
	changed := false

	// Helper used by every case: any event for a session promotes it to live.
	promote := func(sid string) *sessionRow {
		row, created := s.touchLocked(inst, sid)
		if row == nil {
			return nil
		}
		if created {
			changed = true
		}
		if row.source != SourceLive {
			row.source = SourceLive
			changed = true
		}
		return row
	}

	switch evt.Type {
	case "session.created", "session.updated":
		var p oc.SessionInfoEvt
		if err := json.Unmarshal(evt.Properties, &p); err == nil {
			row := promote(p.SessionID)
			if row != nil {
				if mergeSessionInfo(&row.info, p.Info) {
					changed = true
				}
				row.lastActivity = now
				changed = true
			}
		} else {
			s.logger.Debug("dropping event with unknown payload", "type", evt.Type, "err", err)
		}
	case "session.deleted":
		var p oc.SessionInfoEvt
		if err := json.Unmarshal(evt.Properties, &p); err == nil {
			if _, ok := inst.sessions[p.SessionID]; ok {
				delete(inst.sessions, p.SessionID)
				changed = true
			}
		} else {
			s.logger.Debug("dropping event with unknown payload", "type", evt.Type, "err", err)
		}
	case "session.status":
		var p oc.SessionStatusEvt
		if err := json.Unmarshal(evt.Properties, &p); err == nil {
			row := promote(p.SessionID)
			if row != nil {
				if row.status != p.Status {
					row.status = p.Status
					changed = true
				}
				row.lastActivity = now
				if advanceFinished(&row.wasActive, &row.finished, row.status.Type, row.hasPerm, row.hasQuestion, row.lastError, row.lastActivity) {
					changed = true
				}
				changed = true
			}
		} else {
			s.logger.Debug("dropping event with unknown payload", "type", evt.Type, "err", err)
		}
	case "session.idle":
		var p oc.SessionIDEvt
		if err := json.Unmarshal(evt.Properties, &p); err == nil {
			row := promote(p.SessionID)
			if row != nil {
				if row.status.Type == "" {
					row.status = oc.Status{Type: "idle"}
					changed = true
				}
				row.lastActivity = now
				if advanceFinished(&row.wasActive, &row.finished, row.status.Type, row.hasPerm, row.hasQuestion, row.lastError, row.lastActivity) {
					changed = true
				}
				changed = true
			}
		} else {
			s.logger.Debug("dropping event with unknown payload", "type", evt.Type, "err", err)
		}
	case "session.error":
		var p oc.SessionErrorEvt
		if err := json.Unmarshal(evt.Properties, &p); err == nil {
			row := promote(p.SessionID)
			if row != nil {
				row.lastError = now
				changed = true
			}
		} else {
			s.logger.Debug("dropping event with unknown payload", "type", evt.Type, "err", err)
		}
	case "permission.asked":
		var p oc.PermissionRequest
		if err := json.Unmarshal(evt.Properties, &p); err == nil {
			if existing, ok := inst.perms[p.ID]; !ok || existing != p.SessionID {
				inst.perms[p.ID] = p.SessionID
				changed = true
			}
			row := promote(p.SessionID)
			if row != nil && !row.hasPerm {
				row.hasPerm = true
				changed = true
			}
		} else {
			s.logger.Debug("dropping event with unknown payload", "type", evt.Type, "err", err)
		}
	case "permission.replied":
		var p oc.PermissionRepliedEvt
		if err := json.Unmarshal(evt.Properties, &p); err == nil {
			if _, ok := inst.perms[p.RequestID]; ok {
				delete(inst.perms, p.RequestID)
				changed = true
			}
			if row, ok := inst.sessions[p.SessionID]; ok {
				hasPerm := sessionHasPermission(inst, p.SessionID)
				if row.hasPerm != hasPerm {
					row.hasPerm = hasPerm
					changed = true
				}
			}
		} else {
			s.logger.Debug("dropping event with unknown payload", "type", evt.Type, "err", err)
		}
	case "message.updated", "message.part.updated", "message.part.delta":
		var p struct {
			SessionID string `json:"sessionID"`
			Info      *struct {
				SessionID string `json:"sessionID"`
			} `json:"info"`
			Part *struct {
				SessionID string `json:"sessionID"`
				Type      string `json:"type"`
				Tool      string `json:"tool"`
				CallID    string `json:"callID"`
				State     *struct {
					Status string `json:"status"`
				} `json:"state"`
			} `json:"part"`
		}
		if err := json.Unmarshal(evt.Properties, &p); err == nil {
			sid := p.SessionID
			if sid == "" && p.Info != nil {
				sid = p.Info.SessionID
			}
			if sid == "" && p.Part != nil {
				sid = p.Part.SessionID
			}
			if sid != "" {
				row := promote(sid)
				if row != nil {
					if row.lastActivity.IsZero() || now.Sub(row.lastActivity) >= s.cfg.MessageActivityDebounce {
						row.lastActivity = now
						changed = true
					}
				}
				if p.Part != nil && p.Part.Type == "tool" && p.Part.Tool == "question" && p.Part.CallID != "" && p.Part.State != nil {
					affectedSessions := map[string]bool{sid: true}
					switch p.Part.State.Status {
					case "pending":
						if existingSID, ok := inst.questions[p.Part.CallID]; !ok || existingSID != sid {
							if ok && existingSID != "" {
								affectedSessions[existingSID] = true
							}
							inst.questions[p.Part.CallID] = sid
							changed = true
						}
					case "completed", "error":
						if existingSID, ok := inst.questions[p.Part.CallID]; ok {
							delete(inst.questions, p.Part.CallID)
							affectedSessions[existingSID] = true
							changed = true
						}
					}
					for affectedSID := range affectedSessions {
						if questionRow, ok := inst.sessions[affectedSID]; ok {
							hasQuestion := sessionHasQuestion(inst, affectedSID)
							if questionRow.hasQuestion != hasQuestion {
								questionRow.hasQuestion = hasQuestion
								changed = true
							}
						}
					}
				}
			}
		} else {
			s.logger.Debug("dropping event with unknown payload", "type", evt.Type, "err", err)
		}
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func mergeSessionInfo(dst *oc.Session, src oc.Session) bool {
	changed := false
	if src.ID != "" {
		if dst.ID != src.ID {
			changed = true
		}
		dst.ID = src.ID
	}
	if src.Slug != "" {
		if dst.Slug != src.Slug {
			changed = true
		}
		dst.Slug = src.Slug
	}
	if src.Title != "" {
		if dst.Title != src.Title {
			changed = true
		}
		dst.Title = src.Title
	}
	if src.Directory != "" {
		if dst.Directory != src.Directory {
			changed = true
		}
		dst.Directory = src.Directory
	}
	if src.ParentID != "" {
		if dst.ParentID != src.ParentID {
			changed = true
		}
		dst.ParentID = src.ParentID
	}
	if src.Agent != "" {
		if dst.Agent != src.Agent {
			changed = true
		}
		dst.Agent = src.Agent
	}
	if src.Time.Created > 0 {
		if dst.Time.Created != src.Time.Created {
			changed = true
		}
		dst.Time.Created = src.Time.Created
	}
	if src.Time.Updated > 0 {
		if dst.Time.Updated != src.Time.Updated {
			changed = true
		}
		dst.Time.Updated = src.Time.Updated
	}
	return changed
}

func (s *Store) touchLocked(inst *instanceState, sid string) (*sessionRow, bool) {
	if sid == "" {
		return nil, false
	}
	row, ok := inst.sessions[sid]
	if ok {
		return row, false
	}
	row = &sessionRow{}
	row.info.ID = sid
	inst.sessions[sid] = row
	go s.fetchSessionInfo(inst.id, sid)
	return row, true
}

func (s *Store) fetchSessionInfo(instanceID, sid string) {
	s.mu.Lock()
	inst := s.instances[instanceID]
	var client *oc.Client
	if inst != nil {
		client = inst.client
	}
	s.mu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.lookupCtx, s.cfg.SessionLookupTimeout)
	defer cancel()
	info, err := client.GetSession(ctx, sid)
	if err != nil {
		s.logger.Warn("session lookup failed", "instance", instanceID, "session", sid, "err", err)
		return
	}
	changed := false
	s.mu.Lock()
	inst = s.instances[instanceID]
	if inst != nil {
		if row, ok := inst.sessions[sid]; ok {
			if mergeSessionInfo(&row.info, info) {
				changed = true
			}
		}
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func sessionHasPermission(inst *instanceState, sid string) bool {
	for _, s := range inst.perms {
		if s == sid {
			return true
		}
	}
	return false
}

func sessionHasQuestion(inst *instanceState, sid string) bool {
	for _, s := range inst.questions {
		if s == sid {
			return true
		}
	}
	return false
}

// advanceFinished updates the (wasActive, finished) transition memory for one
// session given its freshly-computed attention inputs. It returns true if
// either flag changed.
//
// Rules:
//   - Seeing the session AttnActive marks wasActive (the user requested
//     something and the agent is working) and clears any stale finished flag.
//   - A blocking/error label (permission, question, errored) is the live
//     reason to look — it supersedes "finished"; we clear finished and leave
//     wasActive untouched so the eventual return to idle still counts.
//   - Going idle (AttnInactive) after having been active flips finished on and
//     consumes wasActive.
// finishedOr promotes a plain-inactive attention to AttnFinished when the
// session's finished flag is set. Any non-inactive label (active, permission,
// question, errored) is the live reason to look and is returned unchanged.
func finishedOr(attn Attention, finished bool) Attention {
	if finished && attn == AttnInactive {
		return AttnFinished
	}
	return attn
}

func advanceFinished(wasActive, finished *bool, statusType string, hasPerm, hasQuestion bool, lastError, lastActivity time.Time) bool {
	attn := Classify(statusType, hasPerm, hasQuestion, lastError, lastActivity)
	before := *wasActive
	beforeFin := *finished
	switch attn {
	case AttnActive:
		*wasActive = true
		*finished = false
	case AttnPermissionPending, AttnQuestionPending, AttnErrored:
		*finished = false
	case AttnInactive:
		if *wasActive {
			*finished = true
			*wasActive = false
		}
	}
	return *wasActive != before || *finished != beforeFin
}

// MarkViewed clears the finished/wasActive transition memory for one session,
// across both the opencode pipeline and the provider seam. It is the
// user-viewed signal: once the user opens a session (via cogitator's
// jump/resume), its "work finished" badge goes away regardless of whether they
// acted on it. A no-op (no publish) when nothing matched or changed.
func (s *Store) MarkViewed(providerKind harness.Kind, instanceID, sessionID string) {
	if sessionID == "" {
		return
	}
	changed := false
	clearRow := func(row *sessionRow) {
		if row.finished || row.wasActive {
			row.finished = false
			row.wasActive = false
			changed = true
		}
	}
	s.mu.Lock()
	if instanceID != "" {
		if inst := s.instances[instanceID]; inst != nil {
			if row, ok := inst.sessions[sessionID]; ok {
				clearRow(row)
			}
		}
	} else {
		// The workspace Row carries no instance id, so clear the session
		// wherever it lives across opencode instances (the same session id can
		// appear under several instances sharing a project database).
		for _, inst := range s.instances {
			if row, ok := inst.sessions[sessionID]; ok {
				clearRow(row)
			}
		}
	}
	key := providerSessionKey{provider: providerKind, sessionID: sessionID}
	if prow, ok := s.providerSessions[key]; ok {
		if prow.finished || prow.wasActive {
			prow.finished = false
			prow.wasActive = false
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (s *Store) publish() {
	snap := s.snapshot()
	s.mu.Lock()
	listeners := append([]chan Snapshot(nil), s.listeners...)
	s.mu.Unlock()
	for _, ch := range listeners {
		select {
		case ch <- snap:
		default:
		}
	}
}

func (s *Store) snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	threshold := s.cfg.UnreachableThreshold
	if threshold < 1 {
		threshold = 1
	}

	unreachable := make([]InstanceFailure, 0)
	for _, inst := range s.instances {
		if inst.consecutiveFailures >= threshold {
			unreachable = append(unreachable, InstanceFailure{
				InstanceID:          inst.id,
				Host:                inst.host,
				Port:                inst.port,
				ConsecutiveFailures: inst.consecutiveFailures,
				LastError:           inst.lastError,
			})
		}
	}
	sort.Slice(unreachable, func(i, j int) bool {
		if unreachable[i].Host != unreachable[j].Host {
			return unreachable[i].Host < unreachable[j].Host
		}
		if unreachable[i].Port != unreachable[j].Port {
			return unreachable[i].Port < unreachable[j].Port
		}
		return unreachable[i].InstanceID < unreachable[j].InstanceID
	})

	// Multiple opencode processes started in the same project directory
	// expose the same project-scoped session list, so the same SessionID
	// can appear under several InstanceStates. Dedupe to one row per
	// (provider, sessionID) pair — the provider dimension prevents a Codex
	// session id that collides with an opencode session id from shadowing
	// either row. Within the opencode provider, prefer the live source (it
	// has the SSE-derived status/activity the recent-only row lacks); within
	// the same source, pick the row with the most recent activity.
	type candidate struct {
		view    SessionView
		live    bool
		healthy bool
	}
	best := map[providerSessionKey]candidate{}

	// opencode-sourced sessions (existing oc pipeline).
	for _, inst := range s.instances {
		healthy := inst.consecutiveFailures < threshold
		for _, row := range inst.sessions {
			var created time.Time
			if row.info.Time.Created > 0 {
				created = time.UnixMilli(row.info.Time.Created)
			}
			sv := SessionView{
				InstanceID:   inst.id,
				InstanceName: inst.name,
				SessionID:    row.info.ID,
				Title:        row.info.Title,
				Slug:         row.info.Slug,
				Directory:    row.info.Directory,
				ParentID:     row.info.ParentID,
				Agent:        row.info.Agent,
				StatusType:   row.status.Type,
				Source:       row.source,
				Attention:    finishedOr(Classify(row.status.Type, row.hasPerm, row.hasQuestion, row.lastError, row.lastActivity), row.finished),
				LastActivity: row.lastActivity,
				Created:      created,
				Provider:     harness.Kind("opencode"),
			}
			key := providerSessionKey{provider: sv.Provider, sessionID: sv.SessionID}
			cand := candidate{view: sv, live: row.source == SourceLive, healthy: healthy}
			cur, ok := best[key]
			if !ok {
				best[key] = cand
				continue
			}
			if cand.healthy != cur.healthy {
				if cand.healthy {
					best[key] = cand
				}
				continue
			}
			if cand.live && !cur.live {
				best[key] = cand
				continue
			}
			if cand.live == cur.live && sv.LastActivity.After(cur.view.LastActivity) {
				best[key] = cand
			}
		}
	}

	// Provider-sourced sessions (neutral seam: Codex, future providers).
	// Each update is already the authoritative state for its (provider,
	// sessionID) pair — no multi-instance dedup needed here.
	for key, row := range s.providerSessions {
		u := row.update
		src := Source(u.Source)
		if src != SourceLive && src != SourceRecent {
			src = SourceRecent
		}
		instanceName := u.InstanceName
		if instanceName == "" {
			instanceName = u.InstanceID
		}
		sv := SessionView{
			InstanceID:   u.InstanceID,
			InstanceName: instanceName,
			SessionID:    u.SessionID,
			Title:        u.Title,
			Slug:         u.Slug,
			Directory:    u.Directory,
			ParentID:     u.ParentID,
			Agent:        u.Agent,
			StatusType:   u.StatusType,
			Source:       src,
			Attention:    finishedOr(Classify(u.StatusType, u.HasPermission, u.HasQuestion, u.LastError, u.LastActivity), row.finished),
			LastActivity: u.LastActivity,
			Created:      u.Created,
			Provider:     u.Provider,
		}
		best[key] = candidate{view: sv, live: src == SourceLive, healthy: true}
	}

	rows := make([]SessionView, 0, len(best))
	for _, c := range best {
		rows = append(rows, c.view)
	}
	// Sort deterministically with a mixed provider+instance id set.
	// Primary: provider kind (groups all opencode rows before all codex rows,
	// or vice versa — stable alphabetic order). Secondary: instance id within
	// the provider (groups host:port opencode instances; "codex" forms one
	// stable group regardless of string-compare against host:port values).
	// Tertiary: most-recent activity DESC; tie-break by session id ASC.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		if rows[i].InstanceID != rows[j].InstanceID {
			return rows[i].InstanceID < rows[j].InstanceID
		}
		if rows[i].LastActivity.Equal(rows[j].LastActivity) {
			return rows[i].SessionID < rows[j].SessionID
		}
		return rows[i].LastActivity.After(rows[j].LastActivity)
	})
	return Snapshot{Sessions: rows, UnreachableInstances: unreachable, UpdatedAt: now}
}
