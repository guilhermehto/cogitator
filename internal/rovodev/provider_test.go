package rovodev_test

import (
	"sync"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/provider"
	"github.com/guilhermehto/cogitator/internal/rovodev"
)

// fakeSink records ReplaceProviderInstance batches so tests can assert on the
// emitted snapshot.
type fakeSink struct {
	mu       sync.Mutex
	replaces [][]provider.SessionUpdate
}

func (f *fakeSink) ApplyUpdate(provider.SessionUpdate)                 {}
func (f *fakeSink) RemoveProviderSession(harness.Kind, string, string) {}
func (f *fakeSink) ClearProviderInstance(harness.Kind, string)         {}
func (f *fakeSink) ReplaceProviderInstance(_ harness.Kind, _ string, us []provider.SessionUpdate) {
	cp := make([]provider.SessionUpdate, len(us))
	copy(cp, us)
	f.mu.Lock()
	f.replaces = append(f.replaces, cp)
	f.mu.Unlock()
}

func (f *fakeSink) lastBatch() []provider.SessionUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replaces) == 0 {
		return nil
	}
	return f.replaces[len(f.replaces)-1]
}

func newProvider() *rovodev.Provider {
	return rovodev.NewProvider("", 5*time.Second, 30*time.Minute, nil)
}

// TestProvider_RecencyMapping verifies that a recently-written session is
// reported live/busy while a stale one is reported recent/inactive.
func TestProvider_RecencyMapping(t *testing.T) {
	sink := &fakeSink{}
	p := newProvider()

	now := time.Now()
	sessions := []rovodev.Session{
		{ID: "live", Dir: "/tmp/a", Title: "fresh", LastActivity: now.Add(-1 * time.Minute)},
		{ID: "stale", Dir: "/tmp/b", Title: "old", LastActivity: now.Add(-2 * time.Hour)},
	}
	p.PollOnceForTest(sink, sessions)

	batch := sink.lastBatch()
	if len(batch) != 2 {
		t.Fatalf("got %d updates, want 2", len(batch))
	}

	got := map[string]provider.SessionUpdate{}
	for _, u := range batch {
		got[u.SessionID] = u
		if u.Provider != harness.KindRovodev {
			t.Errorf("%s: Provider = %q, want %q", u.SessionID, u.Provider, harness.KindRovodev)
		}
		if u.InstanceID != rovodev.InstanceID {
			t.Errorf("%s: InstanceID = %q, want %q", u.SessionID, u.InstanceID, rovodev.InstanceID)
		}
	}

	if got["live"].Source != "live" || got["live"].StatusType != "busy" {
		t.Errorf("live session = {Source:%q StatusType:%q}, want {live busy}",
			got["live"].Source, got["live"].StatusType)
	}
	if got["stale"].Source != "recent" || got["stale"].StatusType != "" {
		t.Errorf("stale session = {Source:%q StatusType:%q}, want {recent \"\"}",
			got["stale"].Source, got["stale"].StatusType)
	}
}

// TestProvider_EmptyClearsInstance verifies that polling with no sessions still
// emits one (empty) snapshot so the instance is cleared without a flash.
func TestProvider_EmptyClearsInstance(t *testing.T) {
	sink := &fakeSink{}
	p := newProvider()

	p.PollOnceForTest(sink, nil)

	if len(sink.replaces) != 1 {
		t.Fatalf("got %d ReplaceProviderInstance calls, want 1", len(sink.replaces))
	}
	if len(sink.lastBatch()) != 0 {
		t.Fatalf("got %d updates, want 0", len(sink.lastBatch()))
	}
}
