# Connect your agent

cogitator watches five harnesses. Each needs a one-time setup on the machine where the agent
runs; after that, sessions appear in the dashboard automatically. (Rovo Dev is the exception —
it needs no setup at all.)

Every harness has an **automated** path (paste a prompt into the agent itself and let it do
the setup) and a **manual** path.

## opencode

opencode advertises itself over mDNS and cogitator discovers it automatically. The only setup
is launching opencode with the `--mdns` flag, which could be added to your `opencode` alias.

**Automated.** Paste this to your agent:

```text
Add a shell alias named `ocm` to my shell startup file (~/.zshrc, ~/.bashrc, or whichever
my shell actually uses), defined as:

    alias ocm='opencode --mdns'

Preserve the rest of the file. Then tell me to reload my shell (or open a new terminal)
and start opencode with `ocm` from now on so cogitator can see it.
```

**Manual:**

Launch opencode with `--mdns` so it advertises on `_http._tcp.local.`:

```sh
opencode --mdns                       # default port (random)
opencode serve --mdns --port 7777     # headless, fixed port
```

You can launch as many as you like; cogitator discovers them automatically.

## Claude Code

cogitator displays live attention signals for [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
sessions using Claude Code's lifecycle hooks. Monitoring **auto-enables** when
`~/.claude/projects` exists. No environment variable needed.

**Automated.** Paste this to Claude Code:

```text
Set up cogitator live-attention monitoring for Claude Code on this machine.

1. Run `which cogitator` to find the absolute path to the cogitator binary. If it is not
   found, stop and tell me to install cogitator first.
2. Open ~/.claude/settings.json, creating it if it does not exist. Preserve every existing
   top-level key.
3. Merge the hooks below into the `hooks` object. Replace the bare command `cogitator`
   with the absolute path you found in step 1 (the hook runner may not inherit my
   interactive PATH):

   {
     "hooks": {
       "SessionStart":     [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "UserPromptSubmit": [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "PreToolUse":       [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "PostToolUse":      [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "Stop":             [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "Notification":     [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "SessionEnd":       [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ]
     }
   }

4. Save the file and tell me to restart Claude Code so the hooks take effect.
```

**Manual:**

1. Confirm `~/.claude/projects` exists (it does once you've run Claude Code at least once).
2. Wire the hooks in `~/.claude/settings.json`. cogitator does **not** write this file, so
   paste the block yourself:

   ```json
   {
     "hooks": {
       "SessionStart":     [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "UserPromptSubmit": [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "PreToolUse":       [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "PostToolUse":      [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "Stop":             [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "Notification":     [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ],
       "SessionEnd":       [ { "hooks": [ { "type": "command", "command": "cogitator claude-hook" } ] } ]
     }
   }
   ```

3. Restart Claude Code. Hooks take effect on the next session.

::: warning PATH note
The hook runner may not inherit your interactive shell PATH. If `cogitator` is not found,
replace `"cogitator claude-hook"` with its absolute path, e.g.
`"/Users/you/go/bin/cogitator claude-hook"` (use `which cogitator` to find it).
:::

See [Live attention → Claude Code](/reference/live-attention#claude-code) for how it behaves.

## Codex

cogitator displays live attention signals for [Codex](https://openai.com/codex) sessions using
Codex's lifecycle hooks. Monitoring **auto-enables** when `~/.codex` exists. No environment
variable needed.

**Automated.** Paste this to Codex:

```text
Set up cogitator live-attention monitoring for Codex on this machine.

1. Run `which cogitator` to find the absolute path to the cogitator binary. If it is not
   found, stop and tell me to install cogitator first.
2. Open ~/.codex/hooks.json, creating it if it does not exist. Preserve any existing keys.
3. Merge the hooks below into the `hooks` object. Replace the bare command `cogitator`
   with the absolute path you found in step 1:

   {
     "hooks": {
       "SessionStart":      [ { "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "UserPromptSubmit":  [ { "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "PreToolUse":        [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "PostToolUse":       [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "PermissionRequest": [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "Stop":              [ { "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ]
     }
   }

4. Save the file, then remind me to start `codex`, run `/hooks`, and confirm trust for
   `cogitator codex-hook` — Codex skips untrusted hooks silently.
```

**Manual:**

1. Confirm `~/.codex` exists (it does once Codex is installed).
2. Wire the hooks in `~/.codex/hooks.json`:

   ```json
   {
     "hooks": {
       "SessionStart":      [ { "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "UserPromptSubmit":  [ { "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "PreToolUse":        [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "PostToolUse":       [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "PermissionRequest": [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
       "Stop":              [ { "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ]
     }
   }
   ```

3. Trust the hook: start `codex`, run `/hooks`, and confirm trust for `cogitator codex-hook`.
   Until trusted, Codex skips the hook silently.

See the [Codex deep dive](/codex) for the full setup guide (inline TOML alternative, minimal
hook variant, and `CODEX_HOME` override), and
[Live attention → Codex](/reference/live-attention#codex) for how it behaves.

## omp

cogitator displays live attention signals for [Oh My Pi (omp)](https://oh-my-pi.dev) sessions.
Monitoring **auto-enables** when the omp agent directory (`~/.omp/agent`, or
`$PI_CODING_AGENT_DIR` / `$PI_CONFIG_DIR/agent`) exists. No environment variable needed. omp
sessions then appear in the Sessions pane from a filesystem poll alone.

omp has **no external command-hook** like Codex/Claude (its hooks are in-process TypeScript
modules), so live attention is wired through a small extension cogitator ships (embedded in
the binary). Install it with one command:

```sh
cogitator omp-hook install
```

This writes `~/.omp/agent/extensions/cogitator.ts` with the absolute cogitator binary path
baked in, so it works even when `cogitator` is not on the omp process PATH. Restart omp
afterward.

**Automated.** Paste this to omp:

```text
Set up cogitator live-attention monitoring for omp on this machine.

1. Run `which cogitator` to confirm cogitator is installed. If it is not found, stop
   and tell me to install cogitator first.
2. Run `cogitator omp-hook install` — it writes the live-attention extension into
   ~/.omp/agent/extensions/ with the cogitator binary path baked in.
3. Tell me to restart omp so the extension loads.
```

**Manual (repo checkout):** copy `internal/omp/cogitator.ts` to
`~/.omp/agent/extensions/cogitator.ts` (user-level) or `<repo>/.omp/extensions/cogitator.ts`
(project-level). With a manual copy the extension spawns `cogitator` by name, so ensure
`which cogitator` resolves in the shell that launches omp; the installer avoids this by
baking in the absolute path.

See the [omp deep dive](/omp) for the full setup guide and the event→attention mapping, and
[Live attention → omp](/reference/live-attention#omp) for how it behaves.

## Rovo Dev

cogitator monitors [Atlassian Rovo Dev CLI](https://www.atlassian.com/software/rovo) sessions
with **no setup at all**. Monitoring **auto-enables** when `~/.rovodev/sessions` exists (set
`ROVODEV_HOME` to point cogitator at a different location). Rovo Dev sessions then appear in the
Sessions pane from a filesystem poll alone — each `~/.rovodev/sessions/<id>/` directory supplies
the session's title, workspace, and a recency-derived liveness label.

Rovo Dev exposes no external command-hook that cogitator can wire (unlike Codex/Claude), so
there is no real-time permission/question/error attention for Rovo Dev yet: a recently active
session shows as active and fades to idle once its session files stop changing. Pressing
`enter` on a stopped Rovo Dev row resumes it with `acli rovodev run --restore <id>`.

See [Live attention → Rovo Dev](/reference/live-attention#rovo-dev) for how it behaves.
