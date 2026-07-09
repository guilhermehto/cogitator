package rovodev

import "github.com/guilhermehto/cogitator/internal/provider"

// PollOnceForTest exposes pollOnce for deterministic in-package testing.
// Tests can drive a single poll cycle with a pre-built session slice without
// touching the filesystem or waiting for the ticker.
func (p *Provider) PollOnceForTest(sink provider.Sink, sessions []Session) {
	p.pollOnce(sink, sessions)
}
