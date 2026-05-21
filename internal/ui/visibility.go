package ui

import (
	"time"

	"github.com/guilhermehto/cogitator/internal/state"
)

func shouldHideSubagent(sv state.SessionView) bool {
	if sv.ParentID == "" {
		return false
	}
	if needsAttention(sv.Attention) {
		return false
	}
	return sv.StatusType == "idle" || sv.StatusType == ""
}

// shouldHideInactive returns true when an idle session has gone quiet for
// longer than inactiveAfter. Sessions needing attention are always kept
// visible. A zero LastActivity is treated as "just appeared" — we don't
// have enough information to age it out yet.
func shouldHideInactive(sv state.SessionView, now time.Time, inactiveAfter time.Duration) bool {
	if inactiveAfter <= 0 {
		return false
	}
	if sv.Attention != state.AttnInactive {
		return false
	}
	if sv.LastActivity.IsZero() {
		return false
	}
	return now.Sub(sv.LastActivity) > inactiveAfter
}

// visibleSessions filters snapshot rows for the sessions pane. `now` is the
// reference time used by the inactivity filter; if zero, time.Now() is used.
func visibleSessions(all []state.SessionView, collapseRecent bool, now time.Time, inactiveAfter time.Duration) ([]state.SessionView, map[string]int) {
	if now.IsZero() {
		now = time.Now()
	}
	byKey := make(map[rowKey]state.SessionView, len(all))
	hidden := make(map[rowKey]bool, len(all))
	for _, sv := range all {
		k := rowKey{instanceID: sv.InstanceID, sessionID: sv.SessionID}
		byKey[k] = sv
		if shouldHideSubagent(sv) {
			hidden[k] = true
		}
		if shouldHideInactive(sv, now, inactiveAfter) {
			hidden[k] = true
		}
		if collapseRecent && sv.Source == state.SourceRecent {
			hidden[k] = true
		}
	}

	recentByInstance := make(map[string]int)
	out := make([]state.SessionView, 0, len(all))
	for _, sv := range all {
		if shouldHideSubagent(sv) {
			continue
		}
		if shouldHideInactive(sv, now, inactiveAfter) {
			continue
		}
		if sv.Source == state.SourceRecent {
			recentByInstance[sv.InstanceName]++
			if collapseRecent {
				continue
			}
		}
		sv.ParentID = nearestVisibleParentID(sv, byKey, hidden)
		out = append(out, sv)
	}
	return out, recentByInstance
}

func nearestVisibleParentID(sv state.SessionView, byKey map[rowKey]state.SessionView, hidden map[rowKey]bool) string {
	parentID := sv.ParentID
	for hops := 0; parentID != "" && hops < 32; hops++ {
		k := rowKey{instanceID: sv.InstanceID, sessionID: parentID}
		parent, ok := byKey[k]
		if !ok {
			return ""
		}
		if !hidden[k] {
			return parentID
		}
		parentID = parent.ParentID
	}
	return ""
}
