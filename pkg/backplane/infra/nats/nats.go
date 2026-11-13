// Package nats is a NATS connection as a dependency: a configuration
// section, a connection that never gives up (reconnecting with jitter, no
// buffering while disconnected, transitions logged), a status probe. The
// SDK's own event bus connects through Connect too.
//
// The platform's events travel on the bus of the SDK block
// (BACKPLANE_NATS_*); this is for a NATS of the service's own — one
// backplane does not see:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    Realtime nats.Config `json:"realtime"` // CALLS_REALTIME_URL, ...
//	}
//
//	rt := deps.NewDependency(root, nats.New(cfg.Realtime, nats.WithJetStream()), deps.Name("realtime"))
//	js, err := jetstream.New(rt.Get())
//
// WithJetStream makes JetStream part of the dependency's readiness: a
// server can hold the connection while JetStream is unusable (no meta
// leader, storage full, its cluster lost).
package nats

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Errors.
var (
	// ErrNoURL: the section has no URL.
	ErrNoURL = errors.New("nats: url is required")
	// ErrDisconnected is what the probe reports while the connection is
	// down.
	ErrDisconnected = errors.New("nats: not connected")
	// ErrJetStream is what the probe reports, with WithJetStream, while the
	// connection is up but JetStream does not answer.
	ErrJetStream = errors.New("nats: jetstream unavailable")
)

// connectPoll is how often Provide checks for the first connection.
const connectPoll = 50 * time.Millisecond

// Config is the connection's section.
type Config struct {
	// URL is one server or a comma-separated list.
	URL config.Secret `desc:"Server URL, or comma-separated URLs." json:"url,omitempty"`
	// Creds is the content of a .creds file (user JWT and seed).
	Creds config.Secret `desc:"Content of a .creds file (user JWT and seed)." json:"creds,omitempty"`
	TLS   config.TLS    `desc:"TLS of the connection."                        json:"tls"`
	// InboxPrefix replaces _INBOX as the prefix of the connection's reply
	// subjects, its requests' and its JetStream API calls': a deployment
	// that grants each identity its own prefix keeps other identities from
	// reading its replies.
	InboxPrefix string `desc:"Prefix of reply subjects instead of _INBOX, e.g. _INBOX_billing; no wildcards, no trailing dot." json:"inbox_prefix,omitempty"`
	// How the connection behaves: timeouts, pings, reconnection, buffer.
	config.NATSConnection
}

// JetStream is a JetStream handle on conn whose API calls without a
// deadline take jetstream_timeout; opts come after (a domain, a prefix).
func (c Config) JetStream(conn *natsgo.Conn, opts ...jetstream.JetStreamOpt) (jetstream.JetStream, error) {
	all := append([]jetstream.JetStreamOpt{jetstream.WithDefaultTimeout(c.Resolved().JetStreamTimeout)}, opts...)

	js, err := jetstream.New(conn, all...)
	if err != nil {
		return nil, fmt.Errorf("nats: jetstream: %w", err)
	}

	return js, nil
}

// Enabled reports whether a URL is set.
func (c Config) Enabled() bool { return c.URL != "" }

// Validate checks what the schema cannot: the URL is set, the credentials
// and the TLS material parse, the inbox prefix is a subject prefix.
func (c Config) Validate() error {
	if !c.Enabled() {
		return ErrNoURL
	}

	_, err := c.auth()

	return err
}

// auth is the credentials, TLS and inbox prefix options.
func (c Config) auth() ([]natsgo.Option, error) {
	var opts []natsgo.Option

	if c.InboxPrefix != "" {
		if err := config.CheckInboxPrefix(c.InboxPrefix); err != nil {
			return nil, fmt.Errorf("nats: %w", err)
		}

		opts = append(opts, natsgo.CustomInboxPrefix(c.InboxPrefix))
	}

	if c.Creds.Reveal() != "" {
		creds, err := credentials(c.Creds.Reveal())
		if err != nil {
			return nil, err
		}

		opts = append(opts, creds)
	}

	tc, err := c.TLS.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("nats: %w", err)
	}

	if tc != nil {
		opts = append(opts, natsgo.Secure(tc))
	}

	return opts, nil
}

// credentials turns the content of a .creds file into the user JWT and
// seed option.
func credentials(content string) (natsgo.Option, error) {
	jwt, err := nkeys.ParseDecoratedJWT([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("nats: creds: jwt: %w", err)
	}

	kp, err := nkeys.ParseDecoratedUserNKey([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("nats: creds: seed: %w", err)
	}

	seed, err := kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("nats: creds: seed: %w", err)
	}

	return natsgo.UserJWTAndSeed(jwt, string(seed)), nil
}

// Options of Connect beyond the section.
type Options struct {
	// Name of the client as the server lists it.
	Name string
	Log  *xlog.Logger
	// OnStatus is told every change of connectivity.
	OnStatus func(connected bool)
	// OnClosed runs once the connection is closed for good.
	OnClosed func()
}

// Connect connects without blocking on an unreachable server: the
// connection keeps trying in the background, for ever. While disconnected
// a publish buffers at most reconnect_buffer bytes (0: it fails).
func Connect(cfg Config, o Options) (*natsgo.Conn, error) {
	if !cfg.Enabled() {
		return nil, ErrNoURL
	}

	opts, err := cfg.auth()
	if err != nil {
		return nil, err
	}

	log := o.Log
	if log == nil {
		log = xlog.New(xlog.NopCore{})
	}

	c := cfg.Resolved()

	buffer := -1 // nats.go: -1 buffers nothing
	if c.ReconnectBuffer > 0 {
		buffer = int(min(c.ReconnectBuffer, 1<<31-1))
	}

	status := func(connected bool) {
		if o.OnStatus != nil {
			o.OnStatus(connected)
		}
	}

	opts = append(opts,
		natsgo.Name(o.Name),
		natsgo.RetryOnFailedConnect(true),
		natsgo.MaxReconnects(-1),
		// The same authorization error twice (accounts not loaded yet after
		// a server restart) would close the connection for good.
		natsgo.IgnoreAuthErrorAbort(),
		natsgo.Timeout(c.ConnectTimeout),
		natsgo.ReconnectWait(c.ReconnectWait),
		natsgo.ReconnectJitter(c.ReconnectJitter, c.ReconnectJitter),
		natsgo.PingInterval(c.PingInterval),
		natsgo.MaxPingsOutstanding(int(min(c.MaxPingsOut, 1<<31-1))),
		natsgo.ReconnectBufSize(buffer),
		natsgo.ConnectHandler(func(conn *natsgo.Conn) {
			status(true)
			log.Info("nats connected", xlog.String("url", conn.ConnectedUrlRedacted()))
		}),
		natsgo.ReconnectHandler(func(conn *natsgo.Conn) {
			status(true)
			log.Info("nats reconnected", xlog.String("url", conn.ConnectedUrlRedacted()))
		}),
		natsgo.DisconnectErrHandler(func(_ *natsgo.Conn, err error) {
			status(false)

			if err != nil {
				log.Warn("nats disconnected", xlog.Err(err))
			}
		}),
		natsgo.ErrorHandler(func(_ *natsgo.Conn, sub *natsgo.Subscription, err error) {
			subject := ""
			if sub != nil {
				subject = sub.Subject
			}

			log.Warn("nats error", xlog.Err(err), xlog.String("subject", subject))
		}),
		natsgo.ClosedHandler(func(*natsgo.Conn) {
			status(false)

			if o.OnClosed != nil {
				o.OnClosed()
			}
		}),
	)

	conn, err := natsgo.Connect(cfg.URL.Reveal(), opts...)
	if err != nil {
		return nil, fmt.Errorf("nats: connect: %w", err)
	}

	return conn, nil
}

// Option configures New.
type Option func(*provider)

// WithJetStream makes the probe ask JetStream for the account's info
// (within the probe's deadline) once the connection is up: readiness
// fails while JetStream does not answer, and recovers with it; it never
// fails liveness. opts are the JetStream options the service uses (a
// domain, an API prefix).
func WithJetStream(opts ...jetstream.JetStreamOpt) Option {
	return func(p *provider) { p.jetstream, p.jetOpts = true, opts }
}

// New is the provider of the connection: Provide connects and waits for
// the first connection, the probe reports the status, Close drains
// nothing and closes.
func New(cfg Config, opts ...Option) deps.Provider[*natsgo.Conn] {
	p := provider{cfg: cfg}
	for _, o := range opts {
		o(&p)
	}

	return p
}

type provider struct {
	cfg       Config
	jetstream bool
	jetOpts   []jetstream.JetStreamOpt
}

func (provider) Name() string { return "nats" }

func (p provider) Provide(ctx context.Context, s deps.Scope) (*natsgo.Conn, error) {
	var stats connStats

	conn, err := Connect(p.cfg, Options{Name: s.Path(), Log: s.Log(), OnClosed: stats.stop})
	if err != nil {
		return nil, err
	}

	stats.start(conn, s)

	tick := time.NewTicker(connectPoll)
	defer tick.Stop()

	for !conn.IsConnected() {
		select {
		case <-ctx.Done():
			conn.Close()

			return nil, fmt.Errorf("nats: connection interrupted: %w", ctx.Err())
		case <-tick.C:
		}
	}

	return conn, nil
}

// Probe fails readiness while the connection reconnects (or, with
// WithJetStream, while JetStream does not answer) and liveness once it is
// closed: Connect never gives up, so only a closed connection is beyond
// recovery.
func (p provider) Probe(ctx context.Context, conn *natsgo.Conn) error {
	switch {
	case conn.IsClosed():
		return fmt.Errorf("%w: %w", natsgo.ErrConnectionClosed, deps.ErrTerminal)
	case !conn.IsConnected():
		return ErrDisconnected
	case p.jetstream:
		bounded := append([]jetstream.JetStreamOpt{
			jetstream.WithDefaultTimeout(p.cfg.Resolved().JetStreamTimeout),
		}, p.jetOpts...)

		return JetStreamReady(ctx, conn, bounded...)
	}

	return nil
}

// JetStreamReady is nil when JetStream answers an account info request
// on conn within ctx, ErrJetStream otherwise.
func JetStreamReady(ctx context.Context, conn *natsgo.Conn, opts ...jetstream.JetStreamOpt) error {
	js, err := jetstream.New(conn, opts...)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrJetStream, err)
	}

	if _, err := js.AccountInfo(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrJetStream, err)
	}

	return nil
}

func (provider) Close(_ context.Context, conn *natsgo.Conn) error {
	conn.Close()

	return nil
}

// meterName is the instrumentation scope of the connection metrics.
const meterName = "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"

// connStats reports a connection's state and reconnections, attribute conn
// (the dependency's name), until it closes.
type connStats struct {
	mu  sync.Mutex
	reg metric.Registration
}

func (c *connStats) start(conn *natsgo.Conn, s deps.Scope) {
	meter := otel.Meter(meterName)
	attrs := metric.WithAttributes(attribute.String("conn", s.Name()))

	connected, err1 := meter.Int64ObservableGauge("backplane.nats.connected",
		metric.WithDescription("1 while the NATS connection is up"))
	reconnects, err2 := meter.Int64ObservableCounter("backplane.nats.reconnects",
		metric.WithDescription("Reconnections of the NATS connection"))

	if err := errors.Join(err1, err2); err != nil {
		s.Log().Warn("nats: connection metrics unavailable", xlog.Err(err))

		return
	}

	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		up := int64(0)
		if conn.IsConnected() {
			up = 1
		}

		o.ObserveInt64(connected, up, attrs)
		o.ObserveInt64(reconnects, int64(min(conn.Stats().Reconnects, 1<<63-1)), attrs)

		return nil
	}, connected, reconnects)
	if err != nil {
		s.Log().Warn("nats: connection metrics unavailable", xlog.Err(err))

		return
	}

	c.mu.Lock()
	c.reg = reg
	c.mu.Unlock()

	// Closed before the registration landed: stop ran with nothing to undo.
	if conn.IsClosed() {
		c.stop()
	}
}

// stop unregisters the metrics once the connection is closed.
func (c *connStats) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.reg != nil {
		_ = c.reg.Unregister() // the meter outlives no closed connection
		c.reg = nil
	}
}
