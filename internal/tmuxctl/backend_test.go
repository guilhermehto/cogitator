package tmuxctl

import (
	"errors"
	"reflect"
	"testing"

	"github.com/guilhermehto/cogitator/internal/mux"
)

func assertCalls(t *testing.T, r *fakeRunner, want ...[]string) {
	t.Helper()
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls:\n got %q\nwant %q", r.calls, want)
	}
}

func TestBackendSelect_SessionModeSwitchesClientToSession(t *testing.T) {
	withTMUX(t)
	r := &fakeRunner{}

	if err := (Backend{Runner: r}).Select("repo-a:2", mux.ModeSession, "c1"); err != nil {
		t.Fatal(err)
	}

	assertCalls(t, r, []string{"switch-client", "-c", "c1", "-t", "repo-a"})
}

func TestBackendSelect_WindowModeSelectsWindowThenSwitches(t *testing.T) {
	withTMUX(t)
	r := &fakeRunner{}

	if err := (Backend{Runner: r}).Select("main:5", mux.ModeWindow, ""); err != nil {
		t.Fatal(err)
	}

	assertCalls(t, r,
		[]string{"select-window", "-t", "main:5"},
		[]string{"switch-client", "-t", "main:5"},
	)
}

func TestBackendKill_ByMode(t *testing.T) {
	withTMUX(t)

	r := &fakeRunner{}
	if err := (Backend{Runner: r}).Kill("repo-a:2", mux.ModeSession); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, r, []string{"kill-session", "-t", "repo-a"})

	r = &fakeRunner{}
	if err := (Backend{Runner: r}).Kill("@4", mux.ModeWindow); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, r, []string{"kill-window", "-t", "@4"})
}

func TestBackendCurrentLocation_ReadsClientWindowSession(t *testing.T) {
	withTMUX(t)
	r := &fakeRunner{}
	r.push("/dev/ttys003\n", nil)
	r.push("@7\n", nil)
	r.push("$2\n", nil)

	loc, err := (Backend{Runner: r}).CurrentLocation()
	if err != nil {
		t.Fatal(err)
	}

	want := mux.Location{Client: "/dev/ttys003", WindowID: "@7", SessionID: "$2"}
	if loc != want {
		t.Errorf("got %+v, want %+v", loc, want)
	}
	assertCalls(t, r,
		[]string{"display-message", "-p", "#{client_name}"},
		[]string{"display-message", "-p", "#{window_id}"},
		[]string{"display-message", "-p", "#{session_id}"},
	)
}

func TestBackendCurrentLocation_FailedFieldStaysEmpty(t *testing.T) {
	withTMUX(t)
	r := &fakeRunner{}
	r.push("", errors.New("no client"))
	r.push("@7\n", nil)
	r.push("$2\n", nil)

	loc, err := (Backend{Runner: r}).CurrentLocation()
	if err == nil {
		t.Fatal("want error from failed client lookup")
	}
	if loc != (mux.Location{WindowID: "@7", SessionID: "$2"}) {
		t.Errorf("got %+v", loc)
	}
}

func TestBackendSelfPaneID_ReadsTMUXPane(t *testing.T) {
	t.Setenv("TMUX_PANE", "%42")

	if got := (Backend{}).SelfPaneID(); got != "%42" {
		t.Errorf("got %q, want %%42", got)
	}
}
