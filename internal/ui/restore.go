package ui

import (
	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// rosterToRestored converts a loaded roster map into the slice accepted by
// store.RestoreSessions. It drops entries that cannot contribute a useful seed:
//   - empty SessionID (no session to key on)
//   - attention outside the sticky set (active/inactive are transient)
//
// The Provider field maps to the provider kind; legacy entries fall back to
// Harness, and an empty value defaults to "opencode", matching the defaulting
// in workspace/applySnapshot so the seed key aligns with snapshot Provider.
func rosterToRestored(roster map[string]workspace.RosterEntry) []state.RestoredSession {
	if len(roster) == 0 {
		return nil
	}
	out := make([]state.RestoredSession, 0, len(roster))
	for _, e := range roster {
		if e.SessionID == "" {
			continue
		}
		attn := state.Attention(e.Attention)
		if !attn.IsSticky() {
			continue
		}
		kind := harness.Kind(e.Provider)
		if kind == "" {
			kind = harness.Kind(e.Harness)
		}
		if kind == "" {
			kind = harness.Kind("opencode")
		}
		out = append(out, state.RestoredSession{
			Provider:  kind,
			SessionID: e.SessionID,
			Attention: attn,
		})
	}
	return out
}
