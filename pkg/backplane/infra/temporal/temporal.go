// Package temporal is a Temporal client as a dependency: a configuration
// section, a client traced and measured through the service's OpenTelemetry
// providers and logging through its logger, a health probe. The SDK's own
// connection (BACKPLANE_TEMPORAL_*: hooks, activities, workflows) dials
// through Options too.
//
// For a Temporal of the service's own — another cluster or namespace:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    Billing temporal.Config `json:"billing"` // SHOP_BILLING_ADDR, _NS, ...
//	}
//
//	billing := deps.NewDependency(root, temporal.New(cfg.Billing), deps.Name("billing"))
//	run, err := billing.Get().ExecuteWorkflow(ctx, opts, "Charge", in)
package temporal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.temporal.io/sdk/client"
	otelsdk "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	tlog "go.temporal.io/sdk/log"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Client is the Temporal SDK's client.
type Client = client.Client

// ErrNoAddr: the section has no address.
var ErrNoAddr = errors.New("temporal: addr is required")

// Defaults of Config.
const (
	DefaultNamespace   = "default"
	DefaultDialTimeout = 2 * time.Second
	scopeName          = "github.com/backplane-labs/backplane/temporal"
)

// Config is the connection's section. Field `ns`: `namespace` is a CEL
// reserved word.
type Config struct {
	// Addr is the frontend's host:port.
	Addr      string     `json:"addr,omitempty"`
	Namespace string     `json:"ns"             schemapb:"default=default"`
	TLS       config.TLS `json:"tls"`
	// APIKey authenticates to Temporal Cloud; it turns TLS on even when TLS
	// is not enabled.
	APIKey config.Secret `json:"api_key,omitempty"`
	// DialTimeout bounds one connection attempt.
	DialTimeout time.Duration `json:"dial_timeout" schemapb:"default=2s"`
}

// Enabled reports whether an address is set.
func (c Config) Enabled() bool { return c.Addr != "" }

// WithDefaults fills a zero namespace and dial timeout.
func (c Config) WithDefaults() Config {
	if c.Namespace == "" {
		c.Namespace = DefaultNamespace
	}

	if c.DialTimeout <= 0 {
		c.DialTimeout = DefaultDialTimeout
	}

	return c
}

// Validate checks what the schema cannot: the address is set and the TLS
// material parses.
func (c Config) Validate() error {
	if !c.Enabled() {
		return ErrNoAddr
	}

	if _, err := c.TLS.ClientConfig(); err != nil {
		return fmt.Errorf("temporal: %w", err)
	}

	return nil
}

// Options are the client options of cfg: address, namespace, TLS, API key,
// identity, the logger, OpenTelemetry tracing and metrics.
func Options(cfg Config, identity string, log *xlog.Logger) (client.Options, error) {
	cfg = cfg.WithDefaults()

	if log == nil {
		log = xlog.New(xlog.NopCore{})
	}

	tlsCfg, err := cfg.TLS.ClientConfig()
	if err != nil {
		return client.Options{}, fmt.Errorf("temporal: %w", err)
	}

	tracer, err := otelsdk.NewTracingInterceptor(otelsdk.TracerOptions{})
	if err != nil {
		return client.Options{}, fmt.Errorf("temporal: tracing: %w", err)
	}

	opts := client.Options{
		HostPort:     cfg.Addr,
		Namespace:    cfg.Namespace,
		Identity:     identity,
		Logger:       tlog.NewStructuredLogger(slog.New(xlog.NewSlogHandler(log))),
		Interceptors: []interceptor.ClientInterceptor{tracer},
		MetricsHandler: otelsdk.NewMetricsHandler(otelsdk.MetricsHandlerOptions{
			Meter:                otel.GetMeterProvider().Meter(scopeName),
			OnError:              func(err error) { log.Debug("temporal metric", xlog.Err(err)) },
			UseMonotonicCounters: true,
		}),
		ConnectionOptions: client.ConnectionOptions{TLS: tlsCfg},
	}

	if key := cfg.APIKey.Reveal(); key != "" {
		opts.Credentials = client.NewAPIKeyStaticCredentials(key)
	}

	return opts, nil
}

// Dial makes one connection attempt bounded by the dial timeout.
//
//nolint:ireturn // the Temporal SDK's client is an interface
func Dial(ctx context.Context, cfg Config, opts client.Options) (Client, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.WithDefaults().DialTimeout)
	defer cancel()

	c, err := client.DialContext(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("temporal: dial %s: %w", cfg.Addr, err)
	}

	return c, nil
}

// New is the provider of the client: Provide dials, the probe checks the
// frontend's health, Close closes.
func New(cfg Config) deps.Provider[Client] { return provider{cfg: cfg} }

type provider struct{ cfg Config }

func (provider) Name() string { return "temporal" }

//nolint:ireturn // the Temporal SDK's client is an interface
func (p provider) Provide(ctx context.Context, s deps.Scope) (Client, error) {
	if err := p.cfg.Validate(); err != nil {
		return nil, err
	}

	opts, err := Options(p.cfg, s.Path(), s.Log())
	if err != nil {
		return nil, err
	}

	c, err := Dial(ctx, p.cfg, opts)
	if err != nil {
		return nil, err
	}

	s.Log().Info("temporal ready", xlog.String("addr", p.cfg.Addr), xlog.String("namespace", opts.Namespace))

	return c, nil
}

func (provider) Probe(ctx context.Context, c Client) error {
	if _, err := c.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		return fmt.Errorf("temporal: health: %w", err)
	}

	return nil
}

func (provider) Close(_ context.Context, c Client) error {
	c.Close()

	return nil
}
