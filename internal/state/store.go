// Package state holds the in-memory model the TUI renders. Sessions enter
// the view from two sources:
//
//   - "live": observed via an SSE event (or pending-permission poll) during
//     this monitor run. These get the full attention classification.
//   - "recent": imported from /session because they were touched
//     within the recency window. Treated as discovery context only — they
//     are still discoverable even when not actively working. Promoted to "live"
//     the moment any event arrives for them.
package state

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/goliveira/opencode-monitor/internal/discovery"
	"github.com/goliveira/opencode-monitor/internal/oc"
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
}

type Snapshot struct {
	Sessions  []SessionView
	UpdatedAt time.Time
}

type sessionRow struct {
	info         oc.Session
	status       oc.Status
	hasPerm      bool
	hasQuestion  bool
	source       Source
	lastError    time.Time
	lastActivity time.Time
}

type instanceState struct {
	id        string
	name      string
	client    *oc.Client
	sessions  map[string]*sessionRow
	perms     map[string]string
	questions map[string]string
}

const messageActivityDebounce = 250 * time.Millisecond

type Store struct {
	mu        sync.Mutex
	instances map[string]*instanceState
	listeners []chan Snapshot
	now       func() time.Time
	lookupCtx context.Context
}

func New(ctx context.Context) *Store {
	return &Store{
		instances: map[string]*instanceState{},
		now:       time.Now,
		lookupCtx: ctx,
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
			client:    oc.NewClient(inst.BaseURL()),
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
		if json.Unmarshal(evt.Properties, &p) == nil {
			row := promote(p.SessionID)
			if row != nil {
				if mergeSessionInfo(&row.info, p.Info) {
					changed = true
				}
				row.lastActivity = now
				changed = true
			}
		}
	case "session.deleted":
		var p oc.SessionInfoEvt
		if json.Unmarshal(evt.Properties, &p) == nil {
			if _, ok := inst.sessions[p.SessionID]; ok {
				delete(inst.sessions, p.SessionID)
				changed = true
			}
		}
	case "session.status":
		var p oc.SessionStatusEvt
		if json.Unmarshal(evt.Properties, &p) == nil {
			row := promote(p.SessionID)
			if row != nil {
				if row.status != p.Status {
					row.status = p.Status
					changed = true
				}
				row.lastActivity = now
				changed = true
			}
		}
	case "session.idle":
		var p oc.SessionIDEvt
		if json.Unmarshal(evt.Properties, &p) == nil {
			row := promote(p.SessionID)
			if row != nil {
				if row.status.Type == "" {
					row.status = oc.Status{Type: "idle"}
					changed = true
				}
				row.lastActivity = now
				changed = true
			}
		}
	case "session.error":
		var p oc.SessionErrorEvt
		if json.Unmarshal(evt.Properties, &p) == nil {
			row := promote(p.SessionID)
			if row != nil {
				row.lastError = now
				changed = true
			}
		}
	case "permission.asked":
		var p oc.PermissionRequest
		if json.Unmarshal(evt.Properties, &p) == nil {
			if existing, ok := inst.perms[p.ID]; !ok || existing != p.SessionID {
				inst.perms[p.ID] = p.SessionID
				changed = true
			}
			row := promote(p.SessionID)
			if row != nil && !row.hasPerm {
				row.hasPerm = true
				changed = true
			}
		}
	case "permission.replied":
		var p oc.PermissionRepliedEvt
		if json.Unmarshal(evt.Properties, &p) == nil {
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
		if json.Unmarshal(evt.Properties, &p) == nil {
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
					if row.lastActivity.IsZero() || now.Sub(row.lastActivity) >= messageActivityDebounce {
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
	ctx, cancel := context.WithTimeout(s.lookupCtx, 5*time.Second)
	defer cancel()
	info, err := client.GetSession(ctx, sid)
	if err != nil {
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
	// Multiple opencode processes started in the same project directory
	// expose the same project-scoped session list, so the same SessionID
	// can appear under several InstanceStates. Dedupe to one row per
	// SessionID, preferring the live source (it has the SSE-derived
	// status/activity the recent-only row lacks). Within the same source,
	// pick the row with the most recent activity.
	type candidate struct {
		view SessionView
		live bool
	}
	best := map[string]candidate{}
	for _, inst := range s.instances {
		for _, row := range inst.sessions {
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
				Attention:    Classify(row.status.Type, row.hasPerm, row.hasQuestion, row.lastError, row.lastActivity),
				LastActivity: row.lastActivity,
			}
			cand := candidate{view: sv, live: row.source == SourceLive}
			cur, ok := best[sv.SessionID]
			if !ok {
				best[sv.SessionID] = cand
				continue
			}
			if cand.live && !cur.live {
				best[sv.SessionID] = cand
				continue
			}
			if cand.live == cur.live && sv.LastActivity.After(cur.view.LastActivity) {
				best[sv.SessionID] = cand
			}
		}
	}
	rows := make([]SessionView, 0, len(best))
	for _, c := range best {
		rows = append(rows, c.view)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].InstanceID != rows[j].InstanceID {
			return rows[i].InstanceID < rows[j].InstanceID
		}
		if rows[i].LastActivity.Equal(rows[j].LastActivity) {
			return rows[i].SessionID < rows[j].SessionID
		}
		return rows[i].LastActivity.After(rows[j].LastActivity)
	})
	return Snapshot{Sessions: rows, UpdatedAt: now}
}
