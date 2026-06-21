package omp_test

import (
	"sync"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/omp"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// fakeSink records ApplyUpdate and ReplaceProviderInstance calls so tests can
// assert on emitted updates.
type fakeSink struct {
	mu       sync.Mutex
	updates  []provider.SessionUpdate
	replaces [][]provider.SessionUpdate
}

func (f *fakeSink) ApplyUpdate(u provider.SessionUpdate) {
	f.mu.Lock()
	f.updates = append(f.updates, u)
	f.mu.Unlock()
}
func (f *fakeSink) RemoveProviderSession(_ harness.Kind, _, _ string) {}
func (f *fakeSink) ClearProviderInstance(_ harness.Kind, _ string)    {}
func (f *fakeSink) ReplaceProviderInstance(_ harness.Kind, _ string, us []provider.SessionUpdate) {
	cp := make([]provider.SessionUpdate, len(us))
	copy(cp, us)
	f.mu.Lock()
	f.replaces = append(f.replaces, cp)
	f.mu.Unlock()
}

func (f *fakeSink) lastReplace() []provider.SessionUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replaces) == 0 {
		return nil
	}
	return f.replaces[len(f.replaces)-1]
}

func (f *fakeSink) lastUpdate() (provider.SessionUpdate, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) == 0 {
		return provider.SessionUpdate{}, false
	}
	return f.updates[len(f.updates)-1], true
}

func TestPollOnce_MapsFieldsAndRecency(t *testing.T) {
	p := omp.NewProvider("", 5*time.Second, 30*time.Minute, nil)
	sink := &fakeSink{}
	now := time.Now()

	sessions := []omp.Session{
		{ID: "live1", Dir: "/tmp/live", Title: "Live one", Created: now.Add(-time.Hour), LastActivity: now.Add(-time.Minute)},
		{ID: "old1", Dir: "/tmp/old", Title: "Old one", Created: now.Add(-2 * time.Hour), LastActivity: now.Add(-2 * time.Hour)},
	}
	p.PollOnceForTest(sink, sessions)

	batch := sink.lastReplace()
	if len(batch) != 2 {
		t.Fatalf("replace batch len = %d, want 2", len(batch))
	}
	by := map[string]provider.SessionUpdate{}
	for _, u := range batch {
		if u.Provider != harness.KindOmp {
			t.Errorf("Provider = %q, want %q", u.Provider, harness.KindOmp)
		}
		if u.InstanceID != omp.InstanceID {
			t.Errorf("InstanceID = %q, want %q", u.InstanceID, omp.InstanceID)
		}
		by[u.SessionID] = u
	}
	if by["live1"].Source != "live" {
		t.Errorf("live1 Source = %q, want live", by["live1"].Source)
	}
	if by["live1"].StatusType != "busy" {
		t.Errorf("live1 StatusType = %q, want busy (recent → busy)", by["live1"].StatusType)
	}
	if by["live1"].Title != "Live one" {
		t.Errorf("live1 Title = %q, want Live one", by["live1"].Title)
	}
	if by["old1"].Source != "recent" {
		t.Errorf("old1 Source = %q, want recent", by["old1"].Source)
	}
	if by["old1"].StatusType != "" {
		t.Errorf("old1 StatusType = %q, want empty (stale → not busy)", by["old1"].StatusType)
	}
}

func TestHandleHookFrame_StatusTransitions(t *testing.T) {
	p := omp.NewProvider("", 5*time.Second, 30*time.Minute, nil)
	sink := &fakeSink{}

	// turn_start → busy, seeds the (unknown) session by id.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"turn_start","session_id":"s1","cwd":"/tmp/wt"}`), sink)
	u, ok := sink.lastUpdate()
	if !ok || u.SessionID != "s1" {
		t.Fatalf("turn_start did not emit an update for s1: %+v", u)
	}
	if u.StatusType != "busy" {
		t.Errorf("after turn_start StatusType = %q, want busy", u.StatusType)
	}

	// tool_call(ask) → question pending.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"tool_call","session_id":"s1","tool_name":"ask"}`), sink)
	u, _ = sink.lastUpdate()
	if !u.HasQuestion {
		t.Error("after tool_call(ask) HasQuestion = false, want true")
	}

	// tool_result(ask) → question cleared.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"tool_result","session_id":"s1","tool_name":"ask"}`), sink)
	u, _ = sink.lastUpdate()
	if u.HasQuestion {
		t.Error("after tool_result(ask) HasQuestion = true, want false")
	}

	// tool_result(error) → LastError set.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"tool_result","session_id":"s1","tool_name":"bash","is_error":true}`), sink)
	u, _ = sink.lastUpdate()
	if u.LastError.IsZero() {
		t.Error("after tool_result error LastError is zero, want set")
	}

	// turn_end → idle, question cleared.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"turn_end","session_id":"s1"}`), sink)
	u, _ = sink.lastUpdate()
	if u.StatusType != "idle" {
		t.Errorf("after turn_end StatusType = %q, want idle", u.StatusType)
	}
	if u.HasQuestion {
		t.Error("after turn_end HasQuestion = true, want false")
	}
}

func TestHandleHookFrame_CWDFallbackResolution(t *testing.T) {
	p := omp.NewProvider("", 5*time.Second, 30*time.Minute, nil)
	sink := &fakeSink{}

	// Seed a known on-disk session via a poll so cwd→id resolution works.
	p.PollOnceForTest(sink, []omp.Session{{ID: "disk1", Dir: "/tmp/wt", Title: "Disk", LastActivity: time.Now()}})

	// Hook with no session_id but matching cwd must resolve to disk1, not seed
	// a second row.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"turn_start","cwd":"/tmp/wt"}`), sink)
	u, ok := sink.lastUpdate()
	if !ok {
		t.Fatal("no update emitted")
	}
	if u.SessionID != "disk1" {
		t.Errorf("resolved SessionID = %q, want disk1 (cwd fallback)", u.SessionID)
	}
	if u.StatusType != "busy" {
		t.Errorf("StatusType = %q, want busy", u.StatusType)
	}
}

func TestPollOnce_PreservesHookSeededSession(t *testing.T) {
	p := omp.NewProvider("", 5*time.Second, 30*time.Minute, nil)
	sink := &fakeSink{}

	// A hook arrives before the session file is flushed to disk.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"turn_start","session_id":"early1","cwd":"/tmp/early"}`), sink)

	// A poll that does NOT see early1 on disk must not drop the live overlay.
	p.PollOnceForTest(sink, nil)
	batch := sink.lastReplace()
	found := false
	for _, u := range batch {
		if u.SessionID == "early1" {
			found = true
		}
	}
	if !found {
		t.Errorf("hook-seeded session early1 was pruned by a poll that missed it: %+v", batch)
	}
}
