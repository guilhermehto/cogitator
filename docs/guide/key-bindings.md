# Key bindings

| Key       | Context                                | Action                                                                                                                                                            |
| --------- | --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ctrl+P`  | anywhere (outside a prompt)             | open the session switcher: fuzzy-find a repo/branch or workspace session (listed as `<workspace>/<session>`) and jump to it (`cmd+P` is not supported; terminals don't forward it to TUI apps) |
| `Tab`     | anywhere (outside a prompt)             | swap focus between the Sessions and Workspaces panes                                                                                                            |
| `a`       | Sessions pane focused                   | toggle collapsed/expanded recent sessions                                                                                                                       |
| `A`       | Sessions pane focused                   | fuzzy-find and add a repository to the worktree roster                                                                                                         |
| `P`       | Sessions pane focused                   | pull latest into the highlighted worktree's branch (`git pull --ff-only --no-tags origin <branch>`); handy for refreshing a base branch before branching off it |
| `N`       | Workspaces pane focused                 | create a new, empty workspace                                                                                                                                   |
| `n`       | Workspaces pane focused                 | create a session in the workspace under the cursor: prompts for a session name, then a harness, then checks out one new branch across every member repo        |
| `e`       | Workspaces pane focused                 | open the repo-membership modal for the workspace under the cursor: attach a repo found under `$HOME`, or detach a current member                               |
| `D`       | Workspaces pane focused                 | delete the session or workspace under the cursor, behind a two-step `y`/`y` confirm that shows each member repo's branch merge status                          |
| `Enter`   | Workspaces pane focused, on a session row | launch the session in tmux                                                                                                                                    |
| `Esc`     | inside add/edit prompt                  | cancel the prompt without quitting                                                                                                                              |
| `Enter`   | inside add/edit prompt                  | submit the prompt                                                                                                                                               |

::: info
`Tab` inside the inline add/edit prompt is consumed by the text input widget (cursor
movement / suggestion acceptance is disabled, so Tab does nothing there). Use `Esc` to cancel
the prompt without quitting.
:::

See [Configuration → Workspaces](/guide/configuration#workspaces) for what a workspace is,
disk cost, and how membership works.
