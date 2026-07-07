# Live attention

Setup for each harness lives in [Connect your agent](/guide/connect). This page explains how
live attention behaves once it's wired up.

## Claude Code

cogitator subscribes to Claude Code's lifecycle hooks to track each session's attention state.
Monitoring is auto-enabled when `~/.claude/projects` exists.

If cogitator is not running when a hook fires, `cogitator claude-hook` exits 0 silently;
Claude Code shows no failure and never blocks your tool calls.

## Codex

cogitator subscribes to Codex's lifecycle hooks. Each event maps to an attention state:

| Event                       | Attention state    |
| --------------------------- | ------------------ |
| `SessionStart`              | active             |
| `UserPromptSubmit`          | active             |
| `PreToolUse` / `PostToolUse` | active             |
| `PermissionRequest`         | permission-pending |
| `Stop`                      | idle / awaiting    |

Hooks are enabled by default in Codex (`codex features list | grep hooks`). `PreToolUse` and
`PostToolUse` fire on every tool call; for less process churn, wire only `SessionStart`,
`PermissionRequest`, and `Stop` (see the minimal variant in the [Codex deep dive](/codex)).
If cogitator is not running when a hook fires, `cogitator codex-hook` exits 0 silently; Codex
shows no failure and never blocks your tool calls.

## omp

cogitator polls `~/.omp/agent/sessions/**/*.jsonl` so omp sessions appear with a
recency-derived liveness label without any setup. The shipped extension
(`extensions/cogitator.ts`) adds real-time attention by forwarding lifecycle events over
`cogitator omp-hook`:

| omp event                                        | Attention state  |
| ------------------------------------------------ | ---------------- |
| `session_start` / `turn_start` / `agent_start`   | active           |
| `tool_call` (tool `ask`)                         | question-pending |
| `tool_result` (error)                            | errored          |
| `turn_end` / `agent_end` / `session_shutdown`    | idle / awaiting  |

omp does not expose a permission-request hook event, so there is no distinct
permission-pending state for omp; the `ask` tool surfaces as question-pending. If cogitator
is not running, the extension's spawn fails silently and `cogitator omp-hook` exits 0; omp
shows no failure and never blocks your turns.
