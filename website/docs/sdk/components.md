# Components, dependencies and lifecycle

The service state is an explicit tree under `backplane.Root[Config]`. Components inherit a named logger, tracer and meter. Providers acquire resources; dependency wrappers determine startup and readiness behavior.

```go
type Greeter struct {
    deps.Component
    db deps.Dependency[*store.DB]
}

func New(parent deps.Scope, db deps.Dependency[*store.DB]) *Greeter {
    g := &Greeter{
        Component: deps.NewComponent(parent, "greeter"),
        db: db,
    }
    g.Go(g.report)
    return g
}
```

This fragment assumes your `store.DB` and `report(context.Context) error`. Background functions should stop when their context is canceled. Use the component's lifecycle instead of launching untracked goroutines.

## Choose the correct wrapper

| Wrapper | Startup and use |
| --- | --- |
| `deps.NewDependency` | Required resource; startup waits while acquisition retries, readiness follows the resource probe |
| `deps.NewOptional` | Startup can continue without the resource; acquisition keeps retrying |
| `deps.NewSingleton` | Lazily acquired on first use, released during shutdown; not automatically required for readiness |

The hello state constructs a required store and optional cache:

```go
Store: deps.NewDependency(root, store.New(&cfg.Store), deps.ProbeTimeout(time.Second)),
Cache: deps.NewOptional(root, store.New(&cfg.Cache), deps.Name("cache"),
    deps.ProbeOptional(), deps.Backoff(time.Second, 10*time.Second)),
```

Optional absence must be handled by business logic. Do not dereference a missing optional value or silently convert a required dependency into an optional one to make readiness green.

## Probes

Readiness answers whether this instance can serve its declared work. Liveness answers whether the process can still recover on its own. A process is never alive but useless:

- A failure a dependency may recover from (a connection reconnecting, a database down) fails readiness only. Traffic stops and the process waits.
- A failure it can never recover from fails liveness, so the orchestrator restarts the process. A provider's `Probe` reports it by wrapping `deps.ErrTerminal`, for example a client that closed for good. From then on the dependency fails liveness. This holds for a required dependency, and for an optional one probed with `deps.ProbeOptional`; an optional dependency without it is never probed.
- The SDK's own NATS and Temporal connections follow the same rule. They reconnect forever, so only a NATS connection that closed without `Close` fails liveness.

The `pkg/backplane/infra` providers reconnect for ever, so only a closed NATS connection is terminal:

| Provider | Reconnect | Terminal |
| --- | --- | --- |
| `infra/nats` | for ever, `ReconnectWait` with jitter, also after repeated authorization errors | the connection is closed |
| `infra/postgres` | the pgx pool dials per acquire | none |
| `infra/valkey` | the client reconnects by itself | none |
| `infra/temporal` | the gRPC connection reconnects with capped backoff | none |

Attach additional probes through `svc.ReadinessProbe` and `svc.LivenessProbe`; hello wraps errors with `probe.FromError`.

Return a useful error when not ready. The probe integration preserves a reason, allowing operators to distinguish startup, a failed resource and an application-specific rejection. Avoid credentials or payloads in those reasons.

## Telemetry and shutdown

Use `component.Log()`, `Tracer()` and `Meter()` so component identity remains attached. `Span` and `deps.SpanValue` wrap work with the component tracer; carry the provided context into downstream calls.

Resources should be released by their provider/lifecycle contract. The SDK uses the shared `xshutdown/lifecycle` machinery rather than a second service-specific lifecycle implementation. Verify that cancellation unblocks network calls, loops and dependency acquisition; a goroutine waiting forever can prevent clean shutdown.

The [hello store and greeter](https://github.com/backplane-labs/backplane/tree/master/examples/hello/internal) demonstrate providers, lazy templates, optional cache recovery, probes and a background reporting loop.
