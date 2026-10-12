package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/state"
)

type fakeFeed struct {
	list []agents.Agent
	err  error
}

func (f fakeFeed) List(context.Context) ([]agents.Agent, error) {
	return f.list, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestFormatStatusLine(t *testing.T) {
	rows := []state.SessionView{
		{Attention: state.AttnFinished},
		{Attention: state.AttnPermissionPending},
		{Attention: state.AttnPermissionPending},
		{Attention: state.AttnQuestionPending},
		{Attention: state.AttnErrored},
		{Attention: state.AttnActive},
	}

	got := formatStatusLine(rows)

	want := fmt.Sprintf("%s 1 %s 2 %s 1 %s 1", glyphQuestion, glyphPermission, glyphError, glyphFinished)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatStatusLineIsEmptyWhenNothingNeedsAttention(t *testing.T) {
	if got := formatStatusLine([]state.SessionView{{Attention: state.AttnActive}}); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestStatusFromFeedCountsPermissionAndFinished(t *testing.T) {
	feed := fakeFeed{list: []agents.Agent{
		{ID: "a", Group: agents.GroupNeedsYou, Attention: state.AttnPermissionPending},
		{ID: "b", Group: agents.GroupDone, Attention: state.AttnFinished},
	}}

	line, ok := statusFromFeed(context.Background(), feed, discardLogger())

	want := fmt.Sprintf("%s 1 %s 1", glyphPermission, glyphFinished)
	if !ok || line != want {
		t.Fatalf("got (%q, %v), want (%q, true)", line, ok, want)
	}
}

func TestStatusFromFeedCountsErroredAgents(t *testing.T) {
	feed := fakeFeed{list: []agents.Agent{
		{ID: "a", Group: agents.GroupNeedsYou, Attention: state.AttnErrored},
	}}

	line, ok := statusFromFeed(context.Background(), feed, discardLogger())

	want := fmt.Sprintf("%s 1", glyphError)
	if !ok || line != want {
		t.Fatalf("got (%q, %v), want (%q, true)", line, ok, want)
	}
}

func TestStatusFromFeedEmptyListIsAuthoritative(t *testing.T) {
	line, ok := statusFromFeed(context.Background(), fakeFeed{}, discardLogger())

	if !ok || line != "" {
		t.Fatalf("got (%q, %v), want (\"\", true)", line, ok)
	}
}

func TestStatusFromFeedFallsBackWhenTUINotRunning(t *testing.T) {
	_, ok := statusFromFeed(context.Background(), fakeFeed{err: agentfeed.ErrNotRunning}, discardLogger())

	if ok {
		t.Fatal("expected fallback to discovery")
	}
}

func TestStatusFromFeedFallsBackOnOtherErrors(t *testing.T) {
	_, ok := statusFromFeed(context.Background(), fakeFeed{err: errors.New("version mismatch")}, discardLogger())

	if ok {
		t.Fatal("expected fallback to discovery")
	}
}
