package installkit

import (
	"fmt"
	"strconv"
)

// State is how a component's cogitator wiring compares with what its
// installer would write now.
type State int

const (
	// Missing: no cogitator wiring; install adds it.
	Missing State = iota
	// Current: cogitator's managed wiring matches what install would write.
	Current
	// Stale: cogitator's managed wiring differs (binary or plugin path, keys,
	// template version); install updates it in place.
	Stale
	// Foreign: the user wired cogitator outside cogitator's managed files;
	// install leaves it alone unless forced.
	Foreign
	// Unavailable: the tool the component wires is not on this machine.
	Unavailable
)

func (s State) String() string {
	switch s {
	case Missing:
		return "missing"
	case Current:
		return "current"
	case Stale:
		return "stale"
	case Foreign:
		return "foreign"
	case Unavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// Status is one component's State with the file that decided it. Line is the
// 1-based line of a Foreign finding, or 0.
type Status struct {
	State  State
	Path   string
	Line   int
	Detail string
}

// Location renders the file and, when known, the line as path:line.
func (s Status) Location() string {
	if s.Line <= 0 {
		return s.Path
	}

	return s.Path + ":" + strconv.Itoa(s.Line)
}

// ForeignError is the refusal an installer returns when the user already
// wired cogitator outside cogitator's managed files and --force is not set.
type ForeignError struct {
	Status Status
}

func (e *ForeignError) Error() string {
	return fmt.Sprintf("%s: cogitator is already wired here (%s); left alone, pass --force to install anyway", e.Status.Location(), e.Status.Detail)
}
