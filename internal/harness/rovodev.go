package harness

// rovodevHarness implements Harness for the Atlassian Rovo Dev CLI coding agent.
//
// # Launch / resume mechanism (confirmed via Atlassian docs, support.atlassian.com
// /rovo/docs/rovo-dev-cli-commands and /manage-sessions-in-rovo-dev-cli)
//
// Rovo Dev is invoked through the Atlassian CLI: `acli rovodev run`. It takes no
// directory positional argument; it operates on the process CWD (sessions are
// keyed by the workspace path). The caller must set the process working
// directory to the worktree before exec-ing the returned argv.
//
//   - Fresh launch (empty token): argv = ["acli", "rovodev", "run"]
//   - Resume a specific session (non-empty token = session UUID from the roster):
//     argv = ["acli", "rovodev", "run", "--restore", "<token>"]
//
// `acli rovodev run --restore <id>` restores the identified session; without a
// value it restores the most-recent session for the current workspace. cogitator
// always supplies the explicit session UUID (the ~/.rovodev/sessions/<uuid> dir
// name) so resume targets the exact session recorded in the roster.
//
// # Live-status capability
//
// LiveStatus is true: the polled Rovo Dev session monitor
// (internal/rovodev/provider.go) reports live vs recent sessions from the
// ~/.rovodev/sessions filesystem, so cogitator can distinguish running from
// stopped rows.
type rovodevHarness struct{}

// KindRovodev is the stable Kind string for the Rovo Dev harness.
const KindRovodev Kind = "rovodev"

func init() {
	DefaultRegistry.register(rovodevHarness{})
}

// Kind returns "rovodev".
func (rovodevHarness) Kind() Kind { return KindRovodev }

// Capabilities reports LiveStatus = true because the polled Rovo Dev monitor
// surfaces live/recent sessions from the ~/.rovodev/sessions filesystem.
func (rovodevHarness) Capabilities() Capabilities {
	return Capabilities{LiveStatus: true}
}

// LaunchArgv returns the argv to launch or resume Rovo Dev in worktree.
//
// The caller must set the process working directory to worktree before exec-ing
// the returned argv; Rovo Dev uses the CWD as its workspace and does not accept
// a directory positional argument.
//
// When token is non-empty it is treated as a session UUID and passed via
// `run --restore <token>`. When token is empty, Rovo Dev starts a fresh session.
func (rovodevHarness) LaunchArgv(_ string, token ResumeToken) []string {
	if token != "" {
		return []string{"acli", "rovodev", "run", "--restore", token}
	}
	return []string{"acli", "rovodev", "run"}
}
