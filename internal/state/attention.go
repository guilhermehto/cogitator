package state

import "time"

// Attention is a coarse triage label derived from session status, pending
// permissions/questions, and event history.
type Attention string

const (
	AttnActive            Attention = "active"
	AttnInactive          Attention = "inactive"
	AttnPermissionPending Attention = "permission"
	AttnQuestionPending   Attention = "question"
	AttnErrored           Attention = "errored"
	// AttnFinished marks a session that was working on a user request and has
	// since gone idle, but the user has not yet returned to it. Unlike the
	// other labels it is NOT computed by Classify (which is stateless): it
	// requires per-session transition memory and the user-viewed signal, so
	// the store sets it directly when assembling a snapshot.
	AttnFinished Attention = "finished"
)

// Rank is used to sort the attention pane: lower = more urgent.
func (a Attention) Rank() int {
	switch a {
	case AttnPermissionPending:
		return 0
	case AttnQuestionPending:
		return 0
	case AttnErrored:
		return 1
	case AttnFinished:
		return 1
	default:
		return 2
	}
}

// Classify computes the attention label for one session.
//
// statusType is the value of SessionStatus.type ("idle", "generating",
// "retry", ...). hasPermission means a pending permission request exists for
// this session. hasQuestion means a pending question tool request exists.
// lastError is the time of the most recent session.error event (zero if none).
// lastActivity is the time of the most recent message/session update.
func Classify(statusType string, hasPermission, hasQuestion bool, lastError, lastActivity time.Time) Attention {
	if hasPermission {
		return AttnPermissionPending
	}
	if hasQuestion {
		return AttnQuestionPending
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
