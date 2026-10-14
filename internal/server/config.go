// Package server assembles the backplane server: its configuration and the
// State the SDK builds from it. backplane is a service on its own SDK
// (§12.2): the SDK block (BACKPLANE_*, §4.3) plus the sections here, whose
// environment names are BACKPLANE_<PATH> too — the service is named
// "backplane", so its prefix and the block's coincide, and no section here
// shares a name with a block field.
package server

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/apperrors"
	"github.com/backplane-labs/backplane/internal/audit"
	liveconfig "github.com/backplane-labs/backplane/internal/config"
	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/obs"
	"github.com/backplane-labs/backplane/internal/otlp"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

// Name of the service.
const Name = "backplane"

// Config of the backplane server.
type Config struct {
	config.Backplane `json:"backplane"`

	// PostgreSQL, schema `backplane`: BACKPLANE_PG_DSN, _QUERY_LOG, _POOL_*.
	PG             postgres.Config `json:"pg"`
	Infrastructure Infrastructure  `json:"infrastructure"`
	// Console HTTP: `/`, `/ws`, `/auth`, `/plugins`, served directly or behind a deployment-owned proxy.
	Console Console `json:"console"`
	// Admin token of the console (§11.3): the secret /auth/login accepts,
	// set in the deployment; required, at least 16 characters. Changing it
	// is changing the secret and rolling the replicas; sessions opened with
	// the old one last until they expire or are revoked.
	AdminToken config.Secret `json:"admin_token,omitempty"`
	// Read-only access to independent observability backends (§15.2).
	Obs obs.Config `json:"obs"`
	// Durable control audit settings belong to deployment.
	Audit audit.Settings `json:"audit"`
	// Durable application errors: BACKPLANE_ERRORS_*.
	Errors apperrors.Settings `json:"errors"`
	// OTLP admission into a deployment-owned Collector; empty URL disables it.
	OTLP otlp.Config `json:"otlp"`
	// Live-value overrides: BACKPLANE_LIVE_CONFIG_* (§5.3).
	LiveConfig liveconfig.Settings `json:"live_config"`
	// Nexus endpoint reconciliation and retirement.
	Nexus Nexus `json:"nexus"`
	// Wiring: the runtime of bindings and rules that name none.
	Wiring Wiring `json:"wiring"`
	// Direct runtime: runs in backplane's process.
	Direct Direct `json:"direct"`
}

// Wiring of the installation: BACKPLANE_WIRING_RUNTIME.
type Wiring struct {
	// Runtime of a binding or rule that names none: "temporal", "direct",
	// or "auto": temporal when Temporal is configured, else direct.
	Runtime string `json:"runtime" schemapb:"default=auto"`
}

// Resolve is the runtime "auto" stands for.
func (w Wiring) Resolve(temporal bool) consolev1.Runtime {
	switch w.Runtime {
	case "temporal":
		return consolev1.Runtime_RUNTIME_TEMPORAL
	case "direct":
		return consolev1.Runtime_RUNTIME_DIRECT
	}

	if temporal {
		return consolev1.Runtime_RUNTIME_TEMPORAL
	}

	return consolev1.Runtime_RUNTIME_DIRECT
}

// Direct runtime settings: BACKPLANE_DIRECT_*.
type Direct struct {
	// RunTimeout bounds a rule's run (a hook call is bounded by its deadline).
	RunTimeout time.Duration `json:"run_timeout" schemapb:"default=5m;gt=0"`
	// Retention of recorded runs; 0 keeps them.
	Retention time.Duration `json:"retention" schemapb:"default=168h;gte=0"`
}

// Console of the installation (§11): BACKPLANE_CONSOLE_*.
type Console struct {
	// AssetsDir serves a built console shell from a deployment-owned directory.
	AssetsDir string `json:"assets_dir,omitempty"`
	Listen    string `json:"listen"               schemapb:"default=:8081"`
	// Prefix mounts the console under a path such as /backplane.
	Prefix string `json:"prefix,omitempty"`
	// Origins allowed to open /ws (https://console.example.com); empty:
	// the console's own host.
	Origins []string `json:"origins,omitempty"`
	// Proxies whose X-Forwarded-For is trusted: addresses or CIDRs.
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	// InsecureCookie drops Secure from the session cookie (and HSTS):
	// plain-HTTP development only.
	InsecureCookie bool `json:"insecure_cookie" schemapb:"default=false"`
}

// Settings of the console component.
func (c Config) consoleSettings() console.Settings {
	return console.Settings{
		Listen: c.Console.Listen, Prefix: c.Console.Prefix,
		Origins: c.Console.Origins, TrustedProxies: c.Console.TrustedProxies, InsecureCookie: c.Console.InsecureCookie,
		AdminToken: c.AdminToken, InternalSecret: c.InternalSecret,
	}
}

// Errors of Validate.
var (
	ErrNoPG         = errors.New("pg.dsn is required (BACKPLANE_PG_DSN)")
	ErrNoConsul     = errors.New("backplane.consul.addr is required (BACKPLANE_CONSUL_ADDR)")
	ErrAddr         = errors.New("want host:port with a port in 1..65535")
	ErrPortTaken    = errors.New("port is used twice")
	ErrPrefix       = errors.New("console.prefix: want /path without a trailing slash")
	ErrOrigin       = errors.New("console.origins: want scheme://host[:port]")
	ErrProxy        = errors.New("console.trusted_proxies: want an address or CIDR")
	ErrURL          = obs.ErrURL
	ErrNoAdminToken = errors.New("admin_token is required (BACKPLANE_ADMIN_TOKEN): " +
		"the console's login secret, set in the deployment")
	ErrShortAdminToken = fmt.Errorf("want at least %d characters", console.MinAdminTokenLen)
	ErrWiringRuntime   = errors.New("want auto, temporal or direct")
	ErrWiringTemporal  = errors.New("wiring.runtime is temporal but temporal is not configured (BACKPLANE_TEMPORAL_ADDR)")
)

// Validate checks what the schema cannot: required connections, listen
// addresses and their ports, the console's placement and lists.
func (c Config) Validate() error {
	var errs []error

	if !c.PG.Enabled() {
		errs = append(errs, ErrNoPG)
	} else if err := c.PG.Validate(); err != nil {
		errs = append(errs, err)
	}

	if !c.Consul.Enabled() {
		errs = append(errs, ErrNoConsul)
	}

	if err := c.adminToken(); err != nil {
		errs = append(errs, err)
	}

	switch c.Wiring.Runtime {
	case "", "auto", "direct":
	case "temporal":
		if !c.Temporal.Enabled() {
			errs = append(errs, ErrWiringTemporal)
		}
	default:
		errs = append(errs, fmt.Errorf("wiring.runtime %q: %w", c.Wiring.Runtime, ErrWiringRuntime))
	}

	errs = append(errs, c.ports()...)
	errs = append(errs, c.Console.validate()...)
	errs = append(errs, c.OTLP.Validate(), c.Obs.Validate(), c.Audit.Validate(), c.Infrastructure.Validate())

	return errors.Join(errs...)
}

// adminToken: the console's admin token is set and long enough.
func (c Config) adminToken() error {
	switch token := c.AdminToken.Reveal(); {
	case token == "":
		return ErrNoAdminToken
	case len(token) < console.MinAdminTokenLen:
		return fmt.Errorf("admin_token (BACKPLANE_ADMIN_TOKEN): %w", ErrShortAdminToken)
	}

	return nil
}

// ports: the listen addresses parse, and no two listeners of the process
// share a port.
func (c Config) ports() []error {
	var errs []error

	used := map[int]string{
		int(c.InternalPort): "backplane.internal_port",
		int(c.PublicPort):   "backplane.public_port",
	}

	for _, l := range []struct{ name, addr string }{
		{"console.listen", c.Console.Listen}, {"audit.listen", c.Audit.Listen},
	} {
		port, err := listenPort(l.addr)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", l.name, l.addr, err))

			continue
		}

		if other, ok := used[port]; ok {
			errs = append(errs, fmt.Errorf("%s: %d is %s's: %w", l.name, port, other, ErrPortTaken))

			continue
		}

		used[port] = l.name
	}

	return errs
}

func (c Console) validate() []error {
	var errs []error

	if c.Prefix != "" && (!strings.HasPrefix(c.Prefix, "/") || strings.HasSuffix(c.Prefix, "/")) {
		errs = append(errs, fmt.Errorf("%q: %w", c.Prefix, ErrPrefix))
	}

	for _, o := range c.Origins {
		if u, err := url.Parse(o); err != nil || !httpURL(o) || (u.Path != "" && u.Path != "/") {
			errs = append(errs, fmt.Errorf("%q: %w", o, ErrOrigin))
		}
	}

	for _, p := range c.TrustedProxies {
		if _, err := netip.ParsePrefix(p); err == nil {
			continue
		}

		if _, err := netip.ParseAddr(p); err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", p, ErrProxy))
		}
	}

	return errs
}

// listenPort is the port of a listen address (":8081", "0.0.0.0:8081").
func listenPort(addr string) (int, error) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, ErrAddr
	}

	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return 0, ErrAddr
	}

	return port, nil
}

func httpURL(s string) bool {
	u, err := url.Parse(s)

	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// Nexus controls cleanup of endpoints whose services have no instances.
type Nexus struct {
	AbsenceGrace      time.Duration `json:"absence_grace"      schemapb:"default=5m;gte=0"`
	ReconcileInterval time.Duration `json:"reconcile_interval" schemapb:"default=1m;gt=0"`
}
