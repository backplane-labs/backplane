package valkey

import (
	"context"
	"time"

	"github.com/valkey-io/valkey-go/valkeyaside"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// AsideClient is valkeyaside's cache-aside client over client-side caching:
// Get(ctx, ttl, key, fn) reads a key, filling it once with fn across every
// client on a miss, and keeps it in this process until the server
// invalidates it. Client() is the instrumented connection underneath.
type AsideClient = valkeyaside.CacheAsideClient

// AsideOption tunes NewAside.
type AsideOption func(*valkeyaside.ClientOption)

// AsideClientTTL is how long this client's liveness marker lives (refreshed
// every half): other clients wait no longer for a fill it abandoned.
// Default 10s.
func AsideClientTTL(d time.Duration) AsideOption {
	return func(o *valkeyaside.ClientOption) { o.ClientTTL = d }
}

// AsideLuaLock takes fill locks with Lua scripts (valkeyaside's UseLuaLock).
func AsideLuaLock() AsideOption { return func(o *valkeyaside.ClientOption) { o.UseLuaLock = true } }

// NewAside is the provider of a cache-aside client on the same section as
// New: TLS, auth and database alike, the connection instrumented with the
// service's OpenTelemetry providers; Provide connects and pings, the probe
// pings, Close closes.
//
//	owners := deps.NewDependency(root, valkey.NewAside(cfg.Cache), deps.Name("owners"))
//	owner, err := owners.Get().Get(ctx, time.Minute, "owner:"+id, load)
func NewAside(cfg Config, opts ...AsideOption) deps.Provider[AsideClient] {
	return asideProvider{cfg: cfg, opts: opts}
}

type asideProvider struct {
	cfg  Config
	opts []AsideOption
}

func (asideProvider) Name() string { return "valkey-aside" }

//nolint:ireturn // valkeyaside's client is an interface
func (p asideProvider) Provide(ctx context.Context, s deps.Scope) (AsideClient, error) {
	if err := p.cfg.Validate(); err != nil {
		return nil, err
	}

	option := valkeyaside.ClientOption{
		ClientOption:  p.cfg.clientOption(),
		ClientBuilder: newClient,
	}
	for _, opt := range p.opts {
		opt(&option)
	}

	client, err := valkeyaside.NewClient(option)
	if err != nil {
		return nil, p.cfg.connectError(err)
	}

	if err := ping(ctx, client.Client()); err != nil {
		client.Close()

		return nil, err
	}

	s.Log().Info("valkey cache-aside ready", xlog.String("addr", p.cfg.Addr))

	return client, nil
}

func (asideProvider) Probe(ctx context.Context, c AsideClient) error { return ping(ctx, c.Client()) }

func (asideProvider) Close(_ context.Context, c AsideClient) error {
	c.Close()

	return nil
}
