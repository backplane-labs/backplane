package backplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/build"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

const devVersion = "0.0.0"

// Run brings the service up and blocks until ctx ends, SIGINT/SIGTERM
// arrives or a node's goroutine fails; then stops everything within the
// shutdown budget. A stop requested during start is not an error. A signal
// during the stop exits the process at once with status 1.
func (c *core) Run(ctx context.Context) error {
	c.mu.Lock()
	if c.phase != declaring {
		c.mu.Unlock()

		return ErrClosed
	}

	c.phase = running
	c.mu.Unlock()

	if c.opts.slog {
		prev := slog.Default()

		slog.SetDefault(slog.New(xlog.NewSlogHandler(c.log)))

		defer slog.SetDefault(prev)
	}

	m, err := c.seal()
	if err != nil {
		return errors.Join(fmt.Errorf("backplane: manifest: %w", err), c.conf.Close())
	}

	c.tail(m)

	if c.cfg.InternalSecret.Reveal() == "" {
		c.log.Warn("internal secret not set: the internal API and UI on the platform port are open to anyone who reaches it")
	}

	sigs, stopSignals := c.signals()
	defer stopSignals()

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	stopping, done := make(chan struct{}), make(chan struct{})
	defer close(done)

	go c.watchSignals(sigs, cancelRun, stopping, done)

	var cause error

	if err := c.svc.Start(runCtx); err != nil {
		if runCtx.Err() == nil {
			cause = fmt.Errorf("backplane: start: %w", err)
		} else {
			c.log.Info("stopped during start")
		}
	} else {
		select {
		case <-runCtx.Done():
			c.log.Info("stopping")
		case err := <-c.svc.Failed():
			cause = fmt.Errorf("backplane: %w", err)
		}
	}

	close(stopping)

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.Shutdown.Timeout)
	defer cancel()

	if err := c.svc.Stop(stopCtx); err != nil {
		return errors.Join(cause, fmt.Errorf("backplane: stop: %w", err))
	}

	return cause
}

// seal ends declarations and builds the manifest; an unstamped version gets
// the manifest's content hash so dev builds do not overwrite each other.
func (c *core) seal() (*backplanev1.Manifest, error) {
	c.env.Manifest.Nodes(nodes(c.app))
	c.env.Manifest.Seal()

	m, err := c.env.Manifest.Build()
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	if m.GetVersion() == devVersion {
		raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		sum := sha256.Sum256(raw)
		m.Version = devVersion + "+" + hex.EncodeToString(sum[:6])
	}

	return m, nil
}

// tail adds the nodes that follow the author's tree:
//
//	internal-api → reactors → worker → public:<addr> → drain → register → serving
//
// (the ones ahead of it — config, telemetry, health, platform, consul,
// nats, temporal, hooks — are newCore's). They start in this order and
// stop in reverse: traffic of every kind ends before the tree stops. The
// public listeners and the internal gate share one Shutdown.Listeners
// window, as do the reactors and the worker (traffic too), which leaves
// Shutdown.Reserve of the budget to the tree.
func (c *core) tail(m *backplanev1.Manifest) {
	sd := c.cfg.Shutdown
	listeners := &window{d: sd.Listeners, reserve: sd.Reserve}

	gn := c.svc.Child("internal-api", node.System, false)
	gn.OnStart(func(context.Context) error { c.gate.Open(); return nil })
	gn.OnStop(c.gate.Close)
	gn.Budget(listeners.derive)

	c.work(m, listeners)

	for _, p := range c.public {
		c.listen("public"+p.addr, p.addr, p.grpc, c.publicHandler(p), c.listenOptions(p)...).Budget(listeners.derive)
	}

	dn := c.svc.Child("drain", node.System, false)
	dn.OnStop(func(ctx context.Context) error { return pause(ctx, c.drainFor(ctx)) })

	// The instance state is published from the consul node ahead of the
	// tree; the catalog registration waits for here.
	if c.consulPresence = c.presence(m); c.consulPresence != nil {
		rn := c.svc.Child("register", node.System, false)
		rn.OnStart(c.consulPresence.Register)
		rn.OnStop(c.consulPresence.Deregister)
	}

	sn := c.svc.Child("serving", node.System, false)
	sn.OnStart(func(ctx context.Context) error { c.health.Serving(ctx, true); return nil })
	sn.OnStop(func(ctx context.Context) error { c.health.Serving(ctx, false); return nil })
}

// work adds the event reactors and the Temporal worker when the service
// declared something for them; without the transport it warns.
func (c *core) work(m *backplanev1.Manifest, listeners *window) {
	switch {
	case len(m.GetSubscriptions()) == 0:
	case c.broker == nil:
		c.log.Warn("reactors declared but NATS is not configured: events will not be consumed")
	default:
		n := c.svc.Child("reactors", node.System, false)
		n.OnStart(func(ctx context.Context) error { return c.broker.StartReactors(ctx, n) })
		n.OnStop(c.broker.StopReactors)
		n.Budget(listeners.derive)
	}

	// Hook calls go through Temporal's Nexus or directly to backplane
	// (hookTransport); only the first needs Temporal here.
	if len(m.GetHooks()) > 0 && c.temporal == nil && c.cfg.Hooks.Resolve(false, c.directHooks()) == config.HooksTemporal {
		c.log.Warn("hooks declared but Temporal is not configured: calls fail as unavailable")
	}

	// Activities are served over the platform port (ActivityService) on
	// any runtime; the Temporal worker adds Temporal bindings and the
	// service's own workflows.
	switch {
	case len(m.GetActivities()) == 0 && len(c.env.WorkerRegistrations()) == 0:
	case c.temporal == nil && len(c.env.WorkerRegistrations()) > 0:
		c.log.Warn("workflows registered but Temporal is not configured: they are unavailable")
	case c.temporal == nil:
		c.log.Info("activities served over the platform port only: Temporal is not configured")
	case !c.cfg.Temporal.Worker.Enabled:
		c.log.Info("temporal worker disabled on this replica: other replicas serve the task queue")
	default:
		n := c.svc.Child("worker", node.System, false)
		n.OnStart(func(ctx context.Context) error { return c.temporal.StartWorker(ctx, n) })
		n.OnStop(c.temporal.StopWorker)
		n.Budget(listeners.derive)
	}
}

// pause waits d or until ctx ends: load balancers catch up with the
// deregistration before listeners close.
func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("drain: %w", ctx.Err())
	}
}

// presence builds the explicitly installed discovery driver.
func (c *core) presence(m *backplanev1.Manifest) Presence { //nolint:ireturn // discovery is an installed driver
	if err := c.conf.Degraded(); err != nil {
		c.log.Warn("remote config unavailable, retrying", xlog.Err(err))
	}

	if !c.cfg.Consul.Enabled() || c.opts.presence == nil {
		c.log.Warn("discovery not configured: unregistered, config from env/file only")
		return nil
	}

	presence, err := c.opts.presence(PresenceParams{
		Config: c.cfg, Identity: c.id, Manifest: m, Log: c.log, Configuration: c.conf,
		PlatformPort: port16(c.cfg.InternalPort),
		PrimaryPort:  c.primaryPort(), Commit: build.Get().Commit, Transports: c.transports, Nodes: c.nodes,
	})
	if err != nil {
		c.log.Warn("discovery presence", xlog.Err(err))
		return nil
	}

	return presence
}

// nodes describes the author's tree for the manifest.
func nodes(app *node.Node) []*backplanev1.Node {
	kinds := map[node.Kind]backplanev1.NodeKind{
		node.Component:  backplanev1.NodeKind_NODE_KIND_COMPONENT,
		node.Dependency: backplanev1.NodeKind_NODE_KIND_DEPENDENCY,
		node.Singleton:  backplanev1.NodeKind_NODE_KIND_SINGLETON,
	}

	var out []*backplanev1.Node

	app.Walk(func(n *node.Node) {
		if k, ok := kinds[n.Kind()]; ok {
			out = append(out, &backplanev1.Node{Path: n.Path(), Kind: k, Optional: n.Optional()})
		}
	})

	return out
}

// port16 narrows a schema-validated port (1..65535).
func port16(p int64) uint16 { return uint16(p) } //nolint:gosec // validated by the config schema
