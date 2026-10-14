package temporal

import (
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
)

// UseFastRetry shortens the delay between connection and worker start
// attempts and between connection checks; call before Connect.
func (c *Client) UseFastRetry() {
	c.retry = backoff.Policy{Min: 50 * time.Millisecond, Max: 200 * time.Millisecond}
	c.health = 100 * time.Millisecond
}

// WorkerOptions are the options the worker is built with.
func (c *Client) WorkerOptions() worker.Options { return c.workerOptions() }

// Options are the options the client dials with.
func (c *Client) Options() (client.Options, error) { return c.options() }

// DialTimeout is the bound of one connection attempt.
func (c *Client) DialTimeout() time.Duration { return c.p.Conn.DialTimeout }
