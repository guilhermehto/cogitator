# Oh My Pi (omp) live attention

cogitator can display live attention signals for [Oh My Pi (omp)](https://oh-my-pi.dev) sessions running on the same machine. omp monitoring is **auto-enabled** when the omp agent directory (`~/.omp/agent`, or `$PI_CODING_AGENT_DIR` / `$PI_CONFIG_DIR/agent`) exists on disk; cogitator behaves exactly as before on machines without omp installed.

## How it works

Two layers feed the cogitator Sessions pane:

1. **Polling** — cogitator scans `~/.omp/agent/sessions/**/*.jsonl` every few seconds, reading each session's header (id, cwd, title, created) and last-activity timestamp. This makes every omp session appear with a recency-derived liveness label, with no setup beyond having omp installed.
2. **Live attention hook** — unlike Codex and Claude Code, omp has **no external command-hook** mechanism; its hooks are in-process TypeScript modules. cogitator ships a small extension (`internal/omp/cogitator.ts`, also embedded in the binary) that you install into omp via `cogitator omp-hook install`. It forwards session lifecycle events to the running cogitator over a local Unix-domain socket (`cogitator omp-hook`), so attention updates appear instantly instead of waiting for the next poll.

| omp event | Attention state |
| --- | --- |
| `session_start` / `turn_start` / `agent_start` | active (working) |
| `tool_call` (tool `ask`) | question-pending |
| `tool_result` (`isError`) | errored |
| `turn_end` / `agent_end` / `session_shutdown` | idle / awaiting |

**Limitation:** omp does not expose a permission-request hook event, and a pending approval is never written to the session file, so cogitator cannot show a distinct *permission-pending* state for omp the way it does for Codex/Claude. The agent asking you a question (the `ask` tool) is surfaced as *question-pending*; everything else is working / awaiting / errored.

**If cogitator is not running**, the extension's `cogitator omp-hook` spawn fails silently and `cogitator omp-hook` itself exits **0** — omp never shows a "hook failed" banner and your turns are never blocked. Only one cogitator instance owns the socket; a second instance runs poll-only without the live hook listener.

---

## Step 1 — Enable omp monitoring in cogitator

Nothing to do: monitoring auto-enables when the omp agent directory exists.

| Config field | Default | How to override |
| --- | --- | --- |
| `OmpEnabled` | auto-detected | _(no override — determined by directory presence)_ |
| `OmpHome` | `""` (resolves to `~/.omp/agent`) | Set `PI_CODING_AGENT_DIR=/path/to/omp/agent` (or `PI_CONFIG_DIR=/path/to/omp`) in the environment. |

cogitator resolves the agent directory as `$PI_CODING_AGENT_DIR`, else `$PI_CONFIG_DIR/agent`, else `~/.omp/agent`, and reads sessions from its `sessions/` subdirectory.

---

## Step 2 — Install the live-attention extension

Run the installer (works for `go install` users — the extension is embedded in
the binary, no repo checkout needed):

```sh
cogitator omp-hook install
```

This writes `~/.omp/agent/extensions/cogitator.ts` (creating the directory) with
the **absolute path of the cogitator binary baked in**, so the extension spawns
cogitator directly and does not depend on the omp process inheriting your
interactive shell PATH. It honors `$PI_CODING_AGENT_DIR` / `$PI_CONFIG_DIR` for
the target directory. Restart omp so the extension loads.

**Manual install (repo checkout)** — copy the source and let the bare `cogitator`
name resolve via the omp process PATH:

```sh
mkdir -p ~/.omp/agent/extensions
cp internal/omp/cogitator.ts ~/.omp/agent/extensions/cogitator.ts
```

Project-level installs also work — place it at `<repo>/.omp/extensions/cogitator.ts`.
The extension has no dependencies, registers its handlers on load, and forwards
events fire-and-forget so it never slows omp down. With a manual copy, make sure
`which cogitator` resolves in the shell that launches omp (the installer avoids
this by baking in the absolute path).

---

## Verification

With cogitator running (auto-enabled when `~/.omp/agent` exists) and the extension installed, start an omp session in any directory. You should see a new omp session appear in the cogitator Sessions pane within one poll interval. While omp is working a turn the session shows as active; when omp invokes the `ask` tool it shows question-pending; when the turn ends it shows idle / awaiting.

To confirm the extension is loaded, omp lists it under its loaded extensions (it derives the name `cogitator` from the file). Errors loading the extension surface in omp's startup, not in cogitator.
