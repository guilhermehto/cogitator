# Architecture

- `internal/discovery`: mDNS browsing and add/remove events for opencode instances.
- `internal/supervisor`: per-instance lifecycle (permissions poll, recency poll, SSE loop,
  reconnect backoff).
- `internal/oc`: HTTP + SSE API access and generated OpenAPI-derived core types.
- `internal/state`: in-memory aggregation and dedupe across instances, attention
  classification, unreachable-instance tracking.
- `internal/ui`: Bubble Tea model, rendering, status mode, and footer warnings.
- `internal/config`: single source of timing/threshold defaults.
- `internal/settings`: single-repo config, session roster, merge, repo discovery.
- `internal/workspace`: multi-repo workspace/session domain — types, mutex-guarded
  `workspaces.json` store, session path derivation/validation, worktree-bundle
  assemble/teardown, live status joins.
- `internal/git`, `harness`, `tmuxctl`, `pathnorm`: git ops, harness registry, tmux control,
  canonical paths.

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
