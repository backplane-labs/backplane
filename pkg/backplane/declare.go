package backplane

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/gopherex/xprobe/pkg/probe"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/api"
	"github.com/gopherex/backplane/pkg/backplane/internal/health"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
)

//nolint:gochecknoinits // installs the private accessor for the wsproto package
func init() {
	link.MountWS = func(svc any, prefix string, h http.Handler, services []string, spec routes.Managed) {
		m, ok := svc.(interface {
			mountWS(prefix string, h http.Handler, services []string, spec routes.Managed)
		})
		if !ok {
			panic("backplane: wsproto.Serve needs a *backplane.Service")
		}

		m.mountWS(prefix, h, services, spec)
	}
}

// Declaration errors, reported by Run.
var (
	errPrefixTaken    = errors.New("prefix served twice")
	errConsolePackage = errors.New("must be in proto package")
	errSchemaConflict = errors.New("api: inline and file API schemas declared together")
)

// API describes an API the author serves on their own listener.
func (c *core) API(d api.Decl) {
	c.declare("API", func() {
		r, err := link.APIs.Decl(d)
		if err != nil {
			c.env.Manifest.Fail(err)

			return
		}

		c.env.Manifest.API(r)
		c.mountSchemaFiles(link.APIs.Files(d))
	})
}

// GRPC serves gRPC services on a public port: one route per registered
// service, descriptors derived from the registration. Registering the same
// service twice on one port is a Run error; so is grpc.health.v1, which
// every public gRPC server serves already. api.Interceptors and
// api.StreamInterceptors wrap this registration's services only.
func (c *core) GRPC(register func(r grpc.ServiceRegistrar), opts ...api.GRPCOption) {
	spec := link.APIs.GRPC(opts)

	c.declare("gRPC routes", func() {
		p := c.port(spec.Listen)
		srv := c.grpcServer(p)

		services, err := manifest.Register(srv, register)
		if err != nil {
			c.env.Manifest.Fail(err)
		}

		p.dispatch.Add(services, spec.Unary, spec.Stream)

		if spec.Reflection && !p.reflection {
			reflection.Register(srv)

			p.reflection = true
		}

		for _, name := range services {
			r := &backplanev1.API{Kind: backplanev1.ApiKind_API_KIND_GRPC, Port: uint32(p.port), Services: []string{name}}
			setMatch(r, spec.Host, "/"+name+"/")
			c.env.Manifest.API(r)
		}
	})
}

// HTTP serves h under prefix on a public port. The prefix is normalized to
// end with "/" ("/api" serves "/api/..."); with api.Host it matches that
// host only, so one prefix may serve several hosts. The same host and
// prefix twice on one port is a Run error.
func (c *core) HTTP(prefix string, h http.Handler, opts ...api.HTTPOption) {
	c.mountHTTP("HTTP route", backplanev1.ApiKind_API_KIND_HTTP, prefix, h, link.APIs.HTTP(opts), nil)
}

// GraphQL serves the author's GraphQL handler under prefix on a public
// port, announced with its introspection result (JSON; nil when none).
func (c *core) GraphQL(prefix string, h http.Handler, introspection []byte, opts ...api.HTTPOption) {
	var schema *backplanev1.API
	if introspection != nil {
		schema = &backplanev1.API{Schema: &backplanev1.API_Graphql{Graphql: introspection}}
	}

	c.mountHTTP("GraphQL route", backplanev1.ApiKind_API_KIND_GRAPHQL, prefix, h, link.APIs.HTTP(opts), schema)
}

func (c *core) mountWS(prefix string, h http.Handler, services []string, spec routes.Managed) {
	c.mountHTTP("ws-proto route", backplanev1.ApiKind_API_KIND_WS_PROTO, prefix, h, spec,
		&backplanev1.API{Services: services})
}

// mountHTTP serves h and announces it; extra carries the kind's schema and
// services.
func (c *core) mountHTTP(
	what string, kind backplanev1.ApiKind, prefix string, h http.Handler, spec routes.Managed,
	extra *backplanev1.API,
) {
	c.declare(what, func() {
		p, err := routes.Prefix(prefix)
		if err != nil {
			c.env.Manifest.Fail(err)

			return
		}

		port := c.port(spec.Listen)
		if !port.handle(spec.Host, p, spec.Wrap(h)) {
			c.env.Manifest.Fail(fmt.Errorf("%s %s%s: %w on %s", what, spec.Host, p, errPrefixTaken, port.addr))

			return
		}

		r := &backplanev1.API{Kind: kind, Port: uint32(port.port)}
		setMatch(r, spec.Host, p)

		if spec.OpenAPI != nil {
			r.Schema = &backplanev1.API_Openapi{Openapi: spec.OpenAPI}
		}

		if spec.SchemaFiles != nil {
			if extra.GetSchema() != nil {
				c.env.Manifest.Fail(errSchemaConflict)
				return
			}

			r.Schema = &backplanev1.API_Bundle{Bundle: spec.SchemaFiles.Ref}
			c.mountSchemaFiles(spec.SchemaFiles)
		}

		if extra != nil {
			r.Services = extra.GetServices()
			if extra.GetSchema() != nil {
				r.Schema = extra.GetSchema()
			}
		}

		c.env.Manifest.API(r)
	})
}

// setMatch matches r by path prefix and, when given, host too.
func setMatch(r *backplanev1.API, host, prefix string) {
	r.Prefix, r.Host = prefix, host
}

// Internal registers the internal API: gRPC on the platform port for the
// service's own console plugin, behind the internal secret, served only
// while the author's tree is up. Services must live in the proto package
// <service>.console.v1.
func (c *core) Internal(register func(r grpc.ServiceRegistrar)) {
	c.declare("internal API", func() {
		services, err := manifest.Register(c.internal, register)
		if err != nil {
			c.env.Manifest.Fail(err)
		}

		pkg := strings.ReplaceAll(c.id.Service, "-", "_") + ".console.v1."
		for _, name := range services {
			if !strings.HasPrefix(name, pkg) {
				c.env.Manifest.Fail(fmt.Errorf("internal API %s: %w %s", name, errConsolePackage, strings.TrimSuffix(pkg, ".")))
			}
		}

		c.env.Manifest.Internal(services)
	})
}

// UI declares the console plugin bundle, served at /_backplane/ui/ behind the
// internal secret. Every file carries ETag = the bundle's ui.hash and
// Cache-Control: no-cache, so a cache revalidates and gets 304 until the
// bundle changes.
func (c *core) UI(bundle fs.FS) {
	c.declare("UI", func() {
		c.env.Manifest.UI(bundle)

		// An unreadable bundle is already a declaration error of the
		// manifest: Run fails before anything listens.
		hash, _ := manifest.UIHash(bundle)
		c.platform.Handle(uiPath, http.StripPrefix(uiPath, uiHandler(bundle, hash)))
	})
}

// uiHandler serves bundle with validators: the file server answers
// If-None-Match with 304 against the ETag set here, and drops both headers
// from its error responses.
func uiHandler(bundle fs.FS, hash string) http.Handler {
	files := http.FileServerFS(bundle)
	etag := `"` + hash + `"`

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")

		if hash != "" {
			w.Header().Set("ETag", etag)
		}

		files.ServeHTTP(w, r)
	})
}

// mountSchemaFiles reuses the platform port's internal authentication and ETags.
func (c *core) mountSchemaFiles(bundle *routes.SchemaFiles) {
	if bundle == nil {
		return
	}

	if bundle.Err != nil {
		c.env.Manifest.Fail(bundle.Err)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.apiBundles == nil {
		c.apiBundles = map[string]bool{}
	}

	hash := bundle.Ref.GetHash()
	if c.apiBundles[hash] {
		return
	}

	c.apiBundles[hash] = true
	prefix := "/_backplane/api/" + hash + "/"
	c.platform.Handle(prefix, http.StripPrefix(prefix, uiHandler(bundle.Files, hash)))
}

// LivenessProbe adds a liveness probe. Keep dependencies out of it: a dead
// database is no reason to restart the process.
func (c *core) LivenessProbe(p probe.Probe) {
	c.declare("liveness probe", func() { c.health.Add(health.Live, p) })
}

// ReadinessProbe adds a readiness probe; readiness drives grpc.health.v1
// and so the Consul check. Required dependencies are readiness probes
// already.
func (c *core) ReadinessProbe(p probe.Probe) {
	c.declare("readiness probe", func() { c.health.Add(health.Ready, p) })
}

// StartupProbe adds a startup probe.
func (c *core) StartupProbe(p probe.Probe) {
	c.declare("startup probe", func() { c.health.Add(health.Startup, p) })
}

// publicPort is one managed public listener: gRPC and HTTP share it.
type publicPort struct {
	addr       string
	port       uint16
	grpc       *grpc.Server
	dispatch   *routes.Dispatch // per-registration interceptors of grpc
	reflection bool
	http       *http.ServeMux
	prefixes   map[string]*routes.Hosts
}

// port returns the public port for listen ("" = the default public port).
func (c *core) port(listen string) *publicPort {
	if listen == "" {
		listen = listenAddr(c.cfg.PublicPort)
	}

	for _, p := range c.public {
		if p.addr == listen {
			return p
		}
	}

	p := &publicPort{addr: listen, prefixes: map[string]*routes.Hosts{}}

	n, err := portOf(listen)
	if err != nil {
		c.env.Manifest.Fail(err)
	}

	p.port = n
	c.public = append(c.public, p)

	return p
}

// primaryPort is registered in Consul: the default public port when used,
// else the first declared one, else 0.
func (c *core) primaryPort() uint16 {
	for _, p := range c.public {
		if p.addr == listenAddr(c.cfg.PublicPort) {
			return p.port
		}
	}

	if len(c.public) > 0 {
		return c.public[0].port
	}

	return 0
}

func (p *publicPort) mux() *http.ServeMux {
	if p.http == nil {
		p.http = http.NewServeMux()
	}

	return p.http
}

// handle serves h for host and prefix; false when they are taken.
func (p *publicPort) handle(host, prefix string, h http.Handler) bool {
	hosts, ok := p.prefixes[prefix]
	if !ok {
		hosts = routes.NewHosts()
		p.prefixes[prefix] = hosts
		p.mux().Handle(prefix, hosts)
	}

	return hosts.Add(host, h)
}

func portOf(listen string) (uint16, error) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("listen %q: %w", listen, err)
	}

	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("listen %q: %w", listen, err)
	}

	return uint16(n), nil
}
