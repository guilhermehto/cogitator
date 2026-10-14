package agentfeed

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/hookipc"
)

func shortSockPath(t *testing.T) string {
	t.Helper()

	f, err := os.CreateTemp("", "feed*.sock")
	if err != nil {
		t.Fatalf("create temp socket path: %v", err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })

	return path
}

func startServer(t *testing.T, list []agents.Agent, queue int) (Client, string, chan Action) {
	t.Helper()

	holder := &Holder{}
	holder.Set(list)
	actions := make(chan Action, queue)
	path := shortSockPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cleanup, err := Serve(ctx, path, holder, actions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(cleanup)

	return NewClientAt(path), path, actions
}

func twoAgents() []agents.Agent {
	return []agents.Agent{{ID: "/a", Branch: "one"}, {ID: "/b", Branch: "two"}}
}

func TestListReturnsAgentsInOrder(t *testing.T) {
	client, _, _ := startServer(t, twoAgents(), 1)

	got, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(got) != 2 || got[0].ID != "/a" || got[1].ID != "/b" || got[1].Branch != "two" {
		t.Fatalf("List = %+v", got)
	}
}

func TestFocusEnqueuesActionForKnownAgent(t *testing.T) {
	client, _, actions := startServer(t, twoAgents(), 1)

	if err := client.Focus(context.Background(), "/b", "/dev/ttys001"); err != nil {
		t.Fatalf("Focus: %v", err)
	}

	select {
	case got := <-actions:
		want := Action{Kind: ActionFocus, AgentID: "/b", Client: "/dev/ttys001"}
		if got != want {
			t.Fatalf("action = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("no action enqueued")
	}
}

func TestSeenEnqueuesSeenAction(t *testing.T) {
	client, _, actions := startServer(t, twoAgents(), 1)

	if err := client.Seen(context.Background(), "/a"); err != nil {
		t.Fatalf("Seen: %v", err)
	}

	if got := <-actions; got.Kind != ActionSeen || got.AgentID != "/a" {
		t.Fatalf("action = %+v", got)
	}
}

func TestFocusRejectsUnknownAgentWithoutEnqueueing(t *testing.T) {
	client, _, actions := startServer(t, twoAgents(), 1)

	err := client.Focus(context.Background(), "/nope", "c")
	if err != ErrUnknownAgent {
		t.Fatalf("err = %v, want ErrUnknownAgent", err)
	}

	if len(actions) != 0 {
		t.Fatal("action enqueued for unknown agent")
	}
}

func TestFocusReportsBusyWhenQueueFull(t *testing.T) {
	client, _, _ := startServer(t, twoAgents(), 1)

	if err := client.Focus(context.Background(), "/a", "c"); err != nil {
		t.Fatalf("first Focus: %v", err)
	}

	if err := client.Focus(context.Background(), "/a", "c"); err != ErrBusy {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
}

func TestListReturnsErrNotRunningWithoutServer(t *testing.T) {
	client := NewClientAt(shortSockPath(t))

	if _, err := client.List(context.Background()); err != ErrNotRunning {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestRejectsMismatchedVersionExplicitly(t *testing.T) {
	_, path, _ := startServer(t, twoAgents(), 1)

	raw, err := hookipc.Call(context.Background(), path, []byte(`{"version":99,"op":"list"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if !strings.Contains(string(raw), "version mismatch") {
		t.Fatalf("reply = %s", raw)
	}
}

func TestSocketIsOwnerOnly(t *testing.T) {
	_, path, _ := startServer(t, twoAgents(), 1)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
}
