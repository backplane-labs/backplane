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
//	rt := deps.NewDependency(root, nats.New(cfg.Realtime), deps.Name("realtime"))
//	js, err := jetstream.New(rt.Get())
package nats

import (
	"context"
	"errors"
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

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
)

// Defaults of Options.
const (
	DefaultReconnectWait   = 2 * time.Second
	DefaultReconnectJitter = time.Second
	// connectPoll is how often Provide checks for the first connection.
	connectPoll = 50 * time.Millisecond
)

// Config is the connection's section.
type Config struct {
	// URL is one server or a comma-separated list.
	URL config.Secret `json:"url,omitempty"`
	// Creds is the content of a .creds file (user JWT and seed).
	Creds config.Secret `json:"creds,omitempty"`
	TLS   config.TLS    `json:"tls"`
}

// Enabled reports whether a URL is set.
func (c Config) Enabled() bool { return c.URL != "" }

// Validate checks what the schema cannot: the URL is set, the credentials
// and the TLS material parse.
func (c Config) Validate() error {
	if !c.Enabled() {
		return ErrNoURL
	}

	_, err := c.auth()

	return err
}

// auth is the credentials and TLS options.
func (c Config) auth() ([]natsgo.Option, error) {
	var opts []natsgo.Option

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
	// Pause between reconnection attempts; zero is the default.
	ReconnectWait, ReconnectJitter time.Duration
}

// Connect connects without blocking on an unreachable server: the
// connection keeps trying in the background, for ever. While disconnected
// a publish fails instead of buffering.
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

	wait, jitter := o.ReconnectWait, o.ReconnectJitter
	if wait <= 0 {
		wait = DefaultReconnectWait
	}

	if jitter <= 0 {
		jitter = DefaultReconnectJitter
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
		natsgo.ReconnectWait(wait),
		natsgo.ReconnectJitter(jitter, jitter),
		natsgo.ReconnectBufSize(-1),
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

// New is the provider of the connection: Provide connects and waits for
// the first connection, the probe reports the status, Close drains
// nothing and closes.
func New(cfg Config) deps.Provider[*natsgo.Conn] { return provider{cfg: cfg} }

type provider struct{ cfg Config }

func (provider) Name() string { return "nats" }

func (p provider) Provide(ctx context.Context, s deps.Scope) (*natsgo.Conn, error) {
	conn, err := Connect(p.cfg, Options{Name: s.Path(), Log: s.Log()})
	if err != nil {
		return nil, err
	}

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

func (provider) Probe(_ context.Context, conn *natsgo.Conn) error {
	if !conn.IsConnected() {
		return ErrDisconnected
	}

	return nil
}

func (provider) Close(_ context.Context, conn *natsgo.Conn) error {
	conn.Close()

	return nil
}
