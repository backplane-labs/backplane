package broker

import (
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// Connected reports whether the NATS connection is up right now.
func (b *Broker) Connected() bool {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()

	return conn != nil && conn.IsConnected()
}

// JetStream is the service's own JetStream handle, for what the SDK does
// not wrap. It fails with env.ErrUnavailable before Connect and after
// Close; while the connection is down it is returned all the same (it
// reconnects by itself, its calls fail meanwhile).
func (b *Broker) JetStream() (jetstream.JetStream, error) {
	b.mu.Lock()
	conn, jet := b.conn, b.jet
	b.mu.Unlock()

	if conn == nil || conn.IsClosed() {
		return nil, fmt.Errorf("broker: not connected: %w", env.ErrUnavailable)
	}

	return jet, nil
}
