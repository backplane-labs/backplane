package backplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/gate"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/health"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/listener"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/recovery"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
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
	id       Identity
	cfg      config.Backplane
	log      *xlog.Logger
	conf     configrt.State
	manifest *manifest.Builder
	opts     options

	svc *node.Node // service root: the SDK's nodes and the author's tree
	app *node.Node // the author's tree

	telemetry *telemetry.Telemetry
	health    *health.Health
	guard     guard.Guard
	gate      *gate.Gate
	internal  *grpc.Server     // platform port, gRPC: internal API + health
	internals *routes.Dispatch // per-registration interceptors of the internal API
	platform  *http.ServeMux   // platform port, HTTP behind the guard: UI bundle
	public    []*publicPort
	// declared are the ports of the author's named listeners (Listener).
	declared map[string]uint16
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
		manifest: m,
		health:   health.New(log, cfg.Health.Interval, cfg.Health.Timeout),
		guard:    guard.New(cfg.InternalSecret.Reveal(), cfg.InternalSecretPrevious.Reveal()),
		gate:     gate.New(),
		platform: http.NewServeMux(),
	}
	c.platformRoutes()

	c.svc = node.New(id.Service, log)
	c.svc.SetProbeInterval(cfg.Health.Interval)
	c.mesh = c.newMesh()

	// Mutual TLS of the platform port authorizes first, then the secret.
	var mutual []grpc.ServerOption
	if p := c.mesh.platformTLS(); p != nil {
		mutual = p.GRPCServerOptions()
	}

	c.internals = routes.NewDispatch()
	c.internal = grpc.NewServer(c.serverOptions(recovery.GRPCInternal,
		slices.Concat(mutual, c.guard.ServerOptions(), c.gate.ServerOptions(), c.internals.ServerOptions())...)...)
	hv1.RegisterHealthServer(c.internal, c.health.GRPC())

	cn := c.svc.Child("config", node.System, false)
	cn.OnStop(func(context.Context) error { return conf.Close() })

	c.telemetry = telemetry.New(telemetry.Identity{
		Service: id.Service, Version: id.Version, Instance: id.Instance, Environment: id.Environment,
	}, cfg.Telemetry, log)
	tn := c.svc.Child("telemetry", node.System, false)
	tn.OnStart(c.telemetry.Start)
	tn.OnStop(c.telemetry.Stop)
	// Telemetry stops last: its flush gets Shutdown.Reserve, ending early
	// enough that a hung collector never runs the stop past its budget.
	tn.Budget(func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithDeadline(ctx, keepReserve(ctx, time.Now().Add(cfg.Shutdown.Reserve), stopSlack))
	})

	hn := c.svc.Child("health", node.System, false)
	hn.OnStart(func(ctx context.Context) error { return c.health.Start(ctx, hn) })

	c.listen("platform", listenAddr(cfg.InternalPort), c.internal, c.platformHandler(), c.platformListenOptions()...)
	c.presenceNode()

	c.app = c.svc.Child(id.Service, node.Root, false)
	c.health.Add(health.Ready, c.app.Readiness())
	// Liveness covers the whole tree: the SDK's own transports and the
	// author's dependencies fail it only in a state the process cannot leave.
	c.health.Add(health.Live, c.svc.Liveness())

	return c
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
	c.manifest.Seal()

	if err := c.conf.Close(); err != nil {
		return fmt.Errorf("backplane: %w", err)
	}

	return nil
}

func listenAddr(port int64) string { return ":" + strconv.FormatInt(port, 10) }
