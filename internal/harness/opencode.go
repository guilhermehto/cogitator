package harness

// opencodeHarness implements Harness for the OpenCode coding agent.
//
// # Resume mechanism (confirmed via `opencode --help`, step 4)
//
// OpenCode exposes two resume flags:
//   - --continue / -c  : resume the most-recent session for the launch CWD
//   - --session <id>   : resume a specific session by id
//
// The per-directory session store is the primary resume path: launching
// opencode in the same working directory automatically offers the most-recent
// session. When a ResumeToken (session id) is available from the roster, we
// use --session <id> to target that specific session precisely. When the token
// is empty we rely on the per-directory store (no explicit flag needed).
//
// # mDNS advertisement
//
// --mdns enables mDNS service discovery so cogitator's discovery layer can
// detect the running instance and report LiveStatus = true.
type opencodeHarness struct{}

// KindOpenCode is the stable Kind string for the OpenCode harness.
const KindOpenCode Kind = "opencode"

func init() {
	DefaultRegistry.register(opencodeHarness{})
}

// Kind returns "opencode".
func (opencodeHarness) Kind() Kind { return KindOpenCode }

// Capabilities reports LiveStatus = true because OpenCode advertises its
// running state via mDNS + SSE, which cogitator's discovery layer consumes.
func (opencodeHarness) Capabilities() Capabilities {
	return Capabilities{LiveStatus: true}
}

// LaunchArgv returns the argv to launch or resume OpenCode in worktree.
//
// The caller must set the process working directory to worktree before exec-ing
// the returned argv; OpenCode uses the CWD as the session key.
//
// When token is non-empty it is treated as a session id and passed via
// --session <id>, targeting that specific session. When token is empty,
// OpenCode's per-directory store resumes the most-recent session automatically
// (no explicit flag required).
//
// --mdns is always included so cogitator's mDNS discovery can detect the
// running instance and update the row state to "running".
func (opencodeHarness) LaunchArgv(worktree string, token ResumeToken) []string {
	argv := []string{"opencode", "--mdns"}

	if token != "" {
		// Resume the specific session recorded in the roster.
		argv = append(argv, "--session", token)
	}
	// worktree is passed as the positional project argument so OpenCode opens
	// in the correct directory even if the caller cannot set the CWD (e.g. in
	// tests). In production the caller also sets CWD = worktree for robustness.
	argv = append(argv, worktree)

	return argv
}
