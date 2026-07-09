# Configuration

cogitator persists durable settings as JSON at `$XDG_CONFIG_HOME/cogitator/config.json`
(or `~/.config/cogitator/config.json` when `$XDG_CONFIG_HOME` is unset). The file is created
on first use and is safe to edit by hand. `launchMode` and `defaultHarness` have no in-app
setter, so editing this file is the only way to change them.

```json
{
  "repos": [
    "/Users/you/src/cogitator",
    "/Users/you/src/another-project"
  ],
  "defaultHarness": "opencode",
  "launchMode": "session"
}
```

| Field            | Type         | Default    | Description                                                                                                                                                                                                                                                                                                          |
| ---------------- | ------------ | ---------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `repos`          | string array | `[]`       | Absolute paths to the git repositories cogitator tracks for worktree launching. Normally managed from the UI (press `A` in the Sessions pane to fuzzy-find and add a repo), so entries usually appear here without hand-editing. Paths are canonicalized; a configured repo missing from disk is still listed but its worktree actions are disabled. |
| `defaultHarness` | string       | `opencode` | Harness pre-selected when you create a new worktree (`n`). One of `opencode`, `claude-code`, `codex`, `omp`, `rovodev`. Empty falls back to `opencode`.                                                                                                                                                                 |
| `launchMode`     | string       | `session`  | How a worktree opens in tmux: `window` or `session`. Empty or any unrecognized value falls back to `session`.                                                                                                                                                                                                           |

## tmux window vs session

`launchMode` controls how launching or jumping to a worktree places it in tmux:

- **`session`** (default): opens each worktree as its own **new tmux session**. Each worktree
  is isolated with its own window list; switch with tmux's session switcher (`prefix` + `s`)
  or cogitator's own switcher (`ctrl+P`). Best when you prefer one session per task or branch.
- **`window`**: opens each worktree as a new **window in your current tmux session**.
  Worktrees stay grouped under one session; move between them with your usual tmux window keys
  (`prefix` + number / `n` / `p`). Best when you run cogitator inside an existing tmux session
  and want everything in one place.

Either way, cogitator reuses an existing window/session for a worktree when one is already
open instead of creating a duplicate. Edits to `launchMode` take effect on the next launch;
no restart needed.
