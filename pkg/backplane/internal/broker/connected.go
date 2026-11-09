package broker

import (
	"errors"
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

// ErrClosed: the NATS connection closed for good without Close; it
// recovers only with a restart of the process.
var ErrClosed = errors.New("broker: the NATS connection closed for good")

// Alive is nil while the connection is up or reconnecting, ErrClosed once
// it closed by itself.
func (b *Broker) Alive() error {
	b.mu.Lock()
	conn, closing := b.conn, b.closing
	b.mu.Unlock()

	if conn != nil && conn.IsClosed() && !closing {
		return ErrClosed
	}

	return nil
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
