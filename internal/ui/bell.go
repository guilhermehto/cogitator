package ui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/state"
)

type rowKey struct {
	instanceID string
	sessionID  string
}

func needsAttention(a state.Attention) bool {
	return a == state.AttnPermissionPending || a == state.AttnQuestionPending || a == state.AttnErrored || a == state.AttnFinished
}

// processBellTransitions diffs the current snapshot against bellSent
// and returns the rowKeys that should ring the bell on this tick.
func processBellTransitions(rows []state.SessionView, bellSent map[rowKey]state.Attention) []rowKey {
	seen := map[rowKey]bool{}
	var fired []rowKey
	for _, sv := range rows {
		key := rowKey{instanceID: sv.InstanceID, sessionID: sv.SessionID}
		seen[key] = true
		if needsAttention(sv.Attention) {
			if bellSent[key] != sv.Attention {
				fired = append(fired, key)
				bellSent[key] = sv.Attention
			}
		} else {
			delete(bellSent, key)
		}
	}
	for k := range bellSent {
		if !seen[k] {
			delete(bellSent, k)
		}
	}
	return fired
}

// bubbletea owns stdout while in alt-screen mode; writing BEL to stdout
// avoids the scrollback artifacts caused by tea.Println.
func bellCmd(count int) tea.Cmd {
	if count <= 0 {
		return nil
	}
	return func() tea.Msg {
		for i := 0; i < count; i++ {
			_, _ = os.Stdout.Write([]byte{'\a'})
		}
		return nil
	}
}
