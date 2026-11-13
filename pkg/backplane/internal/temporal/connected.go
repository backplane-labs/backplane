package temporal

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

// Connected reports whether the client holds a connection that answered
// its last check: false before the first dial succeeds, after Close, and
// while the connection's health check fails.
func (c *Client) Connected() bool { return c.connected.Load() }

// ErrDisconnected: the client's connection did not answer its last check.
var ErrDisconnected = errors.New("temporal: not connected")

// Ready is nil while the connection answered its last health check.
func (c *Client) Ready(context.Context) error {
	if !c.Connected() {
		return ErrDisconnected
	}

	return nil
}

// Alive is always nil: the Temporal client's gRPC connection reconnects
// with capped backoff for as long as it is not closed, so no state of it
// needs a restart.
func (c *Client) Alive() error { return nil }

// setConnected records a change of the connection state.
func (c *Client) setConnected(ctx context.Context, connected bool) {
	if c.connected.Swap(connected) == connected {
		return
	}

	metrics.TransportConnected(context.WithoutCancel(ctx), transportTemporal, connected)
}

// watch checks the connection until ctx ends.
func (c *Client) watch(ctx context.Context) {
	t := time.NewTicker(c.health)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		conn := c.current()

		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()

		if conn == nil || closed {
			return
		}

		check, cancel := context.WithTimeout(ctx, c.p.Conn.DialTimeout)
		_, err := conn.CheckHealth(check, &client.CheckHealthRequest{})

		cancel()

		switch {
		case ctx.Err() != nil:
			return
		case err != nil && c.Connected():
			c.log.Warn("temporal connection lost", xlog.Err(err))
			c.setConnected(ctx, false)
		case err == nil && !c.Connected():
			c.log.Info("temporal connection restored")
			c.setConnected(ctx, true)
		}
	}
}
