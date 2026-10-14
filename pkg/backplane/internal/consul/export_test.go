package consul

import (
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
)

// UseFastTiming shrinks the presence clocks so renew and retry cycles take
// milliseconds; call before Start. The TTL stays at Consul's 10s minimum.
func (p *Presence) UseFastTiming() {
	p.timing = timing{
		ttl:         10 * time.Second,
		renewEvery:  40 * time.Millisecond,
		renewGiveUp: 200 * time.Millisecond,
		attempt:     2 * time.Second,
		call:        500 * time.Millisecond,
		establish:   backoff.Policy{Min: 20 * time.Millisecond, Max: 100 * time.Millisecond},
		renew:       backoff.Policy{Min: 10 * time.Millisecond, Max: 40 * time.Millisecond},
	}
}

// Session is the current session id.
func (p *Presence) Session() string { return p.currentSession() }

// SessionName is the name of this process's sessions.
func (p *Presence) SessionName() string { return p.name }

// SetRenewGiveUp overrides how long failed renews are tolerated: tests that
// assert "the session stays" must not lose it to a slow run.
func (p *Presence) SetRenewGiveUp(d time.Duration) { p.timing.renewGiveUp = d }
