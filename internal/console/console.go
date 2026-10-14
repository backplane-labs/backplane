// Package console is backplane's console server (§11): the HTTP the shell
// talks to directly or through a deployment-owned reverse proxy.
//
//	POST /auth/login     {token} → session cookie (HttpOnly, SameSite=Strict)
//	POST /auth/logout    ends the session
//	GET  /auth/session   the current session, 401 without one
//	GET  /ws             ws-proto: backplane's own API (CatalogService,
//	                     SessionService, and what WithServices adds) and the
//	                     relay of everything else to the services' internal
//	                     API on their platform ports
//	GET  /plugins/<service>/<hash>/<path>
//	                     console plugin bundles, fetched from a live instance
//	                     and cached by content hash
//	GET  /               the shell
//
// The console listens on its own address (Settings.Listen) from the start
// of its node to its stop, under Settings.Prefix (or at "/"). A reverse
// proxy must preserve this path and support WebSocket upgrades.
package console

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// Session lifetime (§11.3).
const (
	// SessionTTL is the absolute lifetime of a session.
	SessionTTL = 12 * time.Hour
	// IdleTimeout ends a session without activity (a login, a request with
	// its cookie, an RPC on its connection).
	IdleTimeout = time.Hour

	// touchEvery bounds how often activity is written to the store.
	touchEvery = time.Minute
	// recheckEvery: an open connection re-reads its session this often and
	// closes once it is revoked (on another replica), expired or idle.
	recheckEvery = 30 * time.Second
	// cleanupEvery deletes stale sessions from the store.
	cleanupEvery        = 10 * time.Minute
	attemptCleanupEvery = time.Minute
	// Limits of the console's HTTP server.
	readHeaderTimeout = 10 * time.Second
	serverIdleTimeout = 2 * time.Minute
	maxHeaderBytes    = 1 << 20
	// defaultCacheBytes caps the plugin bundle cache.
	defaultCacheBytes = 64 << 20
)

// Settings are the console's part of the server configuration.
type Settings struct {
	// Listen is the console's own listener (":8081"); empty: none (the
	// Handler is served by someone else, e.g. a test server).
	Listen string
	// Prefix the console is mounted under ("/backplane"); empty: "/".
	Prefix string
	// Origins allowed to open /ws and to log in (scheme://host[:port]);
	// empty: the request's own host.
	Origins []string
	// TrustedProxies whose X-Forwarded-For names the client (addresses,
	// CIDRs): deployment-owned proxy addresses.
	TrustedProxies []string
	// InsecureCookie drops Secure from the session cookie and HSTS from the
	// responses: plain-HTTP development only.
	InsecureCookie bool
	// AdminToken is what /auth/login accepts: configuration of the
	// deployment, at least MinAdminTokenLen characters.
	AdminToken config.Secret
	// InternalSecret is presented to the services' platform ports
	// (relay and bundles).
	InternalSecret config.Secret
}

// Validate checks the admin token: required, at least MinAdminTokenLen
// characters.
func (s Settings) Validate() error {
	switch token := s.AdminToken.Reveal(); {
	case token == "":
		return ErrNoAdminToken
	case len(token) < MinAdminTokenLen:
		return ErrShortAdminToken
	}

	return nil
}

// Option configures New.
type Option func(o *options)

type options struct {
	now              func() time.Time
	services         []func(grpc.ServiceRegistrar)
	cacheBytes       int64
	shell            fs.FS
	telemetry        http.Handler
	sessionTelemetry http.Handler
}

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithServices registers more of backplane's own API on /ws (another
// component's gRPC services, e.g. configuration).
func WithServices(register func(r grpc.ServiceRegistrar)) Option {
	return func(o *options) { o.services = append(o.services, register) }
}

// WithCacheBytes caps the plugin bundle cache (default 64 MiB).
func WithCacheBytes(n int64) Option { return func(o *options) { o.cacheBytes = n } }

// WithShell serves the console's frontend from fsys (index.html for every
// path that is not a file); without it "/" answers a placeholder.
func WithShell(fsys fs.FS) Option { return func(o *options) { o.shell = fsys } }

// WithTelemetry installs independent OTLP admission without operator auth.
func WithTelemetry(handler http.Handler) Option { return func(o *options) { o.telemetry = handler } }

// WithSessionTelemetry installs log admission behind console session and
// origin checks, without requiring a public ingest key in the frontend.
func WithSessionTelemetry(handler http.Handler) Option {
	return func(o *options) { o.sessionTelemetry = handler }
}

// Console is the console server: a component whose start checks the admin
// token is configured and opens the listener,
// whose goroutine deletes stale sessions and closes relay connections of
// gone instances, and whose stop shuts the listener down and closes the
// open /ws connections.
//
// A Console is shared by pointer: it holds the connections,
// the relay pool and the bundle cache.
type Console struct {
	deps.Component

	settings Settings
	base     string // "" or "/prefix"
	origins  []string
	proxies  proxies
	sessions Sessions
	src      registry.Source
	now      func() time.Time
	token    adminToken

	attempts Attempts
	conns    *connections
	relay    *relay
	bundles  *bundles
	handler  http.Handler

	mu  sync.Mutex
	srv *http.Server
	ln  net.Listener
}

// New creates the console under parent: sessions keep the sessions,
// attempts count logins across replicas, src is the installation. Its start fails when s does not pass Validate.
func New(
	parent deps.Scope, s Settings, sessions Sessions, attempts Attempts, src registry.Source, opts ...Option,
) *Console {
	o := options{now: time.Now, cacheBytes: defaultCacheBytes}
	for _, opt := range opts {
		opt(&o)
	}

	c := &Console{
		Component: deps.NewComponent(parent, "console"),
		settings:  s,
		base:      strings.TrimSuffix(s.Prefix, "/"),
		proxies:   parseProxies(s.TrustedProxies),
		sessions:  sessions,
		src:       src,
		now:       o.now,
		token:     newAdminToken(s.AdminToken.Reveal()),
		attempts:  attempts,
		conns:     newConnections(),
	}

	for _, origin := range s.Origins {
		c.origins = append(c.origins, strings.ToLower(strings.TrimSuffix(origin, "/")))
	}

	c.relay = newRelay(src, s.InternalSecret)
	c.bundles = newBundles(s.InternalSecret, o.cacheBytes)
	c.handler = c.routes(c.newWS(o.services), o.shell, o.telemetry, o.sessionTelemetry)

	c.OnStart(func(context.Context) error { return s.Validate() })
	c.OnStart(c.listen)
	c.OnStop(c.shutdown)
	c.Go(c.housekeeping)

	return c
}

// Handler serves the console under Route (the prefix included).
func (c *Console) Handler() http.Handler { return c.handler }

// Route is where the console is mounted: "/" or "<prefix>/".
func (c *Console) Route() string { return c.base + "/" }

// Addr is the listener's address once started; nil without one.
func (c *Console) Addr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ln == nil {
		return nil
	}

	return c.ln.Addr()
}

// listen opens Settings.Listen and serves the console on it.
func (c *Console) listen(ctx context.Context) error {
	if c.settings.Listen == "" {
		return nil
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", c.settings.Listen)
	if err != nil {
		return fmt.Errorf("console: listen: %w", err)
	}

	srv := &http.Server{
		Handler:           otelhttp.NewHandler(c.handler, "console"),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	c.mu.Lock()
	c.srv, c.ln = srv, ln
	c.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log().Error("console: serve", xlog.Err(err))
		}
	}()

	c.Log().Info("console listening", xlog.String("addr", ln.Addr().String()), xlog.String("route", c.Route()))

	return nil
}

// shutdown stops accepting, closes the open /ws connections (hijacked:
// Shutdown does not see them) and waits for in-flight requests within ctx.
func (c *Console) shutdown(ctx context.Context) error {
	c.mu.Lock()
	srv := c.srv
	c.mu.Unlock()

	if srv == nil {
		return nil
	}

	c.conns.closeAll()

	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()

		return fmt.Errorf("console: shutdown: %w", err)
	}

	return nil
}

// housekeeping deletes stale sessions and drops relay connections to
// instances that left, until the node stops.
func (c *Console) housekeeping(ctx context.Context) error {
	defer c.relay.close()

	changes := c.src.Changes(ctx)
	cleanup := time.NewTicker(cleanupEvery)

	attemptCleanup := time.NewTicker(attemptCleanupEvery)
	defer attemptCleanup.Stop()

	defer cleanup.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-changes:
			if !ok {
				return nil
			}

			c.relay.prune(c.src.Current())
		case <-attemptCleanup.C:
			if cleaner, ok := c.attempts.(attemptsPruner); ok {
				if err := cleaner.Prune(ctx); err != nil && ctx.Err() == nil {
					c.Log().Warn("console: delete expired login attempts", xlog.Err(err))
				}
			}
		case <-cleanup.C:
			now := c.now()
			if _, err := c.sessions.DeleteStale(ctx, now, now.Add(-IdleTimeout)); err != nil && ctx.Err() == nil {
				c.Log().Warn("console: delete stale sessions", xlog.Err(err))
			}
		}
	}
}

// alive reports whether s is usable at now.
func alive(s Session, now time.Time) bool {
	return now.Before(s.ExpiresAt) && now.Sub(s.LastSeenAt) < IdleTimeout
}
