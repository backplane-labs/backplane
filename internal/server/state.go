package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/internal/apperrors"
	"github.com/backplane-labs/backplane/internal/apperrors/upstream"
	"github.com/backplane-labs/backplane/internal/audit"
	"github.com/backplane-labs/backplane/internal/config"
	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/obs"
	"github.com/backplane-labs/backplane/internal/otlp"
	"github.com/backplane-labs/backplane/internal/platform"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// State is the server's tree under the Root, in start order: the store,
// the registry, then the components built on them. Components stop in
// reverse, before what they were given.
type State struct {
	// Consul is backplane's client (the SDK's own is not shared): the
	// registry reads through it, configuration writes KV through it.
	Consul *api.Client
	// Store is PostgreSQL: required — the start waits for it (migrated),
	// readiness follows its ping.
	Store deps.Dependency[*store.Store]
	// Registry is the installation as Consul sees it; everything else reads
	// its snapshots (registry.Source).
	Registry *registry.Registry
	// Config keeps Live-value overrides: revisions in PostgreSQL, delivery
	// and reconciliation to Consul KV config/, the console's ConfigService.
	Config *config.Manager
	// Console is the console's HTTP on its own listener: auth, /ws
	// (backplane's own API and the relay to the services' internal API),
	// plugin bundles.
	Console *console.Console
	// Obs reads deployment-owned stores; no dependency on their readiness.
	Obs         *obs.Service
	internalObs bool
	// Errors keeps application errors durably and serves ErrorService.
	Errors *apperrors.Service
	// Audit serves durable control history and optionally exports OTel Logs.
	Audit    *audit.Service
	Platform *platform.Service
	// AuditIngest is the OTLP audit listener: application audit the
	// Collector forwards, stored for AuditService.
	AuditIngest *audit.Ingest

	// Extension points — one field and one constructor line in NewState per
	// component, in the order they depend on each other:
	//
	//	Console *console.Server     // internal/console: HTTP, ws-proto, relay
}

// NewState builds the server's tree. Every component gets what it needs by
// argument: cfg sections, st.Consul, st.Store, st.Registry (as a
// registry.Source).
func NewState(root backplane.Root[Config]) (*State, error) {
	cfg := root.Config()

	// The services' platform ports are dialed as backplane.mesh.platform says.
	consoleOptions := []console.Option{console.WithPlatform(root)}

	if cfg.Console.AssetsDir != "" {
		shell := os.DirFS(cfg.Console.AssetsDir)
		if _, err := fs.Stat(shell, "index.html"); err != nil {
			return nil, fmt.Errorf("console assets: %w", err)
		}

		consoleOptions = append(consoleOptions, console.WithShell(shell))
	}

	if cfg.OTLP.URL != "" {
		admission, err := otlp.New(cfg.OTLP)
		if err != nil {
			return nil, fmt.Errorf("OTLP admission: %w", err)
		}

		consoleOptions = append(consoleOptions,
			console.WithTelemetry(admission), console.WithSessionTelemetry(admission.SessionLogs()))

		root.OnStop(func(context.Context) error { admission.Close(); return nil })
	}

	client, err := consulClient(cfg.Consul)
	if err != nil {
		return nil, err
	}

	st := &State{Consul: client, internalObs: cfg.InternalSecret.Reveal() != ""}

	st.Store = deps.NewDependency(root, store.New(cfg.PG, store.AuditExport(cfg.Audit.ExportURL != "")))
	st.Registry = registry.New(root, client)

	auditOptions := []audit.Option{audit.WithRetention(cfg.Audit.Retention)}
	if cfg.Audit.ExportURL != "" {
		auditOptions = append(auditOptions, audit.WithPublisher(audit.Exporter(cfg.Audit)))
	}

	st.Audit = audit.New(root, st.Store, auditOptions...)

	st.Obs, err = obs.New(cfg.Obs, obs.WithRegistry(st.Registry))
	if err != nil {
		return nil, fmt.Errorf("observability: %w", err)
	}

	root.OnStop(func(context.Context) error { st.Obs.Close(); return nil })

	st.Errors = apperrors.New(root, st.Store, cfg.Errors, apperrors.WithRelated(st.Obs.Errors()),
		apperrors.WithAuthor(sessionAuthor), errorsUpstream(root, cfg.Errors.Upstream))
	st.AuditIngest = audit.NewIngest(root, st.Store, cfg.Audit, audit.WithErrors(st.Errors))

	st.Config = config.New(root, cfg.LiveConfig, client, st.Store, st.Registry, config.Author(sessionAuthor))

	st.Platform = platform.New(root, root.Identity().Instance, capabilities(cfg), st.probes(root, cfg))
	consoleOptions = append(consoleOptions,
		console.WithServices(st.Config.Register), console.WithServices(st.Obs.Register),
		console.WithServices(st.Errors.Register), console.WithServices(st.Audit.Register),
		console.WithServices(st.Platform.Register),
	)
	st.Console = console.New(root, cfg.consoleSettings(), console.NewPG(st.Store),
		console.NewPGAttempts(st.Store, console.DefaultLoginLimits()), st.Registry, consoleOptions...)

	return st, nil
}

// Ready is the server's readiness beyond its required dependencies (which
// are readiness already): every component that must be up before traffic.
// A component with a readiness of its own adds its line here.
func (st *State) Ready(context.Context) error {
	var errs []error

	if err := st.Registry.Ready(); err != nil {
		errs = append(errs, fmt.Errorf("registry: %w", err))
	}

	return errors.Join(errs...)
}

// sessionAuthor names the author of a configuration revision: the console
// session that made the call, "admin" when there is none.
func sessionAuthor(ctx context.Context) string {
	if id, ok := console.SessionID(ctx); ok {
		return "console:" + id.String()
	}

	return "admin"
}

// errorsUpstream forwards accepted errors to errors.upstream.url, over the
// mesh's mutual TLS when errors.upstream.service names the receiver (the
// installation's own service: no proxy); without a URL (an air-gapped
// installation) no forwarder runs.
func errorsUpstream(root backplane.Root[Config], settings upstream.Settings) apperrors.Option {
	if settings.URL == "" {
		return apperrors.WithUpstream(upstream.None{})
	}

	var opts []upstream.Option
	if settings.Service != "" {
		opts = append(opts, upstream.WithTransport(&http.Transport{TLSClientConfig: root.ServiceTLS(settings.Service)}))
	}

	return apperrors.WithUpstream(upstream.New(root, settings, opts...))
}
