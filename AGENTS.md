# AGENTS.md

`cogitator` is a terminal (Bubble Tea) TUI that monitors locally running [opencode](https://opencode.ai) instances discovered over mDNS, renders a live sessions view with attention signals, and (step-by-step, behind a workspace seam) launches/jumps/resumes git worktrees in tmux. Optional Taskwarrior pane. Go module `github.com/guilhermehto/cogitator` (Go 1.25), macOS + Linux only.

## Layout

- `cmd/cogitator/` — entrypoint + flags (`--status`, `--demo`, `--debug`, `--bell`).
- `internal/ui/` — Bubble Tea model/update/view (the only package that imports bubbletea).
- `internal/state/`, `discovery/`, `supervisor/`, `oc/` — mDNS discovery, SSE event ingest, opencode API (`oc/generated.go` is generated — see CONTRIBUTING).
- `internal/workspace/`, `git/`, `harness/`, `tmuxctl/`, `pathnorm/` — worktree launcher seam (config, roster, merge, git, harness registry, tmux control, canonical paths).
- `internal/config/`, `logging/`, `taskwarrior/` — config, logging, Taskwarrior client.

## Commands

- `make ci` — vet + lint + test + cross-build. Run before any PR.
- `make test` — `go test -race -count=1 ./...`.
- `make build` / `make run` — build/run the binary.
- `make generate` — regenerate opencode API models (requires schema; see CONTRIBUTING).

## Conventions

- Conventional commit subjects, imperative, scoped: `fix(ui): ...`, `feat(workspace): ...`. Branches `feat/`, `fix/`, `chore/`, `docs/`.
- Make the smallest correct change; match existing patterns; read before editing.
- Keep `internal/ui` the only bubbletea importer — the worktree-seam packages (`workspace`, `git`, `harness`, `tmuxctl`, `pathnorm`) must not import `internal/ui` or bubbletea.
- Compare worktree paths only via `pathnorm.Canonical` (git output, tmux `@cog_dir`, opencode `Directory` must reconcile).
- Shell-outs (git, tmux) run as `tea.Cmd`s off the UI goroutine — never block Update.
- Persist only durable facts (configured repos, session roster); derive everything else live.
- Don't commit, amend, or push unless asked.
