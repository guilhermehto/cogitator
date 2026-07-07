# Key bindings

| Key       | Context                     | Action                                                                                                                                                        |
| --------- | --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `T`       | anywhere (outside a prompt) | show or hide the Tasks pane                                                                                                                                    |
| `ctrl+P`  | anywhere (outside a prompt) | open the session switcher: fuzzy-find a repo/branch and jump to it (`cmd+P` is not supported — terminals don't forward it to TUI apps)                         |
| `Tab`     | anywhere (outside a prompt) | swap focus between Sessions and Tasks panes when Tasks is shown                                                                                                |
| `j` / `k` | Tasks pane focused          | move cursor down / up                                                                                                                                          |
| `a`       | Tasks pane focused          | open inline prompt to add a new task                                                                                                                           |
| `e`       | Tasks pane focused          | open inline prompt to edit the selected task                                                                                                                   |
| `s`       | Tasks pane focused          | start the selected task, or stop it if already running                                                                                                         |
| `d`       | Tasks pane focused          | mark the selected task done                                                                                                                                    |
| `D`       | Tasks pane focused          | prompt to delete the selected task (confirm with `y`)                                                                                                          |
| `U`       | Tasks pane focused          | undo the last Taskwarrior mutation                                                                                                                             |
| `a`       | Sessions pane focused       | toggle collapsed/expanded recent sessions                                                                                                                      |
| `A`       | Sessions pane focused       | fuzzy-find and add a repository to the worktree roster                                                                                                         |
| `P`       | Sessions pane focused       | pull latest into the highlighted worktree's branch (`git pull --ff-only --no-tags origin <branch>`); handy for refreshing a base branch before branching off it |
| `Esc`     | inside add/edit prompt      | cancel the prompt without quitting                                                                                                                             |
| `Enter`   | inside add/edit prompt      | submit the prompt                                                                                                                                              |

::: info
`Tab` inside the inline add/edit prompt is consumed by the text input widget (cursor
movement / suggestion acceptance is disabled, so Tab does nothing there). Use `Esc` to cancel
the prompt without quitting.
:::
