package backplane

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gopherex/xlog"
)

// stopSlack is left of the stop budget after telemetry's flush, for the
// lifecycle to finish the stop.
const stopSlack = 250 * time.Millisecond

// window is one stop deadline several nodes share: the first of them to
// stop opens it for d, cut short so that reserve is left of the overall
// stop budget.
type window struct {
	d, reserve time.Duration

	once sync.Once
	at   time.Time
}

// derive bounds a node's stop by the window.
func (w *window) derive(ctx context.Context) (context.Context, context.CancelFunc) {
	w.once.Do(func() { w.at = keepReserve(ctx, time.Now().Add(w.d), w.reserve) })

	return context.WithDeadline(ctx, w.at)
}

// keepReserve moves deadline earlier so that reserve is left before ctx's
// own deadline.
func keepReserve(ctx context.Context, deadline time.Time, reserve time.Duration) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		if latest := dl.Add(-reserve); latest.Before(deadline) {
			return latest
		}
	}

	return deadline
}

// drainFor is the drain pause left in ctx: Shutdown.Drain, shortened so
// the listeners and the reserve still fit the stop budget.
func (c *core) drainFor(ctx context.Context) time.Duration {
	sd := c.cfg.Shutdown
	end := keepReserve(ctx, time.Now().Add(sd.Drain), sd.Listeners+sd.Reserve)

	return max(time.Until(end), 0)
}

// signals delivers SIGINT and SIGTERM (or the test's channel) until stop
// is called.
func (c *core) signals() (<-chan os.Signal, func()) {
	if c.opts.signals != nil {
		return c.opts.signals, func() {}
	}

	ch := make(chan os.Signal, 2) //nolint:mnd // the first signal and the second that forces exit
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	return ch, func() { signal.Stop(ch) }
}

// watchSignals cancels the run on the first signal. A signal once the
// stop is under way — the second one, or any after a stop for another
// reason began — exits the process at once with status 1.
func (c *core) watchSignals(sigs <-chan os.Signal, cancel context.CancelFunc, stopping, done <-chan struct{}) {
	requested := false

	for {
		select {
		case <-done:
			return
		case <-stopping:
			stopping, requested = nil, true
		case s := <-sigs:
			if !requested {
				requested = true

				c.log.Info("signal received, stopping", xlog.String("signal", s.String()))
				cancel()

				continue
			}

			c.log.Error("signal received during stop, exiting now", xlog.String("signal", s.String()))
			_ = c.log.Sync()
			c.exit(1)

			return
		}
	}
}

// exit ends the process (the test's function in tests).
func (c *core) exit(code int) {
	if c.opts.exit != nil {
		c.opts.exit(code)

		return
	}

	os.Exit(code)
}
