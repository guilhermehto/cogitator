// Package provider defines the generic monitor-Provider seam through which
// any local coding-agent (opencode, Codex, Claude Code, …) feeds the sessions
// view. It is intentionally free of opencode wire types, bubbletea, and
// internal/state so that new providers can be added without touching the UI or
// the opencode pipeline.
//
// Dependency arrows: state → provider → harness. No cycle.
package provider

import (
	"context"
	"sync"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
)

// SessionUpdate is the neutral ingest payload any Provider pushes into a Sink.
// It carries exactly the inputs needed by state.Classify plus the identifiers
// required to key and group rows in the sessions view.
//
// InstanceID is the stable string that groups sessions under one "instance"
// heading. For opencode it is "host:port"; for Codex it is the synthetic
// constant "codex". It must be non-empty.
//
// SessionID is the provider-scoped session identifier. Combined with Provider
// it forms the collision-safe dedup key (provider, sessionID).
type SessionUpdate struct {
	// Provider identifies the harness that produced this update.
	Provider harness.Kind

	// InstanceID groups sessions under one instance heading.
	// opencode: "host:port"; Codex: "codex".
	InstanceID string

	// InstanceName is the human-readable label for the instance group.
	// May be empty; callers fall back to InstanceID.
	InstanceName string

	// SessionID is the provider-scoped session identifier.
	SessionID string

	// Title is the human-readable session title (may be empty).
	Title string

	// Slug is the short URL-safe label (may be empty).
	Slug string

	// Directory is the working directory for the session.
	Directory string

	// ParentID is the parent session id, if any.
	ParentID string

	// Agent is the agent name/model, if any.
	Agent string

	// StatusType is the raw status string fed to state.Classify
	// (e.g. "busy", "generating", "idle").
	StatusType string

	// HasPermission is true when a pending permission request exists for
	// this session.
	HasPermission bool

	// HasQuestion is true when a pending question tool request exists.
	HasQuestion bool

	// LastError is the time of the most recent error event (zero if none).
	LastError time.Time

	// LastActivity is the time of the most recent message or session update.
	LastActivity time.Time

	// Created is the session's initiation time (zero if unknown).
	Created time.Time

	// Source is "live" or "recent" — matches state.Source values.
	// Providers should use the string constants directly to avoid an import
	// cycle; the store validates/maps them.
	Source string
}

// Sink is the target a Provider pushes SessionUpdates into. state.Store
// implements Sink; the interface lives here so provider never imports state.
type Sink interface {
	// ApplyUpdate ingests a neutral session update. The store builds a
	// SessionView from the update and publishes a new snapshot if anything
	// changed.
	ApplyUpdate(u SessionUpdate)

	// RemoveProviderSession removes a single session from the provider's
	// instance. A no-op if the session is not present.
	RemoveProviderSession(providerKind harness.Kind, instanceID, sessionID string)

	// ClearProviderInstance removes all sessions for a provider instance
	// (e.g. when an opencode process disappears from mDNS).
	ClearProviderInstance(providerKind harness.Kind, instanceID string)

	// ReplaceProviderInstance atomically replaces the full session set for
	// (providerKind, instanceID) with updates. It publishes exactly one
	// snapshot when the set changes, and zero snapshots when it is
	// identical to the current set. Sessions for other providers or
	// instances are never touched.
	ReplaceProviderInstance(providerKind harness.Kind, instanceID string, updates []SessionUpdate)
}

// Provider is the interface any local coding-agent monitor must implement to
// feed the sessions view.
//
// Implementations must be safe for concurrent use. They must not import
// internal/ui, bubbletea, internal/oc, or internal/state.
type Provider interface {
	// Kind returns the harness kind this provider monitors.
	Kind() harness.Kind

	// Start begins monitoring and pushes SessionUpdates into sink until ctx
	// is cancelled. Start blocks until monitoring has stopped; callers run
	// it in a goroutine. A non-nil error indicates a fatal startup failure;
	// transient errors should be logged and retried internally.
	Start(ctx context.Context, sink Sink) error
}

// Manager holds a set of registered Providers and starts them all against a
// single Sink. It is provider-agnostic: it knows nothing about mDNS, opencode,
// or Codex — each Provider owns its own discovery mechanism.
//
// Manager must not import discovery, oc, state, or bubbletea.
type Manager struct {
	mu        sync.Mutex
	providers []Provider
}

// Register adds p to the manager. It is safe to call before Start.
func (m *Manager) Register(p Provider) {
	m.mu.Lock()
	m.providers = append(m.providers, p)
	m.mu.Unlock()
}

// Start launches every registered Provider in its own goroutine, passing ctx
// and sink to each. It returns immediately; providers run until ctx is
// cancelled. Fatal startup errors from individual providers are non-fatal to
// the manager — each provider is responsible for its own error handling.
func (m *Manager) Start(ctx context.Context, sink Sink) {
	m.mu.Lock()
	ps := append([]Provider(nil), m.providers...)
	m.mu.Unlock()
	for _, p := range ps {
		p := p
		go p.Start(ctx, sink) //nolint:errcheck // fatal errors handled inside each provider
	}
}
