package temporal_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

var (
	errStart = errors.New("start: unavailable")
	errFatal = errors.New("namespace not found")
)

// A worker's failed start is retried, and a worker the SDK stops on a
// fatal error is started again, until the context ends.
func TestKeepRestartsAFatalWorker(t *testing.T) {
	t.Parallel()

	c := temporal.New(temporal.Params{Service: "keep", Log: testlog.Discard()})
	c.UseFastRetry()

	var (
		starts  atomic.Int32
		running atomic.Pointer[func(error)]
	)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		c.Keep(ctx, func(onFatal func(error)) error {
			if starts.Add(1) == 1 {
				return errStart
			}

			running.Store(&onFatal)

			return nil
		})
	}()

	await := func(what string, cond func() bool) {
		t.Helper()

		for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("not reached: %s (%d starts)", what, starts.Load())
			}
		}
	}

	await("started after a failed start", func() bool { return starts.Load() == 2 && running.Load() != nil })

	fatal := *running.Load()
	fatal(errFatal)
	fatal(errFatal) // the SDK reports once; a second report does not block

	await("started again after the fatal stop", func() bool { return starts.Load() == 3 })

	cancel()
	<-done

	if n := starts.Load(); n != 3 {
		t.Fatalf("starts after cancel: %d", n)
	}
}
