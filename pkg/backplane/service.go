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
// nodes.
//
// Configuration, telemetry, health and the platform listener start before the service tree.
// The SDK then opens the internal API and public listeners, registers the instance and marks it serving.
// It stops in exact reverse within Shutdown.Timeout: readiness drops, the
// instance deregisters, after Shutdown.Drain the listeners and the internal
// API finish in-flight calls within one Shutdown.Listeners window (then
// streams are cut and internal calls cancelled), and at least
// Shutdown.Reserve is left for components, which stop before the
// dependencies they use. A second SIGINT/SIGTERM during the stop exits at
// once with status 1. A panicking handler fails its request (codes.Internal,
// HTTP 500), never the process. Install Consul discovery explicitly with consul.Driver().
package backplane

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"regexp"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/backplane-labs/backplane/pkg/backplane/build"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/telemetry"
	"github.com/backplane-labs/backplane/pkg/backplane/meshdial"
)

// Root is the root of the author's tree, handed to the State constructor:
// create components and dependencies under it, read the configuration from
// it. Embedding it in State is optional.
type Root[C any] struct {
	deps.Component

	cfg  *C
	conf *config.Runtime[C]
	id   Identity
	mesh *meshState
	// describing: the binary runs as "<binary> manifest".
	describing bool
	replicas   func() (ReplicaWatch, error)
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

// ConfigRevision is the console revision the applied Live values come
// from: the same on every replica, a new one for every save, including a
// rollback or a save that restores earlier values, so it tells apart two
// applications of equal values; 0 without Consul or before the first save.
func (r Root[C]) ConfigRevision() uint64 {
	if r.conf == nil {
		return 0
	}

	return r.conf.Revision()
}

// WatchConfig calls fn with the revision after every applied update of
// the Live fields, once their own Watch callbacks ran; a rejected update
// calls nothing.
func (r Root[C]) WatchConfig(fn func(revision uint64)) {
	if r.conf != nil {
		r.conf.OnApplied(fn)
	}
}

// Identity of the instance.
func (r Root[C]) Identity() Identity { return r.id }

// Describing reports whether the service is only being described: the
// binary runs as "<binary> manifest", which declares and starts nothing. A
// constructor skips its checks of what only running needs (a credential, a
// key); secret configuration fields are optional then and no
// config.Validator runs.
func (r Root[C]) Describing() bool { return r.describing }

// ServiceCredentials are the gRPC credentials of a client of target's
// api.MTLS port: this service's certificate, the server verified as target.
//
//	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(root.ServiceCredentials("sync")))
//
// DialService builds the channel with them and recovers from address
// changes. Without a mesh (the Consul driver and Consul) every handshake fails with
// ErrNoMesh.
//
//nolint:ireturn // gRPC's credentials
func (r Root[C]) ServiceCredentials(target string) credentials.TransportCredentials {
	return r.meshState().ServiceCredentials(target)
}

// DialService is a channel to target's api.MTLS port at addr (host:port)
// with ServiceCredentials: the server verified as target on every
// handshake, the channel recovering within seconds when the peer's address
// moves (meshdial). Calls are not retried; one may wait for readiness
// within its deadline with grpc.WaitForReady(true). opts apply last.
//
//	conn, err := root.DialService("sync", cfg.Sync.Addr)
func (r Root[C]) DialService(target, addr string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return meshdial.Dial(addr, r.ServiceCredentials(target), opts...) //nolint:wrapcheck // meshdial's
}

// DialPlatform is DialService of target's platform port, with
// PlatformCredentials.
func (r Root[C]) DialPlatform(target, addr string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return meshdial.Dial(addr, r.PlatformCredentials(target), opts...) //nolint:wrapcheck // meshdial's
}

// ServiceTLS is ServiceCredentials for an HTTP client:
// &http.Transport{TLSClientConfig: root.ServiceTLS("sync")}.
func (r Root[C]) ServiceTLS(target string) *tls.Config { return r.meshState().ServiceTLS(target) }

// PlatformCredentials are the gRPC credentials of a call to target's
// platform port (port 9400: its internal API): mutual TLS when
// backplane.mesh.platform is mtls, plain otherwise. What backplane and the
// SDK dial platform ports with.
//
//nolint:ireturn // gRPC's credentials
func (r Root[C]) PlatformCredentials(target string) credentials.TransportCredentials {
	return r.meshState().PlatformCredentials(target)
}

// PlatformTLS is PlatformCredentials for an HTTP client; nil when plain.
func (r Root[C]) PlatformTLS(target string) *tls.Config { return r.meshState().PlatformTLS(target) }

func (r Root[C]) meshState() *meshState {
	if r.mesh == nil {
		return &meshState{err: ErrNoMesh}
	}

	return r.mesh
}

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
	o := options{
		id: Identity{Service: build.Get().Service, Version: build.Get().Version}, slog: true,
		args: os.Args, stdout: os.Stdout,
	}
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
	if kind, ok, err := probeKind(o.args); ok {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(usageExit)
		}

		runProbe[C](ctx, kind, append([]config.Option{config.Service(o.id.Service)}, o.config...))
	}

	// "<binary> manifest": declare, print the manifest, exit (Run).
	manifest, err := manifestMode(o.args)
	if err != nil {
		return nil, err
	}

	if manifest {
		o.manifest = true
		o.config = append(o.config, config.Describing())

		if o.log == nil {
			o.log = stderrLogger()
		}
	}

	conf, err := config.Open[C](ctx, append([]config.Option{config.Service(o.id.Service)}, o.config...)...)
	if err != nil {
		return nil, fmt.Errorf("backplane: %w", err)
	}

	block := (*conf.Value()).BackplaneConfig()
	if err := validate(block); err != nil {
		return nil, errors.Join(fmt.Errorf("backplane: %w", err), conf.Close())
	}

	c := newCore(ctx, o, link.ConfigState(conf), block)

	if err := c.mesh.platformErr(); err != nil {
		return nil, errors.Join(fmt.Errorf("backplane: %w", err), c.Close())
	}

	scope := link.Scope(c.app).(deps.Component) //nolint:forcetypeassert,errcheck // deps installs it
	root := Root[C]{
		Component: scope, cfg: conf.Value(), conf: conf, id: c.id, mesh: c.mesh,
		describing: o.manifest, replicas: o.replicaDiscovery(block.Consul),
	}

	state, err := newState(root)
	if err != nil {
		return nil, fmt.Errorf("backplane: state: %w", errors.Join(err, c.Close()))
	}

	return &Service[St]{core: c, state: state}, nil
}

var serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ErrConfig: the SDK block or the service name is unusable.
var ErrConfig = errors.New("backplane: invalid configuration")

// validate checks what the schema cannot express.
func validate(b config.Backplane) error {
	var histograms error
	if _, err := telemetry.HistogramView(b.Telemetry.Metrics.Histograms); err != nil {
		histograms = fmt.Errorf("%w: telemetry.metrics.histograms: %w", ErrConfig, err)
	}

	if _, err := telemetry.Sampler(b.Telemetry.Traces); err != nil {
		histograms = errors.Join(histograms, fmt.Errorf("%w: telemetry.traces.sampler: %w", ErrConfig, err))
	}

	var mesh error
	if d := b.Mesh.Default; d != config.MeshAllow && d != config.MeshDeny {
		mesh = fmt.Errorf("%w: mesh.default must be allow or deny, got %q", ErrConfig, d)
	}

	switch b.Mesh.Platform {
	case config.PlatformPlain, config.PlatformAccept, config.PlatformMTLS:
	default:
		mesh = errors.Join(mesh,
			fmt.Errorf("%w: mesh.platform must be plain, accept or mtls, got %q", ErrConfig, b.Mesh.Platform))
	}

	return errors.Join(validateShutdown(b.Shutdown), validateHealth(b.Health), histograms, mesh)
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
