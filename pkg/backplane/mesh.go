package backplane

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/grpc/credentials"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/health"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/mtls"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// Mesh is a service's identity among services: its certificate, the roots
// peers' chains lead to and who may call it, followed live. The Consul
// driver installs one over Consul's CA and intentions.
type Mesh interface {
	// Certificate is the current certificate; nil until there is one.
	Certificate() *tls.Certificate
	// Roots are the current trusted roots; nil until known.
	Roots() *x509.CertPool
	// Authorize decides whether caller (a service name) may call this
	// service, and why.
	Authorize(caller string) (bool, string)
	// Run follows the identity until ctx ends.
	Run(ctx context.Context) error
	// Ready is nil once a certificate and the roots are known.
	Ready() error
}

// MeshFactory builds a service's mesh identity without starting it.
type MeshFactory func(cfg config.Backplane, service string, log *xlog.Logger) (Mesh, error)

// WithMesh installs where mutual TLS between services gets its identity
// (the Consul driver installs Consul's CA).
func WithMesh(factory MeshFactory) Option { return func(o *options) { o.mesh = factory } }

// ErrNoMesh: mutual TLS between services needs a mesh driver and Consul.
var ErrNoMesh = errors.New("backplane: mutual TLS needs the Consul driver and backplane.consul.addr")

// Caller is the service that made a request on an mTLS port (api.MTLS).
type Caller struct {
	// Service is the caller's service name.
	Service string
	// URI is its SPIFFE identity.
	URI string
}

// CallerOf is the service that made the request ctx belongs to, on a port
// served with api.MTLS; ok is false elsewhere.
func CallerOf(ctx context.Context) (Caller, bool) {
	p, ok := mtls.PeerFrom(ctx)

	return Caller(p), ok
}

// meshState is the service's mesh: built at Open, followed from the first
// use (an mTLS route, a client's credentials), so services that never use
// it ask Consul for nothing.
type meshState struct {
	src  Mesh
	err  error // why there is no mesh
	use  chan struct{}
	once sync.Once
}

// newMesh builds the mesh node ahead of the author's tree: its goroutine
// waits for the first use, then follows the identity; readiness requires a
// certificate once it is used.
func (c *core) newMesh() *meshState {
	m := &meshState{use: make(chan struct{})}

	switch {
	case c.opts.mesh == nil || !c.cfg.Consul.Enabled():
		m.err = ErrNoMesh

		return m
	default:
		src, err := c.opts.mesh(c.cfg, c.id.Service, c.log)
		if err != nil {
			m.err = fmt.Errorf("backplane: mesh: %w", err)

			return m
		}

		m.src = src
	}

	n := c.svc.Child("mesh", node.System, false)
	n.Go(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-m.use:
		}

		return m.src.Run(ctx)
	})

	c.health.Add(health.Ready, probe.FromError(func(context.Context) error {
		select {
		case <-m.use:
			return m.src.Ready()
		default:
			return nil
		}
	}))

	return m
}

// source is the mesh, marked used.
//
//nolint:ireturn // the installed driver's
func (m *meshState) source() (Mesh, error) {
	if m.src == nil {
		return nil, m.err
	}

	m.once.Do(func() { close(m.use) })

	return m.src, nil
}

// unavailable is the identity of a service without a mesh: every handshake
// fails, saying why.
type unavailable struct{ err error }

func (unavailable) Certificate() *tls.Certificate { return nil }
func (unavailable) Roots() *x509.CertPool         { return nil }

func (u unavailable) Authorize(string) (bool, string) { return false, u.err.Error() }

func (u unavailable) Err() error { return u.err }

//nolint:ireturn // the mesh, or why there is none
func (m *meshState) clientSource() mtls.Source {
	src, err := m.source()
	if err != nil {
		return unavailable{err: err}
	}

	return src
}

// ServiceTLS is the TLS configuration of an HTTP client calling target over
// mutual TLS: this service's certificate, the server verified as target
// against the mesh's roots. Without a mesh every handshake fails.
func (m *meshState) ServiceTLS(target string) *tls.Config {
	return mtls.ClientConfig(m.clientSource(), target, "http/1.1")
}

// ServiceCredentials are the gRPC transport credentials of a client calling
// target over mutual TLS: grpc.NewClient(addr,
// grpc.WithTransportCredentials(root.ServiceCredentials("sync"))).
//
//nolint:ireturn // gRPC's credentials
func (m *meshState) ServiceCredentials(target string) credentials.TransportCredentials {
	return credentials.NewTLS(mtls.ClientConfig(m.clientSource(), target, "h2"))
}

// ServiceCredentials are Root.ServiceCredentials, after Open.
//
//nolint:ireturn // gRPC's credentials
func (c *core) ServiceCredentials(target string) credentials.TransportCredentials {
	return c.mesh.ServiceCredentials(target)
}

// ServiceTLS is Root.ServiceTLS, after Open.
func (c *core) ServiceTLS(target string) *tls.Config { return c.mesh.ServiceTLS(target) }
