// Package agentfeed exposes the running TUI's attention-ordered agent list over
// a local socket so short-lived processes (pick, next, --status) can read it
// and request focus/seen actions without taking the single-instance lock. It
// must not import bubbletea or internal/ui.
package agentfeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/guilhermehto/cogitator/internal/agents"
	"github.com/guilhermehto/cogitator/internal/hookipc"
)

const (
	socketFilename = "cogitator-feed.sock"

	// protocolVersion is bumped on any incompatible wire change; a mismatch is
	// reported explicitly rather than guessed around.
	protocolVersion = 1

	// callTimeout keeps pick and next feeling instant when the TUI is wedged.
	callTimeout = 1500 * time.Millisecond
)

const (
	opList  = "list"
	opFocus = "focus"
	opSeen  = "seen"
)

var (
	// ErrNotRunning means no cogitator TUI is serving the feed socket.
	ErrNotRunning = errors.New("cogitator is not running")

	// ErrBusy means the TUI's action queue was full.
	ErrBusy = errors.New("cogitator busy")

	// ErrUnknownAgent means the id matched no agent in the latest list.
	ErrUnknownAgent = errors.New("unknown agent")
)

// ActionKind names what the TUI should do with an agent.
type ActionKind string

const (
	ActionFocus ActionKind = "focus"
	ActionSeen  ActionKind = "seen"
)

// Action is a validated request handed to the TUI for asynchronous execution.
type Action struct {
	Kind    ActionKind
	AgentID string
	Client  string
}

type request struct {
	Version int    `json:"version"`
	Op      string `json:"op"`
	ID      string `json:"id,omitempty"`
	Client  string `json:"client,omitempty"`
}

type response struct {
	Version int            `json:"version"`
	Agents  []agents.Agent `json:"agents,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// SocketPath returns the feed socket path, owner-only once bound.
func SocketPath() string {
	return hookipc.RuntimePath(socketFilename)
}

// Holder keeps the latest agent list for the server to answer from.
type Holder struct {
	mu     sync.RWMutex
	agents []agents.Agent
}

// Set replaces the list. It only swaps a slice header under a lock, so it is
// safe to call from Update.
func (h *Holder) Set(list []agents.Agent) {
	h.mu.Lock()
	h.agents = list
	h.mu.Unlock()
}

func (h *Holder) snapshot() []agents.Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.agents
}

func (h *Holder) has(id string) bool {
	for _, a := range h.snapshot() {
		if a.ID == id {
			return true
		}
	}

	return false
}

// Serve answers feed requests on sockPath until ctx is cancelled or the
// returned cleanup runs. list is answered from holder directly; focus and seen
// are validated, then enqueued on actions without blocking.
func Serve(ctx context.Context, sockPath string, holder *Holder, actions chan<- Action, logger *slog.Logger) (cleanup func(), err error) {
	handler := func(raw []byte) []byte {
		return encode(handle(raw, holder, actions))
	}

	return hookipc.ListenRPC(ctx, sockPath, handler, logger)
}

func handle(raw []byte, holder *Holder, actions chan<- Action) response {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return failure("malformed request")
	}

	if req.Version != protocolVersion {
		return failure(fmt.Sprintf("version mismatch: server %d, client %d", protocolVersion, req.Version))
	}

	switch req.Op {
	case opList:
		return response{Version: protocolVersion, Agents: holder.snapshot()}
	case opFocus:
		return enqueue(ActionFocus, req, holder, actions)
	case opSeen:
		return enqueue(ActionSeen, req, holder, actions)
	default:
		return failure("unknown op " + req.Op)
	}
}

func enqueue(kind ActionKind, req request, holder *Holder, actions chan<- Action) response {
	if !holder.has(req.ID) {
		return failure(ErrUnknownAgent.Error())
	}

	select {
	case actions <- Action{Kind: kind, AgentID: req.ID, Client: req.Client}:
		return response{Version: protocolVersion}
	default:
		return failure(ErrBusy.Error())
	}
}

func failure(message string) response {
	return response{Version: protocolVersion, Error: message}
}

func encode(resp response) []byte {
	out, err := json.Marshal(resp)
	if err != nil {
		out, _ = json.Marshal(failure("encode response"))
	}

	return out
}

// Client talks to a running cogitator's feed socket.
type Client struct {
	sockPath string
}

// NewClient returns a client for the default feed socket.
func NewClient() Client {
	return Client{sockPath: SocketPath()}
}

// NewClientAt returns a client for sockPath.
func NewClientAt(sockPath string) Client {
	return Client{sockPath: sockPath}
}

// List returns the TUI's agents in attention order.
func (c Client) List(ctx context.Context) ([]agents.Agent, error) {
	resp, err := c.call(ctx, request{Op: opList})
	if err != nil {
		return nil, err
	}

	return resp.Agents, nil
}

// Focus asks the TUI to jump the given tmux client to the agent.
func (c Client) Focus(ctx context.Context, id, client string) error {
	_, err := c.call(ctx, request{Op: opFocus, ID: id, Client: client})

	return err
}

// Seen asks the TUI to mark the agent as seen.
func (c Client) Seen(ctx context.Context, id string) error {
	_, err := c.call(ctx, request{Op: opSeen, ID: id})

	return err
}

func (c Client) call(ctx context.Context, req request) (response, error) {
	req.Version = protocolVersion
	payload, err := json.Marshal(req)
	if err != nil {
		return response{}, fmt.Errorf("agentfeed: encode request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	raw, err := hookipc.Call(ctx, c.sockPath, payload)
	if err != nil {
		if errors.Is(err, hookipc.ErrListenerUnavailable) {
			return response{}, ErrNotRunning
		}

		return response{}, fmt.Errorf("agentfeed: %w", err)
	}

	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return response{}, fmt.Errorf("agentfeed: decode response: %w", err)
	}

	if resp.Error != "" {
		return response{}, remoteError(resp.Error)
	}

	return resp, nil
}

func remoteError(message string) error {
	switch message {
	case ErrBusy.Error():
		return ErrBusy
	case ErrUnknownAgent.Error():
		return ErrUnknownAgent
	default:
		return errors.New("agentfeed: " + message)
	}
}
