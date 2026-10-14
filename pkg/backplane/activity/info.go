package activity

import (
	"context"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// Info is what a handler knows about the execution it serves.
type Info struct {
	// Attempt is 1 for the first execution, then counts retries.
	Attempt int32
	// Binding and Step name the binding or rule that called and its step,
	// as the caller put them in the envelope.
	Binding string
	Step    string
	// Key is the same for every attempt of one step execution and unique
	// across them: "<workflow id>/<activity id>". An idempotency key for
	// what the handler does outside.
	Key string
	// Deadline of this attempt; zero when unbounded.
	Deadline time.Time
}

// InfoOf is the Info of the execution ctx belongs to; false outside a
// handler run by the transport (a test calling the handler directly).
func InfoOf(ctx context.Context) (Info, bool) {
	i, ok := env.ActivityInfoOf(ctx)
	if !ok {
		return Info{}, false
	}

	return Info{Attempt: i.Attempt, Binding: i.Binding, Step: i.Step, Key: i.Key, Deadline: i.Deadline}, true
}

// Heartbeat reports progress of a long handler: the step's heartbeat
// timeout (HeartbeatTimeout) counts from the last one, and a cancelled
// step is noticed by ctx on the next one. details show as the pending
// activity's heartbeat details in Temporal. Outside a transport run it does
// nothing.
func Heartbeat(ctx context.Context, details ...any) {
	if i, ok := env.ActivityInfoOf(ctx); ok && i.Heartbeat != nil {
		i.Heartbeat(details...)
	}
}
