package state

import "time"

// Attention is a coarse triage label derived from session status, pending
// permissions, and event history.
type Attention string

const (
	AttnActive            Attention = "active"
	AttnInactive          Attention = "inactive"
	AttnPermissionPending Attention = "permission"
	AttnErrored           Attention = "errored"
)

// Rank is used to sort the attention pane: lower = more urgent.
func (a Attention) Rank() int {
	switch a {
	case AttnPermissionPending:
		return 0
	case AttnErrored:
		return 1
	default:
		return 2
	}
}

// Classify computes the attention label for one session.
//
// statusType is the value of SessionStatus.type ("idle", "generating",
// "retry", ...). hasPermission means a pending permission request exists for
// this session. lastError is the time of the most recent session.error event
// (zero if none). lastActivity is the time of the most recent
// message/session update.
func Classify(statusType string, hasPermission bool, lastError, lastActivity time.Time) Attention {
	if hasPermission {
		return AttnPermissionPending
	}
	// An error counts as needing attention until something newer happens.
	if !lastError.IsZero() && !lastError.Before(lastActivity) {
		return AttnErrored
	}
	switch statusType {
	case "busy", "generating":
		return AttnActive
	default:
		return AttnInactive
	}
}
