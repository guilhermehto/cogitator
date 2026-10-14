package ui

import (
	"os"

	"github.com/guilhermehto/cogitator/internal/mux"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/zellijctl"
)

const (
	hintNoMuxJump   = "no multiplexer — start cogitator inside tmux or zellij to use jump/resume"
	hintNoMuxCreate = "no multiplexer — start cogitator inside tmux or zellij to create worktrees"
)

// NewBackend returns the multiplexer cogitator runs in. zellij wins when
// $ZELLIJ is set, even if $TMUX is too: a zellij session started from a tmux
// pane inherits $TMUX, and the innermost multiplexer is the one the user sees.
// Otherwise it is tmux, whose Available is false outside any multiplexer.
func NewBackend() mux.Backend {
	return backendFor(os.Getenv)
}

func backendFor(getenv func(string) string) mux.Backend {
	if getenv("ZELLIJ") != "" {
		return zellijctl.Backend{Getenv: getenv}
	}

	return tmuxctl.Backend{}
}
