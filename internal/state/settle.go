package state

import "time"

// attentionSettler debounces the attention a session shows. Providers relay
// status in bursts (a turn ending and the next starting, a poll racing a
// hook), so a session can briefly drop out of AttnActive and back; showing
// every hop flickers rows between groups and fires spurious bells. Becoming
// active shows at once; any other change shows only once it has held for the
// whole hold period.
type attentionSettler struct {
	hold  time.Duration
	shown map[providerSessionKey]shownAttention
}

type shownAttention struct {
	attention Attention
	// divergedAt is when the derived attention first differed from the shown
	// one; zero while they agree.
	divergedAt time.Time
}

func newAttentionSettler(hold time.Duration) attentionSettler {
	return attentionSettler{hold: hold, shown: map[providerSessionKey]shownAttention{}}
}

// settle returns the attention to show for key given its freshly derived
// attention, plus when a held label is due to change (zero when not held).
func (st *attentionSettler) settle(key providerSessionKey, derived Attention, now time.Time) (Attention, time.Time) {
	prev, seen := st.shown[key]
	if !seen || st.hold <= 0 || derived == AttnActive || derived == prev.attention {
		st.shown[key] = shownAttention{attention: derived}
		return derived, time.Time{}
	}

	if prev.divergedAt.IsZero() {
		prev.divergedAt = now
	}

	due := prev.divergedAt.Add(st.hold)
	if !now.Before(due) {
		st.shown[key] = shownAttention{attention: derived}
		return derived, time.Time{}
	}

	st.shown[key] = prev
	return prev.attention, due
}

// forgetSession drops what sessionID showed under any provider, so its next
// attention shows at once. Used when the user views a session and expects its
// badge to change now.
func (st *attentionSettler) forgetSession(sessionID string) {
	for key := range st.shown {
		if key.sessionID == sessionID {
			delete(st.shown, key)
		}
	}
}

// retain drops sessions that left the snapshot.
func (st *attentionSettler) retain(present map[providerSessionKey]bool) {
	for key := range st.shown {
		if !present[key] {
			delete(st.shown, key)
		}
	}
}
