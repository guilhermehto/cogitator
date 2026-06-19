# Oh My Pi (omp) live attention

cogitator can display live attention signals for [Oh My Pi](https://github.com/oh-my-pi) (`omp`) sessions running on the same machine, using omp's extension/hook system. omp monitoring is **auto-enabled** when `~/.omp/agent/sessions` (or `$PI_CODING_AGENT_DIR/sessions`) exists on disk; cogitator behaves exactly as before on machines without an omp installation.

## How it works

cogitator monitors omp two ways:

- **Poll** — it reads the omp session store (`~/.omp/agent/sessions/**/*.jsonl`) on an interval to build the session roster: id, working directory, title, and last activity.
- **Live bridge** — a small JS extension installed into omp's extensions directory forwards a handful of lifecycle events to the `cogitator omp-hook` subcommand, which relays them over a local Unix-domain socket to the running cogitator TUI. This drives the crisp running/idle transition and the "waiting for you" signal.

| omp event | Attention state |
| --- | --- |
| `session_start` | active |
| `turn_start` | active |
| `turn_end` | idle / awaiting |
| `tool_call` (`ask` tool) | question-pending |
| `tool_result` (`ask` tool) | clears question-pending |
| `session_shutdown` | idle |

**If cogitator is not running**, the bridge's spawn of `cogitator omp-hook` fails silently and the event is dropped — a closed monitor is the expected case, not a failure, so omp sessions are never blocked or slowed. Only one cogitator instance owns the socket; a second instance runs without the live hook listener.

> **v1 capability boundary:** omp exposes no distinct tool-approval-prompt hook event, so cogitator does not surface an approval-pending state for omp this pass. The `ask` tool — omp's interactive "ask the user a question" mechanism — drives the question-pending signal instead.

---

## Step 1 — Enable omp monitoring in cogitator

cogitator reads its configuration from `internal/config/config.go`. The relevant fields are:

| Config field | Default | How to override |
| --- | --- | --- |
| `OMPEnabled` | auto-detected | _(no override — determined by directory presence)_ |
| `OMPHome` | `""` (resolves to `~/.omp/agent`) | Set `PI_CODING_AGENT_DIR=/path/to/agent/dir` in the environment. |

**Auto-detection:** cogitator enables omp monitoring automatically when the resolved omp sessions directory (`$PI_CODING_AGENT_DIR/sessions` if set, otherwise `~/.omp/agent/sessions`) exists and is a directory. No environment variable is needed on a machine that has run omp at least once.

---

## Step 2 — Install the live bridge

Run:

```sh
cogitator omp-hook install
```

This writes `cogitator-omp.js` into your omp extensions directory (`$PI_CODING_AGENT_DIR/extensions`, defaulting to `~/.omp/agent/extensions`), where omp auto-discovers it for every session. The absolute path to your `cogitator` binary is baked into the file, so the bridge works even when omp's process does not inherit your interactive shell `PATH`.

omp loads extensions at startup, so **restart any running omp sessions** for the bridge to take effect.

The installed extension is a thin forwarder — it registers a few `pi.on(...)` handlers and pipes each event to `cogitator omp-hook`. It blocks nothing and changes no omp behavior. To remove it, delete the file:

```sh
rm ~/.omp/agent/extensions/cogitator-omp.js
```

---

## Verification

With cogitator running (auto-enabled when `~/.omp/agent/sessions` exists) and the bridge installed, start an omp session in any directory. You should see a new omp session appear in the cogitator Sessions pane. When the agent calls the `ask` tool, the session's attention indicator should change to the question-pending state; when a turn finishes, it returns to idle.
