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
