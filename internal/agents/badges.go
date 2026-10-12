package agents

import (
	"sort"
	"strconv"
	"time"

	"github.com/guilhermehto/cogitator/internal/state"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
)

const (
	OptStatus = "@cog_status"
	OptReason = "@cog_reason"
	OptSince  = "@cog_since"

	OptNeeds   = "@cog_needs"
	OptDone    = "@cog_done"
	OptWorking = "@cog_working"
)

var (
	windowOptions = []string{OptStatus, OptReason, OptSince}
	globalOptions = []string{OptNeeds, OptDone, OptWorking}
)

// windowBadge is the desired @cog_* state of one tmux window.
type windowBadge struct {
	status string
	reason string
	since  string
}

func (b windowBadge) value(option string) string {
	switch option {
	case OptStatus:
		return b.status
	case OptReason:
		return b.reason
	default:
		return b.since
	}
}

func paneValue(p Pane, option string) string {
	return windowBadge{status: p.CogStatus, reason: p.CogReason, since: p.CogSince}.value(option)
}

// DiffOptions returns the writes that bring tmux's @cog_* options in line with
// the agent list, given what tmux currently holds (panes and globals).
func DiffOptions(list []Agent, panes []Pane, globals map[string]string) []tmuxctl.OptionWrite {
	current := currentWindowOptions(panes)
	desired := desiredBadges(list, current)

	var writes []tmuxctl.OptionWrite
	for _, windowID := range sortedKeys(current) {
		for _, option := range windowOptions {
			want := desired[windowID].value(option)
			if write, ok := diffOption(windowID, option, current[windowID].value(option), want); ok {
				writes = append(writes, write)
			}
		}
	}

	return append(writes, diffGlobals(desired, globals)...)
}

// ClearAll returns the writes that remove every @cog_* option cogitator owns.
func ClearAll(panes []Pane, globals map[string]string) []tmuxctl.OptionWrite {
	var writes []tmuxctl.OptionWrite

	current := currentWindowOptions(panes)
	for _, windowID := range sortedKeys(current) {
		for _, option := range windowOptions {
			if current[windowID].value(option) != "" {
				writes = append(writes, tmuxctl.OptionWrite{Target: windowID, Name: option, Unset: true})
			}
		}
	}

	for _, option := range globalOptions {
		writes = append(writes, tmuxctl.OptionWrite{Global: true, Name: option, Unset: true})
	}
	return writes
}

// SeenOnFocus returns Done agents a client is looking at and has touched since
// the agent last moved, so an idle parked client never clears fresh badges.
func SeenOnFocus(list []Agent, clients []tmuxctl.ClientFocus) []Agent {
	seen := map[string]bool{}
	for _, client := range clients {
		id, ok := AgentAt(list, client.WindowID, client.SessionID)
		if !ok {
			continue
		}

		active := time.Unix(client.ActivityUnixSecs, 0)
		for _, a := range list {
			if a.ID == id && a.Group == GroupDone && active.After(a.LastActivity) {
				seen[id] = true
			}
		}
	}

	var out []Agent
	for _, a := range list {
		if seen[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

func currentWindowOptions(panes []Pane) map[string]windowBadge {
	current := map[string]windowBadge{}
	for _, p := range panes {
		if _, ok := current[p.WindowID]; !ok {
			current[p.WindowID] = windowBadge{status: p.CogStatus, reason: p.CogReason, since: p.CogSince}
		}
	}
	return current
}

// desiredBadges reduces agents to one badge per listed window; the most urgent
// agent wins, ties broken by agent ID. @cog_since is kept while the status is
// unchanged so a busy Working agent does not cause a write every tick.
func desiredBadges(list []Agent, current map[string]windowBadge) map[string]windowBadge {
	winners := map[string]Agent{}
	for _, a := range list {
		if _, listed := current[a.WindowID]; !listed || a.Group == GroupStopped {
			continue
		}
		if best, ok := winners[a.WindowID]; !ok || moreUrgent(a, best) {
			winners[a.WindowID] = a
		}
	}

	desired := map[string]windowBadge{}
	for windowID, a := range winners {
		badge := windowBadge{status: statusOf(a), reason: reasonOf(a)}
		switch {
		case current[windowID].status == badge.status && current[windowID].since != "":
			badge.since = current[windowID].since
		case !a.LastActivity.IsZero():
			badge.since = strconv.FormatInt(a.LastActivity.Unix(), 10)
		}
		desired[windowID] = badge
	}
	return desired
}

func moreUrgent(a, b Agent) bool {
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	return a.ID < b.ID
}

func statusOf(a Agent) string {
	switch a.Group {
	case GroupNeedsYou:
		return "needs-you"
	case GroupDone:
		return "done"
	case GroupWorking:
		return "working"
	default:
		return "idle"
	}
}

func reasonOf(a Agent) string {
	switch a.Group {
	case GroupNeedsYou:
		return needsYouReason(a.Attention)
	case GroupDone:
		return "finished"
	default:
		return ""
	}
}

func needsYouReason(attention state.Attention) string {
	switch attention {
	case state.AttnPermissionPending:
		return "permission"
	case state.AttnQuestionPending:
		return "question"
	default:
		return "errored"
	}
}

func diffOption(target, option, have, want string) (tmuxctl.OptionWrite, bool) {
	switch {
	case want == have:
		return tmuxctl.OptionWrite{}, false
	case want == "":
		return tmuxctl.OptionWrite{Target: target, Name: option, Unset: true}, true
	default:
		return tmuxctl.OptionWrite{Target: target, Name: option, Value: want}, true
	}
}

func diffGlobals(desired map[string]windowBadge, globals map[string]string) []tmuxctl.OptionWrite {
	counts := map[string]int{}
	for _, badge := range desired {
		switch badge.status {
		case "needs-you":
			counts[OptNeeds]++
		case "done":
			counts[OptDone]++
		case "working":
			counts[OptWorking]++
		}
	}

	var writes []tmuxctl.OptionWrite
	for _, option := range globalOptions {
		want := ""
		if counts[option] > 0 {
			want = strconv.Itoa(counts[option])
		}

		write, ok := diffOption("", option, globals[option], want)
		if ok {
			write.Global = true
			writes = append(writes, write)
		}
	}
	return writes
}

func sortedKeys(m map[string]windowBadge) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
