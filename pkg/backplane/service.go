// Package backplane is the Go SDK. The author describes the service as one
// Config struct and one State built by an explicit constructor:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    Postgres pgx.Config `json:"postgres"`
//	}
//
//	type State struct {
//	    DB    deps.Dependency[*pgxpool.Pool]
//	    Users *users.Repo
//	}
//
//	func NewState(root backplane.Root[Config]) (*State, error) { ... }
//
//	svc, err := backplane.Open(ctx, NewState)
//	svc.GRPC(svc.State().Users.Register)
//	err = svc.Run(ctx)
//
// Everything — the SDK's own parts and the author's tree — is one tree of
// nodes. Run starts it in order:
//
//	config → telemetry → health → platform → consul → nats → temporal → hooks →
//	author's tree → internal-api → reactors → worker →
//	public:<addr> → drain → register → serving
//
// so components may publish events and call hooks from OnStart to OnStop.
// It stops in exact reverse within Shutdown.Timeout: readiness drops, the
// instance deregisters, after Shutdown.Drain the listeners and the internal
// API finish in-flight calls within one Shutdown.Listeners window (then
// streams are cut and internal calls cancelled), and at least
// Shutdown.Reserve is left for components, which stop before the
// dependencies they use. A second SIGINT/SIGTERM during the stop exits at
// once with status 1. A panicking handler fails its request (codes.Internal,
// HTTP 500), never the process. Consul, NATS, Temporal and the telemetry
// collector are optional. Install configured backends explicitly with drivers
// (for example standard.Drivers). RequireNATS or RequireTemporal make a
// configured connection part of readiness.
package backplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/backplane-labs/backplane/pkg/backplane/build"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
)

// Root is the root of the author's tree, handed to the State constructor:
// create components and dependencies under it, read the configuration from
// it. Embedding it in State is optional.
type Root[C any] struct {
	deps.Component

	cfg *C
	id  Identity
}

// Config is a copy of the loaded configuration. Static fields never change;
// config.Live fields are shared by every copy and update in place.
func (r Root[C]) Config() C {
	if r.cfg == nil {
		var zero C

		return zero
	}

	return *r.cfg
}

// Identity of the instance.
func (r Root[C]) Identity() Identity { return r.id }

// Service is one process on the SDK: the author's State plus what it serves.
type Service[St any] struct {
	*core

	state *St
}

// State is what the constructor built.
func (s *Service[St]) State() *St { return s.state }

// Open loads the configuration, builds the State with newState and returns a
// service ready to declare routes and Run. Nothing listens until Run.
//
// Started as "<binary> probe [readiness|liveness|startup]", Open probes the
// service running next to it instead and exits (see ProbeCommand).
//
// ctx bounds loading only (a required Live value may wait for Consul); the
// service does not depend on it afterwards. An invalid configuration or a
// failing constructor is an error; an absent Consul is not. Call Close if
// Run will not be called.
func Open[C config.Backplaner, St any](
	ctx context.Context, newState func(root Root[C]) (*St, error), opts ...Option,
) (*Service[St], error) {
	o := options{id: Identity{Service: build.Get().Service, Version: build.Get().Version}, slog: true}
	for _, opt := range opts {
		opt(&o)
	}

	if o.id.Service == "" {
		return nil, fmt.Errorf("backplane: %w (or pass backplane.Name())", build.ErrUnnamed)
	}

	if !serviceName.MatchString(o.id.Service) {
		return nil, fmt.Errorf("%w: service name %q must match %s", ErrConfig, o.id.Service, serviceName)
	}

	// "<binary> probe [kind]": probe the service running next to it and exit.
	if kind, ok, err := probeKind(os.Args); ok {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(usageExit)
		}

		runProbe[C](ctx, kind, append([]config.Option{config.Service(o.id.Service)}, o.config...))
	}

	conf, err := config.Open[C](ctx, append([]config.Option{config.Service(o.id.Service)}, o.config...)...)
	if err != nil {
		return nil, fmt.Errorf("backplane: %w", err)
	}

	block := (*conf.Value()).BackplaneConfig()
	if err := errors.Join(validate(block), o.validate(block)); err != nil {
		return nil, errors.Join(fmt.Errorf("backplane: %w", err), conf.Close())
	}

	c := newCore(ctx, o, link.ConfigState(conf), block)

	scope := link.Scope(c.app).(deps.Component) //nolint:forcetypeassert,errcheck // deps installs it
	root := Root[C]{Component: scope, cfg: conf.Value(), id: c.id}

	state, err := newState(root)
	if err != nil {
		return nil, fmt.Errorf("backplane: state: %w", errors.Join(err, c.Close()))
	}

	return &Service[St]{core: c, state: state}, nil
}

// ErrConfig: the SDK block or the service name is unusable.
var ErrConfig = errors.New("backplane: invalid configuration")

// serviceName is the form of a service name (§17): it becomes a DNS label,
// a NATS subject token and a Temporal task queue.
var serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// validate checks what the schema cannot express.
func validate(b config.Backplane) error {
	return errors.Join(validateShutdown(b.Shutdown), validateHealth(b.Health), validateNATS(b.NATS))
}

// validateShutdown: the stages fit the budget, leaving the reserve.
func validateShutdown(s config.Shutdown) error {
	switch {
	case s.Timeout <= 0:
		return fmt.Errorf("%w: shutdown.timeout must be positive, got %v", ErrConfig, s.Timeout)
	case s.Drain < 0 || s.Drain >= s.Timeout:
		return fmt.Errorf("%w: shutdown.drain must be in [0, timeout), got %v", ErrConfig, s.Drain)
	case s.Listeners <= 0:
		return fmt.Errorf("%w: shutdown.listeners must be positive, got %v", ErrConfig, s.Listeners)
	case s.Reserve < 0:
		return fmt.Errorf("%w: shutdown.reserve must be >= 0, got %v", ErrConfig, s.Reserve)
	case s.Drain+s.Listeners+s.Reserve > s.Timeout:
		return fmt.Errorf("%w: shutdown.drain + listeners + reserve (%v + %v + %v) must be <= timeout (%v): "+
			"a shorter timeout needs shorter stages (BACKPLANE_SHUTDOWN_LISTENERS, _RESERVE, _DRAIN)",
			ErrConfig, s.Drain, s.Listeners, s.Reserve, s.Timeout)
	}

	return nil
}

// validateHealth: a check fits its interval.
func validateHealth(h config.Health) error {
	switch {
	case h.Interval <= 0:
		return fmt.Errorf("%w: health.interval must be positive, got %v", ErrConfig, h.Interval)
	case h.Timeout <= 0 || h.Timeout > h.Interval:
		return fmt.Errorf("%w: health.timeout must be in (0, interval], got %v", ErrConfig, h.Timeout)
	}

	return nil
}

// validate checks the options against the block: a required transport
// must be configured.
func (o options) validate(b config.Backplane) error {
	switch {
	case b.NATS.Enabled() && o.events == nil:
		return fmt.Errorf("%w: nats configured without an event driver (drivers/nats.Driver)", ErrConfig)
	case b.Temporal.Enabled() && o.workflows == nil:
		return fmt.Errorf("%w: temporal configured without a workflow driver (drivers/temporal.Driver)", ErrConfig)
	case o.requireNATS && !b.NATS.Enabled():
		return fmt.Errorf("%w: RequireNATS without nats.url", ErrConfig)
	case o.requireTemporal && !b.Temporal.Enabled():
		return fmt.Errorf("%w: RequireTemporal without temporal.addr", ErrConfig)
	}

	return nil
}

// validateNATS checks the stream settings JetStream would reject.
func validateNATS(n config.NATS) error {
	switch {
	case !n.Enabled():
		return nil
	case n.MaxAge < 0:
		return fmt.Errorf("%w: nats.max_age must be >= 0 (0: unlimited), got %v", ErrConfig, n.MaxAge)
	case n.DLQMaxAge < 0:
		return fmt.Errorf("%w: nats.dlq_max_age must be >= 0 (0: unlimited), got %v", ErrConfig, n.DLQMaxAge)
	case n.DedupWindow <= 0 || (n.MaxAge > 0 && n.DedupWindow > n.MaxAge):
		return fmt.Errorf("%w: nats.dedup_window must be in (0, max_age], got %v", ErrConfig, n.DedupWindow)
	}

	return nil
}
