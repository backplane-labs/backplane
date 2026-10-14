// Package audit reacts to the service's own Greeted event: a reactor is a
// durable consumer, so it sees every greeting of every instance, once each
// (it is idempotent under redelivery).
package audit

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
)

// Consumer is the reactor's pinned consumer name: moving or renaming the
// component keeps the consumer and its position on the stream.
const Consumer = "hello-greeted-audit"

// Delivery of the reactor; the rest are the defaults.
const (
	maxDeliver  = 3
	concurrency = 2
	timeout     = 5 * time.Second
	minDelay    = time.Second
	maxDelay    = 10 * time.Second
	// maxSeen bounds the ids remembered for deduplication.
	maxSeen = 4096
)

// ErrNoName: a Greeted without a name; no retry fixes it.
var ErrNoName = errors.New("audit: greeted without a name")

// Audit counts greetings per name as they come off the stream.
type Audit struct {
	deps.Component

	mu     sync.Mutex
	counts map[string]uint64
	seen   map[string]bool
	order  []string
}

// New creates the audit under parent and subscribes it to greeted.
func New(parent deps.Scope, greeted event.Ref[greeter.Greeted]) *Audit {
	a := &Audit{Component: deps.NewComponent(parent, "audit"), counts: map[string]uint64{}, seen: map[string]bool{}}

	event.React(a, greeted.Name(), a.handle,
		event.Consumer(Consumer),
		event.MaxDeliver(maxDeliver),
		event.Concurrency(concurrency),
		event.Timeout(timeout),
		event.Redelivery(minDelay, maxDelay),
		event.StartAt(event.StartNew),
	)

	return a
}

// handle counts one delivery; a redelivered event (same Delivery.ID) is
// counted once.
func (a *Audit) handle(ctx context.Context, g greeter.Greeted) error {
	if g.Name == "" {
		return event.Terminal(ErrNoName) //nolint:wrapcheck // marks ErrNoName, keeps it
	}

	d, _ := event.DeliveryOf(ctx)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.seen[d.ID] {
		return nil
	}

	a.remember(d.ID)
	a.counts[g.Name]++

	a.Log().Ctx().Debug(ctx, "greeting audited", xlog.String("name", g.Name), xlog.Int("attempt", d.Attempt),
		xlog.String("excited", d.Extensions["excited"]))

	return nil
}

// remember keeps id among the last maxSeen; a.mu is held.
func (a *Audit) remember(id string) {
	a.seen[id] = true
	a.order = append(a.order, id)

	if len(a.order) > maxSeen {
		delete(a.seen, a.order[0])
		a.order = a.order[1:]
	}
}

// Count is how many greetings of name were audited.
func (a *Audit) Count(name string) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.counts[name]
}

// Total is how many greetings were audited.
func (a *Audit) Total() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	var n uint64
	for _, c := range a.counts {
		n += c
	}

	return n
}
