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
| `defaultHarness` | string       | `opencode` | Harness pre-selected when you create a new worktree (`n`). One of `opencode`, `claude-code`, `codex`, `omp`. Empty falls back to `opencode`.                                                                                                                                                                            |
| `launchMode`     | string       | `session`  | How a worktree opens in tmux: `window` or `session`. Empty or any unrecognized value falls back to `session`.                                                                                                                                                                                                           |
| `workspaceRoot`  | string       | `` (empty) | Directory under which workspace session bundles are created (one git worktree per member repo, per session). Empty uses `$XDG_DATA_HOME/cogitator/workspaces`, falling back to `~/.local/share/cogitator/workspaces` when `$XDG_DATA_HOME` is unset. A leading `~` is expanded. Rejected if it resolves inside an existing git working tree. |

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

## Workspaces

A **workspace** bundles several repos so you can work across all of them on one branch.
Creating a session inside a workspace checks out that branch as a real git worktree in
*every* member repo, laid out side by side under one session directory (rooted at
`workspaceRoot`, above). Press `Tab` to swap between the Sessions pane (single-repo
worktrees) and the Workspaces pane.

- **Real directories, not symlinks**: every supported harness (opencode, Claude Code, Codex,
  omp) searches with ripgrep, which skips symlinked directories unless you pass `-L`. Member
  worktrees are real checkouts, so ripgrep sees every file in them.
- **Disk cost**: one working-tree checkout per member repo, per session — git history itself
  is shared with the repo's other worktrees, not duplicated, but each session materializes
  its own copy of the checked-out files.
- **Divergent bases**: each member's branch is created from that repo's own current `HEAD`,
  so if repo A is on `main` and repo B is on a feature branch, their new worktrees start from
  different points.
- **Membership is independent of `repos`**: the flat `repos` list (Sessions pane) and a
  workspace's member repos are tracked separately — adding a repo to a workspace does not add
  it to `repos`, and vice versa.
- **Hidden repo basenames are rejected**: a member whose directory basename starts with `.`
  (e.g. `~/.dotfiles`) is refused, for the same reason ripgrep skips it — the worktree would
  be invisible to search.
- **Adding a member later**: attaching a repo to a workspace that already has sessions
  prompts which of those sessions to backfill with a new worktree for it; sessions you skip
  keep their existing member list.

`ctrl+P` lists workspace sessions in the session switcher too, labelled
`<workspace>/<session>`. See [Key bindings](/guide/key-bindings) for the Workspaces-pane keys
(`N`, `n`, `e`, `D`).
