package state

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/provider"
)

const settleHold = 5 * time.Second

type settleFixture struct {
	t     *testing.T
	store *Store
	now   time.Time
}

func newSettleFixture(t *testing.T, hold time.Duration) *settleFixture {
	cfg := config.Default()
	cfg.AttentionSettle = hold
	f := &settleFixture{t: t, store: New(context.Background(), cfg, slog.Default()), now: time.Unix(1_700_000, 0)}
	f.store.now = func() time.Time { return f.now }
	return f
}

func (f *settleFixture) status(statusType string) {
	f.store.ApplyUpdate(provider.SessionUpdate{
		Provider:   harness.KindCodex,
		InstanceID: "codex",
		SessionID:  "C1",
		StatusType: statusType,
		Source:     string(SourceLive),
	})
}

func (f *settleFixture) after(d time.Duration) *settleFixture {
	f.now = f.now.Add(d)
	return f
}

func (f *settleFixture) expect(step string, want Attention) {
	f.t.Helper()
	if got := attentionOf(f.t, f.store, "C1"); got != want {
		f.t.Fatalf("%s: attention = %q, want %q", step, got, want)
	}
}

func TestSettle_FinishShowsOnlyOnceItHolds(t *testing.T) {
	f := newSettleFixture(t, settleHold)
	f.status("busy")
	f.status("idle")

	f.expect("just finished", AttnActive)
	f.after(settleHold-time.Millisecond).expect("just before the hold ends", AttnActive)
	f.after(time.Millisecond).expect("hold ended", AttnFinished)
}

func TestSettle_ResumingWithinHoldNeverShowsFinished(t *testing.T) {
	f := newSettleFixture(t, settleHold)
	f.status("busy")
	f.status("idle")
	f.after(2*time.Second).expect("finished blip", AttnActive)

	f.status("busy")
	f.after(settleHold).expect("resumed", AttnActive)

	f.status("idle")
	f.after(settleHold-time.Millisecond).expect("second finish restarts the hold", AttnActive)
	f.after(time.Millisecond).expect("second finish held", AttnFinished)
}

func TestSettle_BecomingActiveShowsAtOnce(t *testing.T) {
	f := newSettleFixture(t, settleHold)
	f.status("idle")
	f.expect("first sighting", AttnInactive)

	f.status("busy")
	f.expect("started working", AttnActive)
}

func TestSettle_MarkViewedClearsFinishedAtOnce(t *testing.T) {
	f := newSettleFixture(t, settleHold)
	f.status("busy")
	f.status("idle")
	f.after(settleHold).expect("finished", AttnFinished)

	f.store.MarkViewed(harness.KindCodex, "codex", "C1")
	f.expect("viewed", AttnInactive)
}

func TestSettle_RepublishesWhenHoldEndsWithoutNewEvents(t *testing.T) {
	cfg := config.Default()
	cfg.AttentionSettle = 20 * time.Millisecond
	s := New(context.Background(), cfg, slog.Default())
	snaps := s.Subscribe()
	update := provider.SessionUpdate{Provider: harness.KindCodex, InstanceID: "codex", SessionID: "C1", Source: string(SourceLive)}

	update.StatusType = "busy"
	s.ApplyUpdate(update)
	update.StatusType = "idle"
	s.ApplyUpdate(update)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case snap := <-snaps:
			if len(snap.Sessions) == 1 && snap.Sessions[0].Attention == AttnFinished {
				return
			}
		case <-deadline:
			t.Fatal("no snapshot showed the settled finished attention")
		}
	}
}
