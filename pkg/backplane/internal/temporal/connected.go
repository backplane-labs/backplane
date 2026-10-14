package temporal

import (
	"context"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

// Connected reports whether the client holds a connection that answered
// its last check: false before the first dial succeeds, after Close, and
// while the connection's health check fails.
func (c *Client) Connected() bool { return c.connected.Load() }

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
