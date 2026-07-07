# Architecture

- `internal/discovery` — mDNS browsing and add/remove events for opencode instances.
- `internal/supervisor` — per-instance lifecycle (permissions poll, recency poll, SSE loop,
  reconnect backoff).
- `internal/oc` — HTTP + SSE API access and generated OpenAPI-derived core types.
- `internal/state` — in-memory aggregation and dedupe across instances, attention
  classification, unreachable-instance tracking.
- `internal/ui` — Bubble Tea model, rendering, status mode, and footer warnings.
- `internal/config` — single source of timing/threshold defaults.
- `internal/workspace`, `git`, `harness`, `tmuxctl`, `pathnorm` — the worktree launcher seam
  (config, roster, merge, git, harness registry, tmux control, canonical paths).

## Development

Common local targets:

```sh
make vet
make lint
make test
make ci
```

OpenAPI workflow:

```sh
make capture-schema
make generate
```

See [CONTRIBUTING.md](https://github.com/guilhermehto/cogitator/blob/main/CONTRIBUTING.md)
for the full contributor guide.

## Roadmap

- macOS code signing + notarization (blocked on Apple Developer Program enrolment).
- OpenAPI-derived SSE event payload types (blocked on opencode publishing the event-stream
  schema).
