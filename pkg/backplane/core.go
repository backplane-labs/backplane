package backplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"google.golang.org/grpc"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/direct"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/gate"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/health"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/listener"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/recovery"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/telemetry"
)

const (
	uiPath     = "/_backplane/ui/"
	probesPath = "/healthz/"
)

// ErrClosed is returned by Run after Close or a previous Run.
var ErrClosed = errors.New("backplane: service already ran or was closed")

type phase int

const (
	declaring phase = iota
	running
	closed
)

// core is the untyped engine behind Service[St]: identity, configuration,
// the node tree and the declarations.
type core struct {
	id   Identity
	cfg  config.Backplane
	log  *xlog.Logger
	conf configrt.State
	env  *env.Env
	opts options

	svc *node.Node // service root: the SDK's nodes and the author's tree
	app *node.Node // the author's tree

	broker     EventTransport // nil without NATS
	telemetry  *telemetry.Telemetry
	temporal   WorkflowTransport // nil without Temporal
	health     *health.Health
	guard      guard.Guard
	gate       *gate.Gate
	internal   *grpc.Server   // platform port, gRPC: internal API + health
	platform   *http.ServeMux // platform port, HTTP behind the guard: UI bundle
	apiBundles map[string]bool
	public     []*publicPort
	// consulPresence is built by Run (it needs the manifest); nil without
	// Consul.
	consulPresence Presence
	// mesh is mutual TLS between services.
	mesh *meshState

	mu    sync.Mutex
	phase phase
}

// newCore builds the SDK's leading nodes; the author's tree hangs under app.
func newCore(ctx context.Context, o options, conf configrt.State, cfg config.Backplane) *core {
	id := o.id.resolve(ctx, cfg)
	log := id.logger(o.log, cfg)
	conf.SetLog(log)
	id.warnUnreachable(log, cfg)
	m := manifest.New(id.Service, id.Version)
	m.Config(conf.Schema(), conf.LivePaths())

	c := &core{
		id: id, cfg: cfg, log: log, conf: conf, opts: o,
		env:      env.New(id.Service, m),
		health:   health.New(log, cfg.Health.Interval, cfg.Health.Timeout),
		guard:    guard.New(cfg.InternalSecret.Reveal(), cfg.InternalSecretPrevious.Reveal()),
		gate:     gate.New(),
		platform: http.NewServeMux(),
	}
	c.internal = grpc.NewServer(c.serverOptions(recovery.GRPCInternal,
		append(c.guard.ServerOptions(), c.gate.ServerOptions()...)...)...)
	hv1.RegisterHealthServer(c.internal, c.health.GRPC())
	backplanev1.RegisterActivityServiceServer(c.internal, direct.NewServer(id.Service, c.env, log))
	c.platformRoutes()

	c.svc = node.New(id.Service, log, c.env)
	c.svc.SetProbeInterval(cfg.Health.Interval)

	cn := c.svc.Child("config", node.System, false)
	cn.OnStop(func(context.Context) error { return conf.Close() })

	c.telemetry = telemetry.New(telemetry.Identity{
		Service: id.Service, Version: id.Version, Instance: id.Instance, Environment: id.Environment,
	}, cfg.Telemetry.Metrics, log)
	tn := c.svc.Child("telemetry", node.System, false)
	tn.OnStart(c.telemetry.Start)
	tn.OnStop(c.telemetry.Stop)

	hn := c.svc.Child("health", node.System, false)
	hn.OnStart(func(ctx context.Context) error { return c.health.Start(ctx, hn) })

	c.listen("platform", listenAddr(cfg.InternalPort), c.internal, c.platformHandler())
	c.presenceNode()
	c.connect()
	c.requireTransports()
	c.mesh = c.newMesh()

	c.app = c.svc.Child(id.Service, node.Root, false)
	c.health.Add(health.Ready, c.app.Readiness())

	return c
}

// connect adds the NATS and Temporal connections ahead of the author's tree,
// so components may publish and call hooks from their start.
func (c *core) connect() {
	scope := link.Scope(c.svc).(deps.Scope) //nolint:forcetypeassert,errcheck // deps installs it

	if c.cfg.NATS.Enabled() {
		c.broker = c.opts.events(c.cfg, scope, c.id)
		n := c.svc.Child("nats", node.System, false)
		n.OnStart(func(ctx context.Context) error { return c.broker.Connect(ctx, n) })
		n.OnStop(c.broker.Close)
		c.env.SetBroker(c.broker)
	}

	if c.cfg.Temporal.Enabled() {
		c.temporal = c.opts.workflows(c.cfg, scope, c.id)
		n := c.svc.Child("temporal", node.System, false)
		n.OnStart(func(ctx context.Context) error { return c.temporal.Connect(ctx, n) })
		n.OnStop(c.temporal.Close)

		// Its own worker serves hook calls raised outside workflows, so
		// components may call hooks from OnStart and OnStop.
		hn := c.svc.Child("hooks", node.System, false)
		hn.OnStart(func(ctx context.Context) error { return c.temporal.StartHookWorker(ctx, hn) })
		hn.OnStop(c.temporal.StopHookWorker)
	}

	c.hookTransport()
	c.watchBound()
}

// boundWatcher is a discovery that also follows the service's bound hooks
// (the Consul driver's).
type boundWatcher interface {
	WatchBound(ctx context.Context, service string, fn func(hooks []string)) error
}

// watchBound follows backplane's published bound hooks of this service so
// hook.Ref.Bound answers locally; without Consul they stay unknown (bound).
func (c *core) watchBound() {
	if !c.directHooks() {
		return
	}

	find, err := c.opts.discovery(c.cfg, c.log)
	if err != nil {
		c.log.Warn("bound hooks watch", xlog.Err(err))

		return
	}

	w, ok := find.(boundWatcher)
	if !ok {
		return
	}

	n := c.svc.Child("bound-hooks", node.System, false)
	n.Go(func(ctx context.Context) error { return w.WatchBound(ctx, c.id.Service, c.env.SetBound) })
}

// directHooks reports whether the direct hook transport is possible:
// Consul is configured and a discovery driver is installed.
func (c *core) directHooks() bool { return c.cfg.Consul.Enabled() && c.opts.discovery != nil }

// hookTransport installs the caller hook calls go through: Temporal's
// Nexus, or backplane's HookService over gRPC with its replicas found
// through the installed discovery (the direct transport).
func (c *core) hookTransport() {
	switch c.cfg.Hooks.Resolve(c.temporal != nil, c.directHooks()) {
	case config.HooksTemporal:
		if c.temporal == nil {
			c.log.Warn("hooks.transport is temporal but temporal is not configured: hook calls are unavailable")

			return
		}

		c.env.SetCaller(c.temporal)
	case config.HooksDirect:
		if c.opts.discovery == nil || !c.cfg.Consul.Enabled() {
			c.log.Warn("hooks.transport is direct but consul is not configured: hook calls are unavailable")

			return
		}

		find, err := c.opts.discovery(c.cfg, c.log)
		if err != nil {
			c.log.Warn("direct hook transport", xlog.Err(err))

			return
		}

		caller := direct.NewCaller(direct.CallerParams{
			Service: c.id.Service, Instance: c.id.Instance, Secret: c.cfg.InternalSecret.Reveal(),
			Timeout: c.cfg.Temporal.HookTimeout, Discovery: find,
		})
		c.env.SetHookTimeout(c.cfg.Temporal.HookTimeout)
		c.env.SetCaller(caller)

		n := c.svc.Child("hooks", node.System, false)
		n.OnStop(func(context.Context) error { caller.Close(); return nil })
	default:
		c.log.Warn("no hook transport: neither temporal nor consul is configured, hook calls are unavailable")
	}
}

// listen adds a listener node.
func (c *core) listen(name, addr string, g *grpc.Server, h http.Handler, opts ...listener.Option) *node.Node {
	l := listener.New(name, addr, g, h, c.log, c.limits(), opts...)
	n := c.svc.Child(name, node.System, false)
	n.OnStart(func(ctx context.Context) error { return l.Start(ctx, n) })
	n.OnStop(l.Stop)

	return n
}

// Name of the service.
func (c *core) Name() string { return c.id.Service }

// Identity of the instance.
func (c *core) Identity() Identity { return c.id }

// Log is the service logger; context-aware calls carry trace and span ids.
func (c *core) Log() *xlog.Logger { return c.log }

// declare runs fn while the service is still being declared; after Run it
// is a programming error.
func (c *core) declare(what string, fn func()) {
	c.mu.Lock()
	ph := c.phase
	c.mu.Unlock()

	if ph != declaring {
		panic("backplane: " + what + " declared after Run")
	}

	fn()
}

// Close releases what Open acquired when Run will not be called. Safe to
// call more than once; Run closes by itself.
func (c *core) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.phase != declaring {
		return nil
	}

	c.phase = closed
	c.env.Manifest.Seal()

	if err := c.conf.Close(); err != nil {
		return fmt.Errorf("backplane: %w", err)
	}

	return nil
}

func listenAddr(port int64) string { return ":" + strconv.FormatInt(port, 10) }
