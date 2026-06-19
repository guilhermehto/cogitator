package harness

// ompHarness implements Harness for the Oh My Pi (omp) coding agent.
//
// # Resume mechanism (confirmed via `omp --help`, omp 16.1.1)
//
// omp takes no directory positional argument; it operates on the process CWD.
// The caller must set the process working directory to the worktree before
// exec-ing the returned argv.
//
//   - Fresh/continue (empty token): argv = ["omp", "--continue"]. omp's
//     per-directory session store resumes the most-recent session for the
//     launch CWD; when none exists it starts a fresh session. This mirrors
//     OpenCode's per-directory resume so jumping back into a worktree picks up
//     where the prior session left off.
//   - Resume a specific session (non-empty token = session id from roster):
//     argv = ["omp", "--resume", "<token>"]. omp matches the token by id
//     prefix, full filename, or path.
//
// # Live-status capability
//
// omp lifecycle events are bridged to cogitator by a small JS extension
// (installed via `cogitator omp-hook install`) that pipes each event to the
// `cogitator omp-hook` subcommand, which forwards it over the local Unix-domain
// socket consumed by internal/omp/provider.go. LiveStatus is true.
type ompHarness struct{}

// KindOMP is the stable Kind string for the Oh My Pi harness.
const KindOMP Kind = "omp"

func init() {
	DefaultRegistry.register(ompHarness{})
}

// Kind returns "omp".
func (ompHarness) Kind() Kind { return KindOMP }

// Capabilities reports LiveStatus = true because the omp hook bridge
// (internal/omp/provider.go) provides real-time attention signals via the
// cogitator omp-hook IPC bridge.
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
// --resume <token>, targeting that specific session. When token is empty,
// --continue resumes the most-recent session for the launch CWD (or starts a
// fresh one when none exists).
func (ompHarness) LaunchArgv(_ string, token ResumeToken) []string {
	if token != "" {
		return []string{"omp", "--resume", token}
	}
	return []string{"omp", "--continue"}
}
