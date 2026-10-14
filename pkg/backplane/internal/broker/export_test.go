package broker

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// Fast shortens the timings for tests; maxDeliver bounds deliveries.
func (b *Broker) Fast(maxDeliver int) {
	b.t.reconnectWait = 100 * time.Millisecond
	b.t.reconnectJitter = 50 * time.Millisecond
	b.t.publishTimeout = 2 * time.Second
	b.t.retry = backoff.Policy{Min: 50 * time.Millisecond, Max: 250 * time.Millisecond}
	b.t.maxDeliver = maxDeliver
	b.t.handlerTimeout = 5 * time.Second
	b.t.nak = backoff.Policy{Min: 20 * time.Millisecond, Max: 100 * time.Millisecond}
}

// Event is the metadata of one published event, for BuildHeaders.
type Event = cloudEvent

// BuildHeaders exposes headers.
func BuildHeaders(ctx context.Context, e Event) nats.Header { return headers(ctx, e) }

// DeadHeaders exposes deadHeaders.
func DeadHeaders(orig nats.Header, id, consumer, cause string, delivered uint64) nats.Header {
	return deadHeaders(orig, id, consumer, cause, delivered)
}

// NakDelay exposes nakDelay.
func NakDelay(p backoff.Policy, n uint64) time.Duration { return nakDelay(p, n) }

// Settings is what a reactor runs with.
type Settings struct {
	Consumer    jetstream.ConsumerConfig
	Concurrency int
	Timeout     time.Duration
	Nak         backoff.Policy
	StartAll    bool
}

// ReactorSettings resolves r over the broker's defaults.
func (b *Broker) ReactorSettings(r env.Reactor) (Settings, error) {
	re, err := b.reactorOf(r)
	if err != nil {
		return Settings{}, err
	}

	d := re.delivery

	return Settings{
		Consumer: b.consumerConfig(re), Concurrency: d.concurrency, Timeout: d.timeout, Nak: d.nak, StartAll: d.startAll,
	}, nil
}

// OwnStreams are the configurations of the service's event and dead-letter
// streams.
func (b *Broker) OwnStreams() (jetstream.StreamConfig, jetstream.StreamConfig) {
	return eventStream(b.p.Service, byEmitter, b.p.Streams), deadStream(b.p.Service, b.p.Streams)
}

// PublishRaw publishes payload as event with key.
func (b *Broker) PublishRaw(ctx context.Context, event, key string, payload []byte) error {
	return b.Publish(ctx, env.Message{Event: event, Key: key, Payload: payload})
}

// Incoming exposes incoming.
func Incoming(h nats.Header, consumer string, attempt uint64) env.Incoming {
	return incoming(h, consumer, attempt)
}

// PublishTimeout is the bound of a Publish without a deadline.
func (b *Broker) PublishTimeout() time.Duration { return b.t.publishTimeout }

// StopDeliveries exposes stopDeliveries.
const StopDeliveries = stopDeliveries
