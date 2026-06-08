package harness

// claudeCodeHarness implements Harness for the Claude Code CLI coding agent.
//
// # Resume mechanism (claude --help, step 2)
//
// Claude Code takes no directory positional argument; it operates on the
// process CWD. The caller must set the process working directory to the
// worktree before exec-ing the returned argv.
//
//   - Fresh launch (empty token): argv = ["claude"]
//   - Resume a specific session (non-empty token = session UUID from roster):
//     argv = ["claude", "--resume", "<token>"]
//
// Note: a compacted or deleted session UUID passed to --resume may error rather
// than fall back to a fresh session — a known limitation matching codex's
// resume convention. Empty token always means fresh launch.
//
// # Live-status capability
//
// Claude Code advertises its running state via mDNS, which cogitator's
// discovery layer consumes. LiveStatus is true.
type claudeCodeHarness struct{}

// KindClaudeCode is the stable Kind string for the Claude Code harness.
const KindClaudeCode Kind = "claude-code"

func init() {
	DefaultRegistry.register(claudeCodeHarness{})
}

// Kind returns "claude-code".
func (claudeCodeHarness) Kind() Kind { return KindClaudeCode }

// Capabilities reports LiveStatus = true because Claude Code advertises its
// running state via mDNS, which cogitator's discovery layer consumes.
func (claudeCodeHarness) Capabilities() Capabilities {
	return Capabilities{LiveStatus: true}
}

// LaunchArgv returns the argv to launch or resume Claude Code in a worktree.
//
// The caller must set the process working directory to worktree before exec-ing
// the returned argv; Claude Code uses the CWD as its project root and does not
// accept a directory positional argument.
//
// When token is non-empty it is treated as a session UUID and passed via
// --resume <token>. When token is empty, Claude Code starts a fresh session.
func (claudeCodeHarness) LaunchArgv(_ string, token ResumeToken) []string {
	if token != "" {
		return []string{"claude", "--resume", token}
	}
	return []string{"claude"}
}
