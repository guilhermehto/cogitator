# Contributing

## Development loop

1. Install dependencies:

```sh
go mod download
```

2. Run local checks before opening a PR:

```sh
make ci
```

3. Run locally while developing:

```sh
make run
```

## Branch naming

Use short descriptive branches with a type prefix:

- `feat/<topic>`
- `fix/<topic>`
- `chore/<topic>`
- `docs/<topic>`

## Commit messages

Use Conventional-style commit subjects, matching existing history:

- `feat(scope): add unreachable footer`
- `fix(ui): keep child rows aligned`
- `chore(build): add goreleaser config`

Keep the subject line imperative and scoped.

## Claude Code hook setup (for contributors testing live attention)

To test the `claude-code` provider end-to-end, paste the following block into
`~/.claude/settings.json` under the top-level `"hooks"` key. cogitator does
**not** write this file; you must add it manually.

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

Restart Claude Code after editing the file. If cogitator is not running when a
hook fires, `cogitator claude-hook` exits 0 silently.

> **PATH note:** the hook runner may not inherit your interactive shell PATH. If `cogitator` is not found, replace `"cogitator claude-hook"` with its absolute path — e.g. `"/Users/you/go/bin/cogitator claude-hook"` (use `which cogitator` to find it).

## OpenAPI generation

The `internal/oc/generated.go` file is generated from `internal/oc/openapi.json`.

- Capture schema from a running `opencode` instance:

```sh
make capture-schema
```

- Regenerate models:

```sh
make generate
```

`make capture-schema` requires `opencode` on your `PATH`.

## Release flow

Releases are tag-driven.

1. Create and push a semver tag.
2. Run:

```sh
make release
```

Current releases are unsigned. On macOS, users may need to clear quarantine after extraction:

```sh
xattr -d com.apple.quarantine cogitator
```
