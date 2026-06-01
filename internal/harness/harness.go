// Package harness defines the harness-agnostic seam through which cogitator
// launches and resumes coding agents. Every harness (OpenCode, Claude Code,
// Codex, …) implements the Harness interface and registers itself in the
// package-level Registry.
//
// # Live-status capability
//
// Harnesses that can report whether an agent is currently running set
// Capabilities.LiveStatus = true. For those harnesses cogitator can show
// "running" vs "stopped" rows. Harnesses with LiveStatus = false are
// launch+jump-only this pass: rows render as "unknown" rather than a false
// "stopped". Adding live-status support for a new harness requires only a new
// Harness implementation plus a discovery provider; no changes to the launcher,
// roster, merge, or jump/resume call sites are needed.
//
// # Resume tokens
//
// ResumeToken is a string that MAY carry a session id but is treated as
// INFORMATIONAL/optional. OpenCode's session store is per-directory: launching
// in the same working directory automatically resumes the most-recent session
// for that directory (confirmed empirically in step 1; the DB keys sessions by
// their launch CWD). When a real session id is available (e.g. from the roster)
// it is passed as the token and used with --session <id>; when absent, the
// per-directory store handles resume automatically. Step 6's roster schema
// should record the session id as an optional field — not a required key.
//
// No import of bubbletea or internal/ui is permitted in this package.
package harness

import "fmt"

// Kind is the stable string identifier for a harness (e.g. "opencode").
// It is stored in the roster and user config; changing a Kind is a breaking
// change to persisted data.
type Kind string

// Capabilities declares what a harness can report to cogitator at runtime.
type Capabilities struct {
	// LiveStatus is true when the harness advertises its running/stopped state
	// via a discovery mechanism (e.g. mDNS + SSE for OpenCode). When false,
	// cogitator cannot distinguish a running agent from a stopped one for this
	// harness; rows render as "unknown" until a discovery provider is added.
	LiveStatus bool
}

// ResumeToken is an optional session identifier passed to LaunchArgv when
// resuming a stopped session. It MAY be a real session id (e.g. the value
// stored in the roster from a prior OpenCode session) or it MAY be empty.
//
// Design note (step-1 finding): OpenCode stores sessions keyed by the literal
// launch CWD. Launching in the same directory automatically resumes the most-
// recent session for that directory — no explicit token is required. When a
// token IS provided, it is used with --session <id> to resume a specific
// session rather than the most-recent one. The roster (step 6) should record
// the session id as an optional field, not a required key.
type ResumeToken = string

// Harness is the interface every coding-agent harness must implement.
//
// Implementations must be safe for concurrent use (they are stateless value
// types in practice, but the interface does not enforce this).
type Harness interface {
	// Kind returns the stable string identifier for this harness.
	Kind() Kind

	// Capabilities returns the runtime capabilities this harness exposes.
	Capabilities() Capabilities

	// LaunchArgv returns the argv (program + arguments, no shell) to launch
	// or resume the harness in the given worktree directory.
	//
	// worktree is the canonical absolute path to the worktree directory; the
	// caller is responsible for setting the process working directory to
	// worktree before exec-ing the returned argv.
	//
	// token is the ResumeToken for the session to resume. When empty, the
	// harness should launch fresh (or rely on its own per-directory store to
	// resume automatically). When non-empty, the harness should attempt to
	// resume the identified session.
	LaunchArgv(worktree string, token ResumeToken) []string
}

// Registry maps Kind strings to registered Harness implementations.
// Use the package-level DefaultRegistry for normal operation.
type Registry struct {
	harnesses map[Kind]Harness
}

// newRegistry returns an empty Registry.
func newRegistry() *Registry {
	return &Registry{harnesses: make(map[Kind]Harness)}
}

// register adds h to r. It panics if a harness with the same Kind is already
// registered (registration happens at init time; a duplicate is a programmer
// error, not a runtime condition).
func (r *Registry) register(h Harness) {
	k := h.Kind()
	if _, exists := r.harnesses[k]; exists {
		panic(fmt.Sprintf("harness: duplicate registration for kind %q", k))
	}
	r.harnesses[k] = h
}

// Get returns the Harness registered under kind, or an error if no harness
// with that kind has been registered.
func (r *Registry) Get(kind Kind) (Harness, error) {
	h, ok := r.harnesses[kind]
	if !ok {
		return nil, fmt.Errorf("harness: unknown kind %q", kind)
	}
	return h, nil
}

// DefaultRegistry is the package-level registry populated by init functions
// in each harness implementation file (e.g. opencode.go).
var DefaultRegistry = newRegistry()
