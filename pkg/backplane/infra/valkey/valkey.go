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
// The address is one node or a comma-separated list of seeds: a cluster is
// discovered from whichever answers, a standalone server is used as is.
//
// NewAside is the same connection as a valkeyaside cache-aside client
// (client-side caching with server invalidation): AsideClient.
package valkey

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	// Addr is host:port of a node, or comma-separated seeds of a cluster
	// (host:port,host:port): any one answering is enough to discover it.
	Addr     string        `desc:"host:port of a node, or comma-separated seeds of a cluster." json:"addr,omitempty"`
	Username string        `desc:"ACL user."                                                   json:"username,omitempty"`
	Password config.Secret `desc:"Password."                                                   json:"password,omitempty"`
	// DB is the logical database. How many there are is the server's to
	// say: databases on a standalone server (16 by default),
	// cluster-databases on a Valkey 9+ cluster (1 by default); a classic
	// cluster has only database 0. A number the server does not have fails
	// the connection.
	DB  int64      `desc:"Logical database; a number the server does not have fails the connection." json:"db"  schemapb:"default=0;gte=0"`
	TLS config.TLS `desc:"TLS of the connection."                                                    json:"tls"`
}

// Enabled reports whether an address is set.
func (c Config) Enabled() bool { return len(c.addrs()) > 0 }

// addrs are the addresses of Addr.
func (c Config) addrs() []string {
	var out []string

	for a := range strings.SplitSeq(c.Addr, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}

	return out
}

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

	client, err := valkeyotel.NewClient(p.cfg.clientOption())
	if err != nil {
		return nil, p.cfg.connectError(err)
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

// clientOption is the connection of a validated section.
func (c Config) clientOption() valkeygo.ClientOption {
	tlsConfig, _ := c.TLS.ClientConfig() // Validate parsed it

	return valkeygo.ClientOption{
		InitAddress: c.addrs(),
		Username:    c.Username,
		Password:    c.Password.Reveal(),
		SelectDB:    int(c.DB),
		TLSConfig:   tlsConfig,
	}
}

// ErrClusterDB: a database other than 0 on a cluster without numbered
// databases.
var ErrClusterDB = errors.New("valkey: a cluster has only database 0 unless it is Valkey 9+ with cluster-databases")

// connectError says why the connection failed.
func (c Config) connectError(err error) error {
	if c.DB != 0 && strings.Contains(err.Error(), "SELECT is not allowed in cluster mode") {
		return fmt.Errorf("%w: db %d: %w", ErrClusterDB, c.DB, err)
	}

	return fmt.Errorf("valkey: connect %s: %w", c.Addr, err)
}

func ping(ctx context.Context, c Client) error {
	if err := c.Do(ctx, c.B().Ping().Build()).Error(); err != nil {
		return fmt.Errorf("valkey: ping: %w", err)
	}

	return nil
}
