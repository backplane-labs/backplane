// Package broker carries events over NATS JetStream (design §8).
//
// Every service has one stream, bp_<service>, with subjects
// bp.<service>.<Event>. The emitter creates or updates it at start with the
// operator's Streams (its configuration wins); a subscriber creates it when
// missing, with the platform defaults, so it exists before the emitter
// first runs. Events are
// CloudEvents in NATS binary mode: ce-* headers, JSON data, Nats-Msg-Id =
// ce-id for deduplication and the W3C trace context. The publisher may set
// ce-id itself (an outbox's id), ce-time and extension attributes.
//
// A reactor is a durable pull consumer <subscriber>__<consumer> filtered on
// one event, delivered as its env.Delivery says over the broker's defaults.
// A handler that fails is redelivered with a growing delay; when
// the last delivery fails, or the handler marks its error terminal, the
// message goes to the dead-letter subject bp.dlq.<subscriber>.<consumer>
// (stream bp_dlq_<subscriber>) and is terminated. A handler cancelled by
// the service's stop is nacked without a delay and never dead-lettered.
// A consumer deleted on the server while the service runs is recreated at
// the reactor's position (the oldest unsettled message, else
// after the newest one seen) and consumption resumes.
//
// NATS is optional and may be down: Connect does not wait for it, streams
// and consumers are ensured in the background, Publish fails fast while
// disconnected, and consumption resumes after a reconnect.
package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xtrace"

	infranats "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

const (
	instrumentation = "github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	// transport is the attribute of backplane.transport.connected.
	transport = "nats"
)

// ErrNoService: the broker has no service name to derive NATS names from.
var ErrNoService = errors.New("broker: service name is empty")

// Params of New.
type Params struct {
	// Conn is the connection: URL, credentials, TLS.
	Conn infranats.Config
	// PublishTimeout bounds a Publish whose ctx has no deadline; zero is
	// the default (5s).
	PublishTimeout time.Duration
	Service        string
	Instance       string
	Version        string
	Log            *xlog.Logger
	Env            *env.Env
	// Streams the service owns; the zero value takes the platform
	// defaults.
	Streams Streams
}

// Streams are the operator's settings of the streams the service owns:
// its event stream bp_<service> and its dead letters bp_dlq_<service>.
// Zero MaxAge, MaxBytes or DeadMaxAge mean unlimited.
type Streams struct {
	MaxAge     time.Duration // retention of the service's events
	MaxBytes   int64         // size of the event stream, oldest dropped first
	Replicas   int           // of both streams
	Duplicates time.Duration // dedup window of the event stream
	DeadMaxAge time.Duration // retention of the dead letters
}

// DefaultStreams are the platform defaults of Streams.
func DefaultStreams() Streams {
	return Streams{
		MaxAge: eventsMaxAge, Replicas: 1, Duplicates: dedupWindow, DeadMaxAge: deadMaxAge,
	}
}

// tuning holds the timings; tests shorten them.
type tuning struct {
	// Connection.
	reconnectWait   time.Duration
	reconnectJitter time.Duration
	publishTimeout  time.Duration // when the caller's ctx has no deadline
	// Background ensure of streams and consumers.
	retry backoff.Policy
	// Reactors, unless the reactor sets its own (env.Delivery).
	maxDeliver     int
	handlerTimeout time.Duration
	nak            backoff.Policy // delay after the n-th failed delivery
	concurrency    int            // handlers in flight per reactor
	// How often a reactor of every service looks for new event streams.
	discover time.Duration
}

// Defaults of tuning.
const (
	defReconnectWait   = 2 * time.Second
	defReconnectJitter = time.Second
	defPublishTimeout  = 5 * time.Second
	defRetryFloor      = 500 * time.Millisecond
	defRetryCeil       = 15 * time.Second
	defMaxDeliver      = 5
	defHandlerTimeout  = 30 * time.Second
	defNakFloor        = time.Second
	defNakCeil         = time.Minute
	defConcurrency     = 4
	defDiscover        = 30 * time.Second
)

func defaults() tuning {
	return tuning{
		reconnectWait:   defReconnectWait,
		reconnectJitter: defReconnectJitter,
		publishTimeout:  defPublishTimeout,
		retry:           backoff.Policy{Min: defRetryFloor, Max: defRetryCeil},
		maxDeliver:      defMaxDeliver,
		handlerTimeout:  defHandlerTimeout,
		nak:             backoff.Policy{Min: defNakFloor, Max: defNakCeil},
		concurrency:     defConcurrency,
		discover:        defDiscover,
	}
}

// Broker is the service's NATS connection: it publishes the service's
// events and runs its reactors.
type Broker struct {
	p   Params
	t   tuning
	log *xlog.Logger

	mu     sync.Mutex
	conn   *nats.Conn
	jet    jetstream.JetStream
	closed chan struct{} // closed when the connection is closed

	reactors reactors
}

// New creates the broker; nothing connects until Connect.
func New(p Params) *Broker {
	log := p.Log
	if log == nil {
		log = xlog.New(xlog.NopCore{})
	}

	if p.Streams == (Streams{}) {
		p.Streams = DefaultStreams()
	}

	t := defaults()
	if p.PublishTimeout > 0 {
		t.publishTimeout = p.PublishTimeout
	}

	return &Broker{p: p, t: t, log: log, closed: make(chan struct{})}
}

// Connect connects without blocking on an unreachable NATS and ensures the
// service's stream; background work runs on g.
func (b *Broker) Connect(ctx context.Context, g node.Group) error {
	if b.p.Service == "" {
		return ErrNoService
	}

	if err := checkService(b.p.Service); err != nil {
		return err
	}

	conn, err := b.dial()
	if err != nil {
		return fmt.Errorf("broker: %w", err)
	}

	jet, err := jetstream.New(conn)
	if err != nil {
		conn.Close()

		return fmt.Errorf("broker: jetstream: %w", err)
	}

	b.mu.Lock()
	b.conn, b.jet = conn, jet
	b.mu.Unlock()

	metrics.TransportConnected(ctx, transport, conn.IsConnected())

	if !conn.IsConnected() {
		b.log.Warn("nats unreachable, connecting in the background")
	}

	if b.p.Env == nil || !b.p.Env.Manifest.HasEvents() {
		return nil
	}

	if conn.IsConnected() {
		sctx, cancel := context.WithTimeout(ctx, b.t.publishTimeout)
		err := b.ensureOwn(sctx)

		cancel()

		if err == nil {
			return nil
		}

		b.log.Warn("event stream not ensured, retrying in the background", xlog.Err(err))
	}

	g.Go(func(ctx context.Context) error {
		err := backoff.Retry(ctx, b.t.retry, b.ensureOwn, func(err error, in time.Duration) {
			b.log.Debug("event stream not ensured", xlog.Err(err), xlog.Duration("retry_in", in))
		})
		if err == nil {
			b.log.Info("event stream ensured", xlog.String("stream", StreamName(b.p.Service)))
		}

		return nil // only the node's stop ends the retries
	})

	return nil
}

// dial connects through infra/nats: never gives up, logs the transitions,
// reports connectivity as a metric.
func (b *Broker) dial() (*nats.Conn, error) {
	name := b.p.Instance
	if name == "" {
		name = b.p.Service
	}

	var once sync.Once

	//nolint:wrapcheck // Connect names nats
	return infranats.Connect(b.p.Conn, infranats.Options{
		Name: name, Log: b.log,
		OnStatus:      func(connected bool) { metrics.TransportConnected(context.Background(), transport, connected) },
		OnClosed:      func() { once.Do(func() { close(b.closed) }) },
		ReconnectWait: b.t.reconnectWait, ReconnectJitter: b.t.reconnectJitter,
	})
}

// connected returns JetStream while the connection is up.
func (b *Broker) connected() (jetstream.JetStream, error) {
	b.mu.Lock()
	conn, jet := b.conn, b.jet
	b.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("broker: not connected: %w", env.ErrUnavailable)
	}

	if !conn.IsConnected() {
		return nil, fmt.Errorf("%w (%s): %w", ErrNotConnected, conn.Status(), env.ErrUnavailable)
	}

	return jet, nil
}

// Close drains and closes the connection.
func (b *Broker) Close(ctx context.Context) error {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()

	if conn == nil {
		return nil
	}

	if err := conn.Drain(); err != nil {
		// Not connected (or already closed): nothing to drain.
		conn.Close()

		return nil //nolint:nilerr // closed either way
	}

	select {
	case <-b.closed:
		return nil
	case <-ctx.Done():
		conn.Close()

		return fmt.Errorf("broker: drain: %w", ctx.Err())
	}
}

// Publish implements env.Broker: m.Event is the full name
// "<service>.<Event>" of one of the service's events, m.Payload its JSON.
// It returns once JetStream acknowledged the event, and fails fast while
// NATS is unreachable.
func (b *Broker) Publish(ctx context.Context, m env.Message) error {
	jet, err := b.connected()
	if err != nil {
		return err
	}

	service, name, err := SplitEvent(m.Event)
	if err != nil {
		return err
	}

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, b.t.publishTimeout)
		defer cancel()
	}

	subject := Subject(service, name)
	attrs := xtrace.WithAttrs(
		attribute.String("messaging.system", "nats"),
		attribute.String("messaging.destination.name", subject),
	)

	event := cloudEvent{
		ID: m.ID, Service: service, Instance: b.p.Instance, Version: b.p.Version,
		Type: m.Event, Key: m.Key, Time: m.Time, Extensions: m.Extensions,
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}

	if event.Time.IsZero() {
		event.Time = time.Now()
	}

	send := func(ctx context.Context, _ trace.Span) error {
		msg := &nats.Msg{Subject: subject, Data: m.Payload, Header: headers(ctx, event)}

		return b.publish(ctx, jet, msg, service == b.p.Service)
	}

	//nolint:wrapcheck // publish wraps
	return xtrace.Run(ctx, otel.Tracer(instrumentation), "publish "+m.Event, send,
		xtrace.WithSpanOptions(trace.WithSpanKind(trace.SpanKindProducer)), attrs)
}

// publish sends msg and waits for its PubAck. When no stream answers and
// the stream is the service's own, it ensures the stream and tries again.
func (b *Broker) publish(ctx context.Context, jet jetstream.JetStream, msg *nats.Msg, own bool) error {
	_, err := jet.PublishMsg(ctx, msg)
	if errors.Is(err, jetstream.ErrNoStreamResponse) && own {
		if err := b.ensureOwn(ctx); err != nil {
			return err
		}

		_, err = jet.PublishMsg(ctx, msg)
	}

	if err != nil {
		return fmt.Errorf("broker: publish %s: %w", msg.Subject, err)
	}

	return nil
}
