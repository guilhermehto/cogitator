package harness

// ompHarness implements Harness for the Oh My Pi (omp) coding agent.
//
// # Resume mechanism
//
// omp takes no directory positional argument; it operates on the process CWD
// (sessions are keyed by the launch directory under ~/.omp/agent/sessions).
// The caller must set the process working directory to the worktree before
// exec-ing the returned argv.
//
//   - Fresh launch (empty token): argv = ["omp"]
//   - Resume a specific session (non-empty token = session id from roster):
//     argv = ["omp", "--resume", "<token>"]
//
// `omp --resume <id>` matches the session id (case-insensitive prefix) within
// the current directory scope, falling back to a global search. An empty token
// always means fresh launch by convention; the roster supplies an explicit id
// when a prior session should be resumed.
//
// # Live-status capability
//
// LiveStatus is true: the omp hook listener (internal/omp/provider.go) provides
// real-time attention signals via the shipped omp extension and the
// cogitator omp-hook IPC bridge.
type ompHarness struct{}

// KindOmp is the stable Kind string for the omp harness.
const KindOmp Kind = "omp"

func init() {
	DefaultRegistry.register(ompHarness{})
}

// Kind returns "omp".
func (ompHarness) Kind() Kind { return KindOmp }

// Capabilities reports LiveStatus = true because the omp hook listener provides
// real-time attention signals via the cogitator omp-hook IPC bridge.
func (ompHarness) Capabilities() Capabilities {
	return Capabilities{LiveStatus: true}
}

// LaunchArgv returns the argv to launch or resume omp in worktree.
//
// The caller must set the process working directory to worktree before exec-ing
// the returned argv; omp uses the CWD as its project root and does not accept a
// directory positional argument.
//
// When token is non-empty it is treated as a session id and passed via
// --resume <token>. When token is empty, omp starts a fresh session.
func (ompHarness) LaunchArgv(_ string, token ResumeToken) []string {
	if token != "" {
		return []string{"omp", "--resume", token}
	}
	return []string{"omp"}
}
