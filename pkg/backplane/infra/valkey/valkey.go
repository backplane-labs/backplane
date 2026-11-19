// Package valkey is a Valkey (Redis protocol) connection as a dependency:
// a configuration section, a client instrumented with the service's
// OpenTelemetry providers (command spans only under an operation), a PING
// probe and a close.
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
	"net"
	"strings"
	"time"

	valkeygo "github.com/valkey-io/valkey-go"
	"github.com/valkey-io/valkey-go/valkeyotel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xtrace/pkg/scope"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Client is valkey-go's client: commands are built with B() and sent with
// Do, DoMulti or DoCache.
type Client = valkeygo.Client

// ErrNoAddr: the section has no address.
var ErrNoAddr = errors.New("valkey: addr is required")

// Defaults of the section: explicit, never derived from the node. A zero
// field of a Config built by hand reads its default too.
const (
	DefaultDialTimeout      = 5 * time.Second
	DefaultConnWriteTimeout = 10 * time.Second
	DefaultBlockingPoolSize = 1024
	DefaultBlockingPipeline = 2000
	DefaultBufferEachConn   = 1 << 19
)

// multiplex is pipeline_multiplex as valkey-go takes it: 2^n connections,
// where valkey-go reads 0 as "derive from the CPUs" and a negative n as
// one connection.
func multiplex(n int64) int {
	if n == 0 {
		return -1
	}

	return int(n)
}

// orDefault is v, or def when v is zero (a Config built by hand).
func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}

	return v
}

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
	// Sentinel finds the primary of a master set through sentinels and
	// follows it across a failover; instead of addr.
	Sentinel Sentinel `desc:"Sentinels of a master set, instead of addr." json:"sentinel"`
	// DialTimeout bounds establishing one connection.
	DialTimeout time.Duration `desc:"Bound of establishing one connection." json:"dial_timeout" schemapb:"default=5s;gt=0"`
	// ConnWriteTimeout is the per-connection deadline of writing a command
	// and waiting for its pipelined reply (valkey-go has no separate read
	// timeout).
	ConnWriteTimeout time.Duration `desc:"Deadline of a command's write and its pipelined reply." json:"conn_write_timeout" schemapb:"default=10s;gt=0"`
	ConnLifetime     time.Duration `desc:"A connection this old is replaced; 0: for ever."        json:"conn_lifetime"      schemapb:"default=0s;gte=0"`
	// Blocking pool: dedicated connections of blocking commands (BLPOP,
	// XREAD BLOCK, ...) and of transactions.
	BlockingPoolSize    int64         `desc:"Most dedicated connections (blocking commands, transactions)." json:"blocking_pool_size"     schemapb:"default=1024;gt=0"`
	BlockingPoolMinSize int64         `desc:"Dedicated connections kept open."                              json:"blocking_pool_min_size" schemapb:"default=0;gte=0"`
	BlockingPoolCleanup time.Duration `desc:"How often idle dedicated connections are closed; 0: never."    json:"blocking_pool_cleanup"  schemapb:"default=0s;gte=0"`
	// Pipelining of the shared connections.
	PipelineMultiplex     int64         `desc:"2^n shared connections per node pipeline the commands (0: one)."                json:"pipeline_multiplex"      schemapb:"default=2;gte=0;lte=8"`
	BlockingPipeline      int64         `desc:"Commands pipelined at once above which a command takes a dedicated connection." json:"blocking_pipeline"       schemapb:"default=2000;gt=0"`
	MaxFlushDelay         time.Duration `desc:"Wait to batch more commands into one write; 0: write at once."                  json:"max_flush_delay"         schemapb:"default=0s;gte=0"`
	DisableAutoPipelining bool          `desc:"Commands take a dedicated connection unless pipelined explicitly."              json:"disable_auto_pipelining" schemapb:"default=false"`
	ReadBufferEachConn    int64         `desc:"Read buffer of each connection, bytes."                                         json:"read_buffer_each_conn"   schemapb:"default=524288;gt=0"`
	WriteBufferEachConn   int64         `desc:"Write buffer of each connection, bytes."                                        json:"write_buffer_each_conn"  schemapb:"default=524288;gt=0"`
}

// Sentinel is the master set a connection follows through its sentinels.
// Username, Password and TLS of the section are the data nodes'; these are
// the sentinels' own.
type Sentinel struct {
	// MasterSet is the name the sentinels monitor the primary under; Addrs
	// the sentinels, host:port comma-separated.
	MasterSet string        `desc:"Name the sentinels monitor the primary under." json:"master_set,omitempty"`
	Addrs     string        `desc:"Sentinels, comma-separated host:port."         json:"addrs,omitempty"`
	Username  string        `desc:"ACL user of the sentinels."                    json:"username,omitempty"`
	Password  config.Secret `desc:"Password of the sentinels."                    json:"password,omitempty"`
	TLS       config.TLS    `desc:"TLS of the connections to the sentinels."      json:"tls"`
}

// Enabled reports whether the sentinels are set.
func (s Sentinel) Enabled() bool { return len(split(s.Addrs)) > 0 }

// Errors of the sentinel section.
var (
	// ErrSentinelAndAddr: both addr and sentinel addrs; one or the other.
	ErrSentinelAndAddr = errors.New("valkey: addr and sentinel.addrs are exclusive")
	// ErrNoMasterSet: sentinels without the master set they monitor.
	ErrNoMasterSet = errors.New("valkey: sentinel.master_set is required with sentinel.addrs")
)

// Enabled reports whether an address or sentinels are set.
func (c Config) Enabled() bool { return len(c.addrs()) > 0 || c.Sentinel.Enabled() }

// addrs are the addresses of Addr.
func (c Config) addrs() []string { return split(c.Addr) }

// split is a comma-separated host:port list.
func split(list string) []string {
	var out []string

	for a := range strings.SplitSeq(list, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}

	return out
}

// Validate checks what the schema cannot: the address is set and the TLS
// material parses.
func (c Config) Validate() error {
	switch {
	case !c.Enabled():
		return ErrNoAddr
	case len(c.addrs()) > 0 && c.Sentinel.Enabled():
		return ErrSentinelAndAddr
	case c.Sentinel.Enabled() && c.Sentinel.MasterSet == "":
		return ErrNoMasterSet
	}

	if _, err := c.TLS.ClientConfig(); err != nil {
		return fmt.Errorf("valkey: %w", err)
	}

	if _, err := c.Sentinel.TLS.ClientConfig(); err != nil {
		return fmt.Errorf("valkey: sentinel: %w", err)
	}

	return nil
}

// New is the provider of the client: Provide connects and pings, the probe
// pings, Close closes.
func New(cfg Config) deps.Provider[Client] { return provider{cfg: cfg} }

type provider struct{ cfg Config }

func (provider) Name() string { return "valkey" }

// clientBuilder makes instrumented clients whose commands are spans of the
// operations issuing them: commands from a context without a span (the
// service's background loops, pings) start no traces of their own. Command
// and dial metrics carry the attribute pool, the dependency's name.
func clientBuilder(pool string) func(valkeygo.ClientOption) (valkeygo.Client, error) {
	return func(o valkeygo.ClientOption) (valkeygo.Client, error) {
		return valkeyotel.NewClient(o, valkeyotel.WithTracerProvider(scope.ChildOnly(otel.GetTracerProvider())),
			valkeyotel.MetricAttrs(attribute.String("pool", pool)))
	}
}

//nolint:ireturn // valkey-go's client is an interface
func (p provider) Provide(ctx context.Context, s deps.Scope) (Client, error) {
	if err := p.cfg.Validate(); err != nil {
		return nil, err
	}

	client, err := clientBuilder(s.Name())(p.cfg.clientOption())
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

	opt := valkeygo.ClientOption{
		InitAddress: c.addrs(),
		Username:    c.Username,
		Password:    c.Password.Reveal(),
		SelectDB:    int(c.DB),
		TLSConfig:   tlsConfig,

		Dialer: net.Dialer{
			Timeout: orDefault(c.DialTimeout, DefaultDialTimeout), KeepAlive: valkeygo.DefaultTCPKeepAlive,
		},
		ConnWriteTimeout:      orDefault(c.ConnWriteTimeout, DefaultConnWriteTimeout),
		ConnLifetime:          c.ConnLifetime,
		BlockingPoolSize:      int(orDefault(c.BlockingPoolSize, DefaultBlockingPoolSize)),
		BlockingPoolMinSize:   int(c.BlockingPoolMinSize),
		BlockingPoolCleanup:   c.BlockingPoolCleanup,
		PipelineMultiplex:     multiplex(c.PipelineMultiplex),
		BlockingPipeline:      int(orDefault(c.BlockingPipeline, DefaultBlockingPipeline)),
		MaxFlushDelay:         c.MaxFlushDelay,
		DisableAutoPipelining: c.DisableAutoPipelining,
		ReadBufferEachConn:    int(orDefault(c.ReadBufferEachConn, DefaultBufferEachConn)),
		WriteBufferEachConn:   int(orDefault(c.WriteBufferEachConn, DefaultBufferEachConn)),
	}

	if c.Sentinel.Enabled() {
		sentinelTLS, _ := c.Sentinel.TLS.ClientConfig() // Validate parsed it

		opt.InitAddress = split(c.Sentinel.Addrs)
		opt.Sentinel = valkeygo.SentinelOption{
			MasterSet: c.Sentinel.MasterSet, Username: c.Sentinel.Username,
			Password: c.Sentinel.Password.Reveal(), TLSConfig: sentinelTLS,
		}
	}

	return opt
}

// ErrClusterDB: a database other than 0 on a cluster without numbered
// databases.
var ErrClusterDB = errors.New("valkey: a cluster has only database 0 unless it is Valkey 9+ with cluster-databases")

// connectError says why the connection failed.
func (c Config) connectError(err error) error {
	if c.DB != 0 && strings.Contains(err.Error(), "SELECT is not allowed in cluster mode") {
		return fmt.Errorf("%w: db %d: %w", ErrClusterDB, c.DB, err)
	}

	if c.Sentinel.Enabled() {
		return fmt.Errorf("valkey: connect %s via sentinels %s: %w", c.Sentinel.MasterSet, c.Sentinel.Addrs, err)
	}

	return fmt.Errorf("valkey: connect %s: %w", c.Addr, err)
}

func ping(ctx context.Context, c Client) error {
	if err := c.Do(ctx, c.B().Ping().Build()).Error(); err != nil {
		return fmt.Errorf("valkey: ping: %w", err)
	}

	return nil
}
