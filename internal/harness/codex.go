package harness

// codexHarness implements Harness for the Codex CLI coding agent.
//
// # Resume mechanism (confirmed empirically, codex-cli 0.136.0, step 1)
//
// Codex takes no directory positional argument; it operates on the process
// CWD. The caller must set the process working directory to the worktree
// before exec-ing the returned argv.
//
//   - Fresh launch (empty token): argv = ["codex"]
//   - Resume a specific session (non-empty token = session UUID from roster):
//     argv = ["codex", "resume", "<token>"]
//
// Note: `codex resume --last` resumes the most-recent session in the CWD, but
// cogitator uses empty-token = fresh launch by convention; the roster supplies
// an explicit UUID when a prior session should be resumed.
//
// # Live-status capability
//
// Codex does not currently advertise its running state via mDNS or SSE.
// LiveStatus is false for this pass; a later step will flip it true once a
// discovery provider is added.
type codexHarness struct{}

// KindCodex is the stable Kind string for the Codex harness.
const KindCodex Kind = "codex"

func init() {
	DefaultRegistry.register(codexHarness{})
}

// Kind returns "codex".
func (codexHarness) Kind() Kind { return KindCodex }

// Capabilities reports LiveStatus = false because Codex does not yet expose a
// discovery mechanism that cogitator can consume. Rows render as "unknown"
// until a discovery provider is added in a later step.
func (codexHarness) Capabilities() Capabilities {
	return Capabilities{LiveStatus: false}
}

// LaunchArgv returns the argv to launch or resume Codex in worktree.
//
// The caller must set the process working directory to worktree before exec-ing
// the returned argv; Codex uses the CWD as its project root and does not
// accept a directory positional argument.
//
// When token is non-empty it is treated as a session UUID and passed via the
// "resume" subcommand. When token is empty, Codex starts a fresh session.
func (codexHarness) LaunchArgv(_ string, token ResumeToken) []string {
	if token != "" {
		return []string{"codex", "resume", token}
	}
	return []string{"codex"}
}
