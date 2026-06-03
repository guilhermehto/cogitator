# Codex live attention (opt-in)

cogitator can display live attention signals for [Codex](https://openai.com/codex) sessions running on the same machine, using the Codex lifecycle hooks system. This is an opt-in feature; cogitator behaves exactly as before if it is not enabled.

## How it works

Each Codex lifecycle event (session start, tool use, permission request, stop) fires a hook that runs `cogitator codex-hook`. That subcommand reads the event JSON from stdin and forwards it over a local Unix-domain socket to the running cogitator TUI, which updates the session's live attention state:

| Event | Attention state |
| --- | --- |
| `SessionStart` | active |
| `UserPromptSubmit` | active |
| `PreToolUse` / `PostToolUse` | active |
| `PermissionRequest` | permission-pending |
| `Stop` | idle / awaiting |

**If cogitator is not running**, `cogitator codex-hook` exits with code 1 (never 2). Codex marks the hook run as failed and **continues** — it never blocks your tool calls or turns. Only one cogitator instance owns the socket; a second instance runs without the live hook listener.

`PreToolUse` and `PostToolUse` fire on every tool call. If you want less process churn, wire only `SessionStart`, `PermissionRequest`, and `Stop` (see the minimal snippet below).

---

## Step 1 — Enable Codex monitoring in cogitator

cogitator reads its configuration from `internal/config/config.go`. The relevant fields are:

| Config field | Default | How to override |
| --- | --- | --- |
| `CodexEnabled` | `false` | Set `CODEX_ENABLED=true` in the environment that launches cogitator, **or** modify `Default()` in `config.go` to return `true` (for a source build). |
| `CodexHome` | `""` (resolves to `~/.codex`) | Set `CODEX_HOME=/path/to/codex/home` in the environment. |

> **Note:** `CodexEnabled` defaults to `false`. cogitator will not start the Codex session monitor until this is set to `true`.

For a quick one-off test:

```sh
CODEX_ENABLED=true cogitator
```

To make it permanent, export `CODEX_ENABLED=true` in your shell profile (`.zshrc`, `.bashrc`, etc.) before launching cogitator.

---

## Step 2 — Verify hooks are enabled in Codex

Hooks are **enabled by default** in Codex. To confirm:

```sh
codex features list | grep hooks
```

You should see `hooks` listed as enabled. The canonical config key is `hooks`; `codex_hooks` is a deprecated alias.

To disable hooks (not recommended for this integration):

```toml
# ~/.codex/config.toml
[features]
hooks = false
```

---

## Step 3 — Wire the hooks

Choose one of the two formats below. Both are equivalent.

### Option A — `~/.codex/hooks.json` (recommended)

Create or edit `~/.codex/hooks.json`:

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

`cogitator` must be on your `$PATH`. If it is not, replace `"cogitator codex-hook"` with the absolute path, e.g. `"/usr/local/bin/cogitator codex-hook"`.

**Minimal variant** (less process churn — skips per-tool-call events):

```json
{
  "hooks": {
    "SessionStart":      [ { "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
    "PermissionRequest": [ { "matcher": "*", "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ],
    "Stop":              [ { "hooks": [ { "type": "command", "command": "cogitator codex-hook" } ] } ]
  }
}
```

Repo-local hooks also work: place the same JSON at `<repo>/.codex/hooks.json`.

### Option B — inline TOML in `~/.codex/config.toml`

```toml
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = "cogitator codex-hook"

[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "cogitator codex-hook"

[[hooks.PreToolUse]]
matcher = "*"
[[hooks.PreToolUse.hooks]]
type = "command"
command = "cogitator codex-hook"

[[hooks.PostToolUse]]
matcher = "*"
[[hooks.PostToolUse.hooks]]
type = "command"
command = "cogitator codex-hook"

[[hooks.PermissionRequest]]
matcher = "*"
[[hooks.PermissionRequest.hooks]]
type = "command"
command = "cogitator codex-hook"

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "cogitator codex-hook"
```

---

## Step 4 — Trust the hook (required)

Non-managed command hooks must be reviewed and trusted before Codex will run them. Until trusted, Codex silently skips the hook.

1. Start Codex normally: `codex`
2. Inside the Codex session, run the `/hooks` slash command.
3. Review the `cogitator codex-hook` entry and confirm trust.

After trusting, the hook fires on every subsequent Codex session without further prompts.

> **Advanced one-off:** `codex --dangerously-bypass-hook-trust` skips the trust check for a single session. Use only for testing; do not use in production.

---

## Verification

With cogitator running (`CODEX_ENABLED=true cogitator`) and the hooks wired and trusted, start a Codex session in any directory. You should see a new Codex session appear in the cogitator Sessions pane. When Codex requests a permission, the session's attention indicator should change to the permission-pending state.
