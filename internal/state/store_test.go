package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/goliveira/opencode-monitor/internal/discovery"
	"github.com/goliveira/opencode-monitor/internal/oc"
)

func makeSession(id string, updatedMs int64) oc.Session {
	var s oc.Session
	s.ID = id
	s.Title = id
	s.Time.Updated = updatedMs
	return s
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestSyncRecentPrunesOnlyRecentRows(t *testing.T) {
	ctx := context.Background()
	s := New(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)

	s.SyncRecent(inst.ID, []oc.Session{makeSession("A", 1_000), makeSession("B", 1_000)})

	s.ApplyEvent(inst.ID, oc.Event{
		Type:       "session.status",
		Properties: mustJSON(t, oc.SessionStatusEvt{SessionID: "A", Status: oc.Status{Type: "busy"}}),
	})

	s.SyncRecent(inst.ID, []oc.Session{makeSession("A", 2_000)})

	snap := s.snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session after pruning, got %d", len(snap.Sessions))
	}
	if snap.Sessions[0].SessionID != "A" {
		t.Fatalf("expected remaining session A, got %q", snap.Sessions[0].SessionID)
	}
	if snap.Sessions[0].Source != SourceLive {
		t.Fatalf("expected remaining session to stay live, got %q", snap.Sessions[0].Source)
	}
}

func TestApplyEventUnknownTypeDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	s := New(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)

	ch := s.Subscribe()
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected initial snapshot")
	}

	s.ApplyEvent(inst.ID, oc.Event{Type: "server.heartbeat", Properties: mustJSON(t, map[string]any{})})

	select {
	case <-ch:
		t.Fatal("unexpected publish for unknown/no-op event")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSnapshotSortBreaksLastActivityTiesBySessionID(t *testing.T) {
	ctx := context.Background()
	s := New(ctx)
	inst := discovery.Instance{ID: "inst-1", Host: "127.0.0.1", Port: 1}
	s.AddInstance(inst)

	s.SyncRecent(inst.ID, []oc.Session{makeSession("b", 1_000), makeSession("a", 1_000)})

	snap := s.snapshot()
	if len(snap.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(snap.Sessions))
	}
	if snap.Sessions[0].SessionID != "a" || snap.Sessions[1].SessionID != "b" {
		t.Fatalf("unexpected order: %q, %q", snap.Sessions[0].SessionID, snap.Sessions[1].SessionID)
	}
}
