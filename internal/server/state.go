package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/apperrors"
	"github.com/backplane-labs/backplane/internal/audit"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/config"
	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/executor"
	"github.com/backplane-labs/backplane/internal/obs"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/otlp"
	"github.com/backplane-labs/backplane/internal/platform"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/rules"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
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
	// Bindings keeps bindings (hook -> steps) and rules (event -> steps):
	// versions in PostgreSQL validated against the latest manifests, the
	// console's BindingService and RuleService.
	Bindings *bindings.Manager
	// Rules consumes events for the rules and starts their runs.
	Rules *rules.Engine
	// Ops is the console's operations on NATS and Temporal through the
	// SDK's own clients: events, streams, consumers, dead letters;
	// workflows, runs, schedules; hook and activity calls (their workflows
	// run on backplane's task queue).
	Ops *ops.Ops
	// Direct is the direct runtime: bindings and rules run in this process
	// without Temporal, activities called over the services' platform
	// ports; backplane's HookService; the console's run RPCs.
	Direct *direct.Service
	// Executor answers every hook over Temporal Nexus (an endpoint per
	// service with hooks, a Nexus worker on backplane's queue) and runs
	// bindings and rules as the workflow backplane.Binding.v1 there; the
	// console's run RPCs of BindingService.
	Executor *executor.Executor
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

	var consoleOptions []console.Option

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
	st.Ops = ops.New(root, st.Registry, ops.Author(sessionAuthor), ops.Namespace(cfg.Temporal.Namespace))

	auditOptions := []audit.Option{audit.WithRetention(cfg.Audit.Retention), audit.WithRunQueue(st.Ops.RunQueue)}
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
		apperrors.WithAuthor(sessionAuthor))
	st.AuditIngest = audit.NewIngest(root, st.Store, cfg.Audit, audit.WithErrors(st.Errors))

	st.Config = config.New(root, cfg.LiveConfig, client, st.Store, st.Registry, config.Author(sessionAuthor))

	st.Bindings = bindings.New(root, st.Store, st.Registry, bindings.Author(sessionAuthor),
		bindings.DefaultRuntime(cfg.Wiring.Resolve(cfg.Temporal.Enabled())))
	consoleOptions = append(consoleOptions, st.runtime(root, cfg, st.Errors.Register)...)
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

func (st *State) runtime(root backplane.Root[Config],
	cfg Config,
	registerErrors func(grpc.ServiceRegistrar),
) []console.Option {
	st.Direct = direct.New(root, st.Bindings, st.Registry, st.Store, cfg.InternalSecret.Reveal(),
		direct.Author(sessionAuthor), direct.RunTimeout(cfg.Direct.RunTimeout), direct.Retention(cfg.Direct.Retention))
	st.Ops.SetDirect(st.Direct)

	registerBindings := st.Direct.RegisterBindings(st.Bindings.BindingAPI(), st.Bindings, nil)
	registerRules := func(r grpc.ServiceRegistrar) { consolev1.RegisterRuleServiceServer(r, st.Bindings.RuleAPI()) }

	if cfg.Temporal.Enabled() {
		workflows.Register(root, ops.RegisterWorkflows)
		st.Executor = executor.New(root, st.Bindings, st.Registry, executor.Author(sessionAuthor),
			executor.Namespace(cfg.Temporal.Namespace), executor.Runs(st.Ops.Workflows()),
			executor.AbsenceGrace(cfg.Nexus.AbsenceGrace), executor.Resync(cfg.Nexus.ReconcileInterval),
			executor.WithDirect(st.Direct))
		workflows.Register(root, st.Executor.RegisterWorkflows)
		direct.WithTemporal(st.Executor)(st.Direct)

		registerBindings = st.Executor.Register(st.Bindings.BindingAPI())
	}

	if cfg.NATS.Enabled() {
		ruleOptions := []rules.Option{rules.WithDirect(st.Direct), rules.TemporalEnabled(cfg.Temporal.Enabled())}
		if cfg.Temporal.Enabled() {
			ruleOptions = append(ruleOptions, rules.WithRuns(st.Ops.Workflows()))
		}

		st.Rules = rules.New(root, st.Bindings, st.Registry, ruleOptions...)
		registerRules = st.Rules.Register
	}

	st.Platform = platform.New(root, root.Identity().Instance, capabilities(cfg), st.probes(root, cfg))
	registrations := []func(grpc.ServiceRegistrar){
		st.Config.Register,
		st.Audit.Commands(st.scheduleCommands(st.Ops.Register), sessionAuthor),
		st.Audit.Commands(registerBindings, sessionAuthor), st.Audit.Commands(registerRules, sessionAuthor),
		st.Bindings.Register, st.Obs.Register, registerErrors, st.Audit.Register, st.Platform.Register,
	}

	consoleOptions := make([]console.Option, 0, len(registrations))
	for _, register := range registrations {
		consoleOptions = append(consoleOptions, console.WithServices(platform.Gate(register, disabledMethods(cfg))))
	}

	return consoleOptions
}
