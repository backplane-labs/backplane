// Package valkey is a Valkey (Redis protocol) connection as a dependency:
// a configuration section, a client instrumented with the service's
// OpenTelemetry providers, a PING probe and a close.
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    Cache valkey.Config `json:"cache"` // GREETER_CACHE_ADDR, ...
//	}
//
//	cache := deps.NewDependency(root, valkey.New(cfg.Cache), deps.Name("cache"))
//	err := cache.Get().Do(ctx, cache.Get().B().Set().Key("k").Value("v").Build()).Error()
//
// The address is a seed: a cluster is discovered from it.
package valkey

import (
	"context"
	"errors"
	"fmt"

	valkeygo "github.com/valkey-io/valkey-go"
	"github.com/valkey-io/valkey-go/valkeyotel"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Client is valkey-go's client: commands are built with B() and sent with
// Do, DoMulti or DoCache.
type Client = valkeygo.Client

// ErrNoAddr: the section has no address.
var ErrNoAddr = errors.New("valkey: addr is required")

// Config is the connection's section.
type Config struct {
	// Addr is host:port of a node.
	Addr     string        `json:"addr,omitempty"`
	Username string        `json:"username,omitempty"`
	Password config.Secret `json:"password,omitempty"`
	// DB is the logical database (standalone only). How many there are is
	// the server's databases setting (16 by default); a number it does not
	// have fails the connection.
	DB  int64      `json:"db"  schemapb:"default=0;gte=0"`
	TLS config.TLS `json:"tls"`
}

// Enabled reports whether an address is set.
func (c Config) Enabled() bool { return c.Addr != "" }

// Validate checks what the schema cannot: the address is set and the TLS
// material parses.
func (c Config) Validate() error {
	if !c.Enabled() {
		return ErrNoAddr
	}

	if _, err := c.TLS.ClientConfig(); err != nil {
		return fmt.Errorf("valkey: %w", err)
	}

	return nil
}

// New is the provider of the client: Provide connects and pings, the probe
// pings, Close closes.
func New(cfg Config) deps.Provider[Client] { return provider{cfg: cfg} }

type provider struct{ cfg Config }

func (provider) Name() string { return "valkey" }

//nolint:ireturn // valkey-go's client is an interface
func (p provider) Provide(ctx context.Context, s deps.Scope) (Client, error) {
	if err := p.cfg.Validate(); err != nil {
		return nil, err
	}

	tlsConfig, _ := p.cfg.TLS.ClientConfig() // Validate parsed it

	client, err := valkeyotel.NewClient(valkeygo.ClientOption{
		InitAddress: []string{p.cfg.Addr},
		Username:    p.cfg.Username,
		Password:    p.cfg.Password.Reveal(),
		SelectDB:    int(p.cfg.DB),
		TLSConfig:   tlsConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("valkey: connect %s: %w", p.cfg.Addr, err)
	}

	if err := ping(ctx, client); err != nil {
		client.Close()

		return nil, err
	}

	s.Log().Info("valkey ready", xlog.String("addr", p.cfg.Addr))

	return client, nil
}

func (provider) Probe(ctx context.Context, c Client) error { return ping(ctx, c) }

func (provider) Close(_ context.Context, c Client) error {
	c.Close()

	return nil
}

func ping(ctx context.Context, c Client) error {
	if err := c.Do(ctx, c.B().Ping().Build()).Error(); err != nil {
		return fmt.Errorf("valkey: ping: %w", err)
	}

	return nil
}
