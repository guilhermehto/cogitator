package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/guilhermehto/cogitator/internal/taskwarrior"
)

// ClientAPI is the subset of taskwarrior.Client that the UI layer depends on.
// Declaring it here (rather than importing the concrete type) lets tests inject
// a fake without shelling out to the real `task` binary.
type ClientAPI interface {
	Available() bool
	Export(ctx context.Context) ([]taskwarrior.TaskView, error)
	Add(ctx context.Context, dsl string) error
	Modify(ctx context.Context, id, dsl string) error
	Done(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Undo(ctx context.Context) error
}

// tasksLoadedMsg carries the result of a loadTasksCmd invocation.
// err is non-nil when Export failed; tasks may be nil in that case.
type tasksLoadedMsg struct {
	tasks []taskwarrior.TaskView
	err   error
}

// taskMutationOkMsg signals that a mutation completed successfully.
// op names the operation (e.g. "add", "done", "undo") for display purposes.
type taskMutationOkMsg struct {
	op string
}

// taskMutationFailedMsg signals that a mutation failed.
// The Update loop surfaces err in the footer and does not refresh the list.
type taskMutationFailedMsg struct {
	op  string
	err error
}

// loadTasksCmd returns a tea.Cmd that calls Export with a fresh timeout context
// and delivers a tasksLoadedMsg. It is safe to call when c.Available() is false
// — the resulting message will carry the error from Export.
func loadTasksCmd(c ClientAPI, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		tasks, err := c.Export(ctx)
		return tasksLoadedMsg{tasks: tasks, err: err}
	}
}

// mutateCmd returns a tea.Cmd that calls fn with a fresh timeout context and
// delivers either taskMutationOkMsg or taskMutationFailedMsg.
//
// The factory deliberately does NOT chain loadTasksCmd on success. The Update
// loop reacts to taskMutationOkMsg by dispatching a fresh loadTasksCmd itself,
// which avoids the "Cmd returning a Cmd" confusion where a tea.Cmd would need
// to return another tea.Cmd rather than a tea.Msg.
func mutateCmd(c ClientAPI, timeout time.Duration, op string, fn func(ClientAPI, context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := fn(c, ctx); err != nil {
			return taskMutationFailedMsg{op: op, err: err}
		}
		return taskMutationOkMsg{op: op}
	}
}
