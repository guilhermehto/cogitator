package state

// Tests for the neutral provider ingest path (ApplyUpdate / RemoveProviderSession /
// ClearProviderInstance) and the collision-safe (provider, sessionID) dedup.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/discovery"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/oc"
	"github.com/guilhermehto/cogitator/internal/provider"
)

func newProviderTestStore(ctx context.Context) *Store {
	return New(ctx, config.Default(), slog.Default())
}

// TestCollidingSessionIDsAcrossProviders feeds two providers with the same
// session id and asserts both rows survive in the snapshot (no shadowing).
func TestCollidingSessionIDsAcrossProviders(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)

	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	// opencode instance with session "shared-id".
	inst := discovery.Instance{ID: "127.0.0.1:8080", Host: "127.0.0.1", Port: 8080}
	s.AddInstance(inst)
	s.SyncRecent(inst.ID, []oc.Session{makeSession("shared-id", 1_000)})

	// codex provider with the same session id.
	s.ApplyUpdate(provider.SessionUpdate{
		Provider:     harness.Kind("codex"),
		InstanceID:   "codex",
		SessionID:    "shared-id",
		Title:        "Codex session",
		StatusType:   "busy",
		LastActivity: now,
		Source:       "live",
	})

	snap := s.snapshot()

	var ocCount, codexCount int
	for _, sv := range snap.Sessions {
		if sv.SessionID == "shared-id" {
			switch sv.Provider {
			case harness.Kind("opencode"):
				ocCount++
			case harness.Kind("codex"):
				codexCount++
				if sv.Attention != AttnActive {
					t.Errorf("codex row attention = %q, want %q", sv.Attention, AttnActive)
				}
			}
		}
	}
	if ocCount != 1 {
		t.Errorf("expected 1 opencode row for shared-id, got %d (shadowed)", ocCount)
	}
	if codexCount != 1 {
		t.Errorf("expected 1 codex row for shared-id, got %d (shadowed)", codexCount)
	}
}

// TestApplyUpdateAttentionClassify verifies that Classify is called correctly
// for provider-sourced updates (permission pending, errored, active, inactive).
func TestApplyUpdateAttentionClassify(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	cases := []struct {
		name          string
		update        provider.SessionUpdate
		wantAttention Attention
	}{
		{
			name: "permission pending",
			update: provider.SessionUpdate{
				Provider: harness.Kind("codex"), InstanceID: "codex",
				SessionID: "perm-ses", HasPermission: true, Source: "live",
				LastActivity: now,
			},
			wantAttention: AttnPermissionPending,
		},
		{
			name: "errored",
			update: provider.SessionUpdate{
				Provider: harness.Kind("codex"), InstanceID: "codex",
				SessionID: "err-ses", LastError: now, LastActivity: now.Add(-time.Second),
				Source: "live",
			},
			wantAttention: AttnErrored,
		},
		{
			name: "active",
			update: provider.SessionUpdate{
				Provider: harness.Kind("codex"), InstanceID: "codex",
				SessionID: "active-ses", StatusType: "busy", Source: "live",
				LastActivity: now,
			},
			wantAttention: AttnActive,
		},
		{
			name: "inactive",
			update: provider.SessionUpdate{
				Provider: harness.Kind("codex"), InstanceID: "codex",
				SessionID: "idle-ses", StatusType: "idle", Source: "recent",
				LastActivity: now,
			},
			wantAttention: AttnInactive,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s.ApplyUpdate(tc.update)
			snap := s.snapshot()
			var found *SessionView
			for i := range snap.Sessions {
				if snap.Sessions[i].SessionID == tc.update.SessionID {
					found = &snap.Sessions[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("session %q not found in snapshot", tc.update.SessionID)
			}
			if found.Attention != tc.wantAttention {
				t.Errorf("attention = %q, want %q", found.Attention, tc.wantAttention)
			}
		})
	}
}

// TestSnapshotSortStableWithMixedProviders verifies that rows from opencode
// (host:port instance ids) and codex (synthetic "codex" instance id) sort
// deterministically: provider ASC, then instance id ASC, then activity DESC.
func TestSnapshotSortStableWithMixedProviders(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	base := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return base }

	// Two opencode instances.
	instA := discovery.Instance{ID: "127.0.0.1:1111", Host: "127.0.0.1", Port: 1111}
	instB := discovery.Instance{ID: "127.0.0.1:2222", Host: "127.0.0.1", Port: 2222}
	s.AddInstance(instA)
	s.AddInstance(instB)
	s.SyncRecent(instA.ID, []oc.Session{makeSession("oc-ses-a", 1_000)})
	s.SyncRecent(instB.ID, []oc.Session{makeSession("oc-ses-b", 2_000)})

	// One codex session.
	s.ApplyUpdate(provider.SessionUpdate{
		Provider:     harness.Kind("codex"),
		InstanceID:   "codex",
		SessionID:    "codex-ses-1",
		StatusType:   "idle",
		LastActivity: base,
		Source:       "recent",
	})

	snap := s.snapshot()

	// Collect provider+instance groups in order.
	type groupKey struct {
		provider   harness.Kind
		instanceID string
	}
	var order []groupKey
	seen := map[groupKey]bool{}
	for _, sv := range snap.Sessions {
		gk := groupKey{sv.Provider, sv.InstanceID}
		if !seen[gk] {
			order = append(order, gk)
			seen[gk] = true
		}
	}

	// "codex" < "opencode" alphabetically, so codex group comes first.
	if len(order) < 3 {
		t.Fatalf("expected at least 3 groups, got %d: %v", len(order), order)
	}
	if order[0].provider != harness.Kind("codex") {
		t.Errorf("first group provider = %q, want %q", order[0].provider, "codex")
	}
	if order[0].instanceID != "codex" {
		t.Errorf("first group instanceID = %q, want %q", order[0].instanceID, "codex")
	}
	// opencode groups follow, sorted by instance id.
	if order[1].provider != harness.Kind("opencode") || order[1].instanceID != instA.ID {
		t.Errorf("second group = {%q, %q}, want {opencode, %q}", order[1].provider, order[1].instanceID, instA.ID)
	}
	if order[2].provider != harness.Kind("opencode") || order[2].instanceID != instB.ID {
		t.Errorf("third group = {%q, %q}, want {opencode, %q}", order[2].provider, order[2].instanceID, instB.ID)
	}
}

// TestRemoveProviderSession removes a single session and asserts it disappears.
func TestRemoveProviderSession(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	s.ApplyUpdate(provider.SessionUpdate{
		Provider: harness.Kind("codex"), InstanceID: "codex",
		SessionID: "to-remove", Source: "live", LastActivity: now,
	})
	s.ApplyUpdate(provider.SessionUpdate{
		Provider: harness.Kind("codex"), InstanceID: "codex",
		SessionID: "to-keep", Source: "live", LastActivity: now,
	})

	snap := s.snapshot()
	if len(snap.Sessions) != 2 {
		t.Fatalf("expected 2 sessions before removal, got %d", len(snap.Sessions))
	}

	s.RemoveProviderSession(harness.Kind("codex"), "codex", "to-remove")

	snap = s.snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session after removal, got %d", len(snap.Sessions))
	}
	if snap.Sessions[0].SessionID != "to-keep" {
		t.Errorf("remaining session = %q, want %q", snap.Sessions[0].SessionID, "to-keep")
	}
}

// TestClearProviderInstance removes all sessions for one instance and leaves
// sessions from other providers untouched.
func TestClearProviderInstance(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	s.ApplyUpdate(provider.SessionUpdate{
		Provider: harness.Kind("codex"), InstanceID: "codex",
		SessionID: "codex-1", Source: "live", LastActivity: now,
	})
	s.ApplyUpdate(provider.SessionUpdate{
		Provider: harness.Kind("codex"), InstanceID: "codex",
		SessionID: "codex-2", Source: "live", LastActivity: now,
	})
	// A different provider — must survive the clear.
	s.ApplyUpdate(provider.SessionUpdate{
		Provider: harness.Kind("other"), InstanceID: "other-inst",
		SessionID: "other-1", Source: "live", LastActivity: now,
	})

	s.ClearProviderInstance(harness.Kind("codex"), "codex")

	snap := s.snapshot()
	for _, sv := range snap.Sessions {
		if sv.Provider == harness.Kind("codex") {
			t.Errorf("codex session %q survived ClearProviderInstance", sv.SessionID)
		}
	}
	var otherFound bool
	for _, sv := range snap.Sessions {
		if sv.Provider == harness.Kind("other") {
			otherFound = true
		}
	}
	if !otherFound {
		t.Error("other-provider session was incorrectly cleared")
	}
}

// ── ReplaceProviderInstance tests ─────────────────────────────────────────────

// drainSnapshots reads all snapshots currently buffered in ch without blocking.
func drainSnapshots(ch <-chan Snapshot) []Snapshot {
	var out []Snapshot
	for {
		select {
		case s := <-ch:
			out = append(out, s)
		default:
			return out
		}
	}
}

// countSessionsForProvider counts sessions in snap belonging to providerKind.
func countSessionsForProvider(snap Snapshot, providerKind harness.Kind) int {
	n := 0
	for _, sv := range snap.Sessions {
		if sv.Provider == providerKind {
			n++
		}
	}
	return n
}

// makeUpdate builds a minimal SessionUpdate for (codex, "codex", sessionID).
func makeUpdate(sessionID, statusType string, lastActivity time.Time) provider.SessionUpdate {
	return provider.SessionUpdate{
		Provider:     harness.KindCodex,
		InstanceID:   "codex",
		SessionID:    sessionID,
		StatusType:   statusType,
		LastActivity: lastActivity,
		Source:       "live",
	}
}

// TestReplaceProviderInstance_SinglePublishOnChange verifies that a call with N
// updates for (codex,"codex") replacing a prior set emits exactly one snapshot
// containing all N sessions — no empty intermediate snapshot is observable.
func TestReplaceProviderInstance_SinglePublishOnChange(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	ch := s.Subscribe()
	// Drain the initial snapshot emitted by Subscribe.
	drainSnapshots(ch)

	updates := []provider.SessionUpdate{
		makeUpdate("ses-1", "busy", now),
		makeUpdate("ses-2", "idle", now),
		makeUpdate("ses-3", "busy", now),
	}
	s.ReplaceProviderInstance(harness.KindCodex, "codex", updates)

	snaps := drainSnapshots(ch)
	if len(snaps) != 1 {
		t.Fatalf("expected exactly 1 snapshot, got %d", len(snaps))
	}
	snap := snaps[0]
	if n := countSessionsForProvider(snap, harness.KindCodex); n != 3 {
		t.Errorf("snapshot has %d codex sessions, want 3", n)
	}
	// Verify no intermediate empty snapshot was published.
	for _, sv := range snap.Sessions {
		if sv.Provider == harness.KindCodex && sv.SessionID == "" {
			t.Error("snapshot contains a codex session with empty SessionID")
		}
	}
}

// TestReplaceProviderInstance_ZeroPublishWhenUnchanged verifies that calling
// ReplaceProviderInstance with an identical set emits zero snapshots.
func TestReplaceProviderInstance_ZeroPublishWhenUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	updates := []provider.SessionUpdate{
		makeUpdate("ses-1", "busy", now),
		makeUpdate("ses-2", "idle", now),
	}
	// First call — establishes the set.
	s.ReplaceProviderInstance(harness.KindCodex, "codex", updates)

	ch := s.Subscribe()
	drainSnapshots(ch) // drain initial snapshot

	// Second call with identical set — must produce zero snapshots.
	s.ReplaceProviderInstance(harness.KindCodex, "codex", updates)

	snaps := drainSnapshots(ch)
	if len(snaps) != 0 {
		t.Errorf("expected 0 snapshots for identical replace, got %d", len(snaps))
	}
}

// TestReplaceProviderInstance_PrunesRemovedSessions verifies that a prior set
// {A,B,C} replaced by {A,B} produces a snapshot omitting C and including A,B.
func TestReplaceProviderInstance_PrunesRemovedSessions(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	// Establish {A, B, C}.
	s.ReplaceProviderInstance(harness.KindCodex, "codex", []provider.SessionUpdate{
		makeUpdate("A", "idle", now),
		makeUpdate("B", "idle", now),
		makeUpdate("C", "idle", now),
	})

	ch := s.Subscribe()
	drainSnapshots(ch)

	// Replace with {A, B} — C must be pruned.
	s.ReplaceProviderInstance(harness.KindCodex, "codex", []provider.SessionUpdate{
		makeUpdate("A", "idle", now),
		makeUpdate("B", "idle", now),
	})

	snaps := drainSnapshots(ch)
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot after pruning, got %d", len(snaps))
	}
	snap := snaps[0]
	ids := map[string]bool{}
	for _, sv := range snap.Sessions {
		if sv.Provider == harness.KindCodex {
			ids[sv.SessionID] = true
		}
	}
	if ids["C"] {
		t.Error("session C survived replace — should have been pruned")
	}
	if !ids["A"] || !ids["B"] {
		t.Errorf("sessions A and B must survive; got ids=%v", ids)
	}
}

// TestReplaceProviderInstance_ScopedToProviderInstance verifies that rows for
// other providers/instances are never touched by a replace call.
func TestReplaceProviderInstance_ScopedToProviderInstance(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	// Seed a session from a different provider.
	s.ApplyUpdate(provider.SessionUpdate{
		Provider:     harness.Kind("opencode-provider"),
		InstanceID:   "other-inst",
		SessionID:    "other-ses",
		StatusType:   "idle",
		LastActivity: now,
		Source:       "live",
	})

	// Replace codex sessions — must not touch the other provider's row.
	s.ReplaceProviderInstance(harness.KindCodex, "codex", []provider.SessionUpdate{
		makeUpdate("codex-ses", "busy", now),
	})

	snap := s.snapshot()
	var otherFound bool
	for _, sv := range snap.Sessions {
		if sv.Provider == harness.Kind("opencode-provider") && sv.SessionID == "other-ses" {
			otherFound = true
		}
	}
	if !otherFound {
		t.Error("other-provider session was incorrectly removed by ReplaceProviderInstance")
	}
}

// TestReplaceProviderInstance_HookSeededSessionIdempotent verifies that a
// hook-seeded session present in two consecutive identical replaces with no new
// hook event produces zero snapshots on the second replace (struct == holds
// because timestamps are stored once and reused).
func TestReplaceProviderInstance_HookSeededSessionIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	// Use a fixed now so LastActivity is identical across both calls.
	now := time.Unix(1_700_000, 0)
	s.now = func() time.Time { return now }

	hookSeeded := provider.SessionUpdate{
		Provider:      harness.KindCodex,
		InstanceID:    "codex",
		SessionID:     "hook-ses",
		StatusType:    "busy",
		HasPermission: true,
		LastActivity:  now, // stored once, reused — struct == holds
		Source:        "live",
	}

	// First replace — establishes the hook-seeded session.
	s.ReplaceProviderInstance(harness.KindCodex, "codex", []provider.SessionUpdate{hookSeeded})

	ch := s.Subscribe()
	drainSnapshots(ch)

	// Second replace with identical update — must produce zero snapshots.
	s.ReplaceProviderInstance(harness.KindCodex, "codex", []provider.SessionUpdate{hookSeeded})

	snaps := drainSnapshots(ch)
	if len(snaps) != 0 {
		t.Errorf("expected 0 snapshots for identical hook-seeded replace, got %d", len(snaps))
	}
}

// TestOpenCodeApplyEventPathUnchanged verifies that the existing opencode
// ApplyEvent path produces identical snapshot behavior after the dedup re-key.
func TestOpenCodeApplyEventPathUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newProviderTestStore(ctx)
	inst := discovery.Instance{ID: "127.0.0.1:9999", Host: "127.0.0.1", Port: 9999}
	s.AddInstance(inst)

	s.SyncRecent(inst.ID, []oc.Session{makeSession("oc-ses", 1_000)})
	s.ApplyEvent(inst.ID, oc.Event{
		Type:       "session.status",
		Properties: mustJSON(t, oc.SessionStatusEvt{SessionID: "oc-ses", Status: oc.Status{Type: "busy"}}),
	})

	snap := s.snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(snap.Sessions))
	}
	sv := snap.Sessions[0]
	if sv.SessionID != "oc-ses" {
		t.Errorf("SessionID = %q, want %q", sv.SessionID, "oc-ses")
	}
	if sv.Provider != harness.Kind("opencode") {
		t.Errorf("Provider = %q, want %q", sv.Provider, "opencode")
	}
	if sv.Attention != AttnActive {
		t.Errorf("Attention = %q, want %q", sv.Attention, AttnActive)
	}
	if sv.Source != SourceLive {
		t.Errorf("Source = %q, want %q", sv.Source, SourceLive)
	}
}
