package omp

import "github.com/guilhermehto/cogitator/internal/provider"

// PollOnceForTest exposes pollOnce for deterministic in-package testing. Tests
// can drive a single poll cycle with a pre-built session slice without touching
// the filesystem or waiting for the ticker.
func (p *Provider) PollOnceForTest(sink provider.Sink, sessions []Session) {
	p.pollOnce(sink, sessions)
}

// HandleHookFrameForTest exposes handleHookFrame so tests can drive the hook
// overlay path without a live socket.
func (p *Provider) HandleHookFrameForTest(raw []byte, sink provider.Sink) {
	p.handleHookFrame(raw, sink)
}

// ReadSessionsForTest re-exports ReadSessions so external test packages can
// call it without importing the internal package directly.
var ReadSessionsForTest = ReadSessions
