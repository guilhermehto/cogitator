package omp_test

import (
	"sync"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/omp"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// recordSink records ApplyUpdate and ReplaceProviderInstance calls.
type recordSink struct {
	mu       sync.Mutex
	updates  []provider.SessionUpdate
	replaces [][]provider.SessionUpdate
}

func (s *recordSink) ApplyUpdate(u provider.SessionUpdate) {
	s.mu.Lock()
	s.updates = append(s.updates, u)
	s.mu.Unlock()
}

func (s *recordSink) RemoveProviderSession(_ harness.Kind, _, _ string) {}

func (s *recordSink) ClearProviderInstance(_ harness.Kind, _ string) {}

func (s *recordSink) ReplaceProviderInstance(_ harness.Kind, _ string, us []provider.SessionUpdate) {
	cp := make([]provider.SessionUpdate, len(us))
	copy(cp, us)
	s.mu.Lock()
	s.replaces = append(s.replaces, cp)
	s.mu.Unlock()
}

func (s *recordSink) lastUpdate() (provider.SessionUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.updates) == 0 {
		return provider.SessionUpdate{}, false
	}
	return s.updates[len(s.updates)-1], true
}

func (s *recordSink) lastReplace() ([]provider.SessionUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.replaces) == 0 {
		return nil, false
	}
	return s.replaces[len(s.replaces)-1], true
}

func (s *recordSink) latestForSession(id string) (provider.SessionUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found provider.SessionUpdate
	var ok bool
	for _, u := range s.updates {
		if u.SessionID == id {
			found = u
			ok = true
		}
	}
	return found, ok
}

func TestProvider_Kind(t *testing.T) {
	p := omp.NewProvider("", 5*time.Second, 30*time.Minute, nil)
	if p.Kind() != harness.KindOMP {
		t.Errorf("Kind() = %q, want %q", p.Kind(), harness.KindOMP)
	}
}

func TestPollOnce_RecencyMapping(t *testing.T) {
	tests := []struct {
		name       string
		age        time.Duration
		wantSource string
		wantStatus string
	}{
		{"within window → live/busy", 5 * time.Minute, "live", "busy"},
		{"outside window → recent/empty", 31 * time.Minute, "recent", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := omp.NewProvider("", 10*time.Second, 30*time.Minute, nil)
			sink := &recordSink{}
			p.PollOnceForTest(sink, []omp.Session{
				{ID: "s1", Dir: "/tmp/x", Title: "t", LastActivity: time.Now().Add(-tc.age)},
			})
			batch, ok := sink.lastReplace()
			if !ok || len(batch) != 1 {
				t.Fatalf("expected one replace with one update, got %v", batch)
			}
			if batch[0].Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", batch[0].Source, tc.wantSource)
			}
			if batch[0].StatusType != tc.wantStatus {
				t.Errorf("StatusType = %q, want %q", batch[0].StatusType, tc.wantStatus)
			}
		})
	}
}

func TestHook_TurnLifecycle_BusyThenIdle(t *testing.T) {
	p := omp.NewProvider("", 10*time.Second, 30*time.Minute, nil)
	sink := &recordSink{}
	const sf = "/x/sessions/-tmp-x/2026-06-19T10-00-00-000Z_s1.jsonl"

	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"turn_start","cwd":"/tmp/x","session_file":"`+sf+`"}`), sink)
	u, ok := sink.lastUpdate()
	if !ok {
		t.Fatal("no update after turn_start")
	}
	if u.SessionID != "s1" {
		t.Errorf("SessionID = %q, want s1 (from session_file basename)", u.SessionID)
	}
	if u.StatusType != "busy" {
		t.Errorf("turn_start StatusType = %q, want busy", u.StatusType)
	}

	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"turn_end","cwd":"/tmp/x","session_file":"`+sf+`"}`), sink)
	u, _ = sink.lastUpdate()
	if u.StatusType != "idle" {
		t.Errorf("turn_end StatusType = %q, want idle", u.StatusType)
	}
}

func TestHook_AskTool_SetsAndClearsQuestion(t *testing.T) {
	p := omp.NewProvider("", 10*time.Second, 30*time.Minute, nil)
	sink := &recordSink{}
	const sf = "/x/2026-06-19T10-00-00-000Z_s1.jsonl"

	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"tool_call","tool_name":"ask","cwd":"/tmp/x","session_file":"`+sf+`"}`), sink)
	u, _ := sink.lastUpdate()
	if !u.HasQuestion {
		t.Error("ask tool_call did not set HasQuestion")
	}

	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"tool_result","tool_name":"ask","cwd":"/tmp/x","session_file":"`+sf+`"}`), sink)
	u, _ = sink.lastUpdate()
	if u.HasQuestion {
		t.Error("ask tool_result did not clear HasQuestion")
	}
}

func TestHook_NonAskToolCall_DoesNotSetQuestion(t *testing.T) {
	p := omp.NewProvider("", 10*time.Second, 30*time.Minute, nil)
	sink := &recordSink{}
	const sf = "/x/2026-06-19T10-00-00-000Z_s1.jsonl"

	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"tool_call","tool_name":"bash","cwd":"/tmp/x","session_file":"`+sf+`"}`), sink)
	u, _ := sink.lastUpdate()
	if u.HasQuestion {
		t.Error("non-ask tool_call must not set HasQuestion")
	}
	if u.StatusType != "busy" {
		t.Errorf("non-ask tool_call StatusType = %q, want busy", u.StatusType)
	}
}

// TestHookSeedSurvivesPoll verifies that a session seeded by a hook (before its
// file exists on disk) is not pruned by a poll that does not see it, but IS
// pruned once its overlay is cleared and it remains absent.
func TestHookSeedSurvivesPoll(t *testing.T) {
	p := omp.NewProvider("", 10*time.Second, 30*time.Minute, nil)
	sink := &recordSink{}
	const sf = "/x/2026-06-19T10-00-00-000Z_seed.jsonl"

	// Hook arrives before any disk flush — seeds an entry with a live overlay.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"turn_start","cwd":"/tmp/seed","session_file":"`+sf+`"}`), sink)

	// Poll with NO sessions on disk — the seeded entry must survive.
	p.PollOnceForTest(sink, nil)
	batch, ok := sink.lastReplace()
	if !ok || len(batch) != 1 {
		t.Fatalf("seeded session was dropped by poll; batch=%v", batch)
	}
	if batch[0].SessionID != "seed" {
		t.Errorf("survived SessionID = %q, want seed", batch[0].SessionID)
	}
}

func TestHook_CWDFallbackResolution(t *testing.T) {
	p := omp.NewProvider("", 10*time.Second, 30*time.Minute, nil)
	sink := &recordSink{}

	// Seed a known session via poll (Dir is the lookup key).
	p.PollOnceForTest(sink, []omp.Session{
		{ID: "known", Dir: "/tmp/proj", Title: "t", LastActivity: time.Now()},
	})

	// Hook with no session_file — must resolve via cwd match.
	p.HandleHookFrameForTest([]byte(`{"hook_event_name":"turn_end","cwd":"/tmp/proj"}`), sink)
	u, ok := sink.lastUpdate()
	if !ok {
		t.Fatal("no update emitted")
	}
	if u.SessionID != "known" {
		t.Errorf("SessionID = %q, want known (cwd fallback)", u.SessionID)
	}
	if u.StatusType != "idle" {
		t.Errorf("StatusType = %q, want idle", u.StatusType)
	}
}
