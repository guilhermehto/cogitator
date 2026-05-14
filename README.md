# opencode-monitor

Throwaway POC: a TUI command centre that gives a **live** multi-session view of
locally-running [opencode](https://opencode.ai) instances and triages sessions
that need attention (pending permission requests and errors), while separating
active work from inactive context.

**Status:** proof of concept. macOS only. Read-only; no controls. Don't ship.

## Prerequisite

Each opencode instance you want to monitor must be launched with `--mdns` so
it advertises itself on `_http._tcp.local.`:

```sh
opencode --mdns                       # default port (random)
opencode serve --mdns --port 7777     # headless, fixed port
```

You can launch as many as you like; the monitor discovers them automatically.

## Run

```sh
go run ./cmd/opencode-monitor
```

`q`, `Esc`, or `Ctrl+C` quits. A debug log is written to
`/tmp/opencode-monitor.log`.

## What you'll see

Two panes, split horizontally:

- **Sessions** (left): grouped by instance.
  - **Live** rows (bright): observed via SSE during this monitor run.
    `active` means the session is currently doing work (`busy`/`generating`).
    Non-busy top-level sessions render as `inactive`.
    Subagents nest under their parent with `↳` and an `@agent-name` tag.
    Finished subagents (`idle`/empty status) are hidden unless they have a
    pending permission request or an unresolved error.
  - **Recent** rows (dim, italic `recent` label): pulled from each instance's
    project session list, filtered to those updated in the last 30 minutes.
    Promoted to "live" the moment any event arrives for them.
- **Needs attention** (right): subset of the above with a pending permission
  request or an unresolved error. Sorted by urgency.

## How it works

For each discovered instance the monitor:

1. Subscribes to `GET /global/event` (NOT `GET /event`, which is silently
   scoped to the requesting client's directory).
2. Polls `GET /permission` every 5s so a permission raised before the
   monitor connected still surfaces.
3. Polls `GET /session` every 30s and keeps rows whose `time.updated` falls
   within the last 30 minutes — discovery for sessions you were already
   working on before the monitor started.
4. Asynchronously fetches `GET /session/{id}` the first time it sees an
   unfamiliar session ID, just to populate title/slug/agent for display.

## What you can't see, and why

opencode does **not** expose "which session is currently open in this TUI".
The `/tui/*` endpoints are *control* (tell a TUI to do something), not state
queries. The TUI process owns its current selection internally and never
broadcasts it. So the closest proxy is **recency** — sessions touched in the
last N minutes are the ones you're likely working on. That's what the recent
import shows.

opencode also shares **one database across all running processes**:

- `GET /global/event` echoes every event to every connected process.
- `GET /experimental/session` returns the same global list to every process.
- `GET /session` is project-scoped to the requesting process's working
  directory, which is why we use it for recency import — it gives natural
  per-instance scoping when your opencode instances are in different cwds.

If you have two opencode instances started in the **same** cwd, you'll see
their session lists overlap. That's a faithful reflection of opencode's
model, not a bug.

## Lifecycle

When an instance disappears (process killed, mDNS announce gone) its sessions
are silently dropped. When it comes back, the next mDNS browse pass re-adds
it and the recency import refills its rows.

## Why throwaway

The goal is to validate the pipeline (mDNS → `/global/event` + `/session`
recency → classifier → TUI) and the two-pane UX. A real version would
replace the ad-hoc HTTP types with a generated client, add interactivity
(approve permissions, jump into a session), and likely surface opencode's
TUI-current-session out of band (a small opencode plugin could broadcast
selection events on `tui.session.select` via `/global/event`, eliminating
the recency proxy).
