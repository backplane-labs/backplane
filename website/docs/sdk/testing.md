# Test a service and its integrations

Choose checks for the boundary you changed. Fast tests of payload validation and component behavior do not require every platform dependency. Delivery, failover and gateway routing need real infrastructure.

## Layers

| Layer | What to establish |
| --- | --- |
| Domain/unit | Validation, pure transformations, fallback decisions and idempotency behavior |
| SDK/component | Startup failure, optional dependency recovery, cancellation, probes and accepted/rejected live configuration |
| Contract | Generated clients, schemas, route documents and manifest consistency |
| Runtime integration | JetStream delivery, Temporal execution, cookie relay and configuration reconciliation |
| Deployment/browser | Real assets, deep links, CSP, theme, authentication and service registration |
| Multiple replicas | Shared consumers, concurrent reconciliation, crash/rejoin and endpoint retirement |

The `pkg/backplane/backplanetest` package and repository conformance tests provide reusable examples of SDK behavior. Keep test stores and durable consumer names isolated from a development installation.

## Component harness

`backplanetest.New(t, backplanetest.Name("hello"))` constructs a service tree with recording transports, without `Open`, listeners or Consul. Build your components under `h.Root()`, substitute providers with `deps.Static(value)` and call `h.Start()`. Cleanup stops the tree under a bounded shutdown budget.

| Helper | Use |
| --- | --- |
| `Config[C](t, vars)` | Load schema defaults and explicit environment-style values without reading process env/files/Consul |
| `Ready(h)` | Inspect readiness with required-dependency reasons |
| `Answer(h, hookRef, fn)` | Supply a hook answer |
| `Events(h, eventRef)` / `EventsWithMeta` | Inspect published payloads and IDs/keys/headers |
| `FailPublish(h, err)` | Exercise publication failure |
| `Unavailable(h)` / `Available(h)` | Exercise transport loss and recovery |
| `Activity[Req, Res](ctx, h, name, input)` | Invoke a declared activity once |
| `ReactConsumer(ctx, h, consumer, value)` | Deliver once to a pinned reactor |
| `SetLive(&field, value)` | Update a live field and notify its watchers |
| `SetSections(h, name, services)` | Publish section `name` to the tree's `sections.Follow` as Backplane would: `services` by service; empty is a known empty set; a repeat signals no change |
| `Offered(h, name)` | Section `name` as the tree offers it: the value a `sections.OfferLive` set last, else the manifest's; nil when none |
| `h.Manifest()` | Inspect declarations and declaration errors |
| `Stop(h, component)` | Kill one component mid-test (a projection, a consumer): its goroutines' contexts end wherever they are, its subtree's `OnStop` hooks run, the rest keeps running |
| `Find(h, name)` | A node of the tree by name or path (`sync/fanout`), for `Stop` or inspection, without the service exporting it |
| `h.Stop()` | Stop the whole tree now, as the cleanup would; a restarted instance is a new harness on the same state |

A chaos test kills and restarts on the same state:

```go
h := backplanetest.New(t)
p := projections.New(h.Root(), deps.Static(db))
h.Start()
// ... feed events until the projection is mid-batch
backplanetest.Stop(h, p)            // killed: its context ends, nothing else stops

h2 := backplanetest.New(t)          // the restart
projections.New(h2.Root(), deps.Static(db))
h2.Start()
// ... assert it catches up without loss or duplicates
```

An internal API handler is called directly as a console operator would call it:
`backplanetest.Operator(ctx, session)` makes `backplane.OperatorOf` report that
session, so the operator's branch of an audit is testable without the console.

```go
ctx := backplanetest.Operator(t.Context(), session)
_, err := admin.CloseRoom(ctx, req) // audits actor console:<session>
```

A handler served with `api.MTLS` is called as another service of the mesh
would call it: `backplanetest.Caller(ctx, backplane.Caller{Service: "sync"})`
makes `backplane.CallerOf` report that caller. Over bufconn, without mutual
TLS, set it from a server interceptor:

```go
srv := grpc.NewServer(grpc.UnaryInterceptor(
	func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(backplanetest.Caller(ctx, backplane.Caller{Service: "sync"}), req)
	}))
```

The harness does not simulate JetStream redelivery or DLQ semantics. Its activity call executes once; retries belong in runtime integration tests. For Temporal code, use `pkg/backplane/workflows/workflowtest.New(t, h)`, which supplies a Temporal test environment with the service's registrations. This testing extension intentionally imports Temporal separately from core component tests.

## Check declarations without starting

Declaration mistakes — a prefix served twice, `api.MTLS` and plain routes on
one port, `api.MTLS` without a mesh, a gRPC service registered twice — fail
`Run`, so a service with them restarts in a loop. `svc.Validate()` reports
them without listening on anything. Keep the declarations in a function
`main` calls and test it:

```go
func TestDeclare(t *testing.T) {
    svc, err := backplane.Open(t.Context(), newState, backplane.ConfigOptions(config.WithoutFile()))
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = svc.Close() })

    declare(svc) // the same function main calls before Run

    if err := svc.Validate(); err != nil {
        t.Fatal(err)
    }
}
```

## Repository commands

```bash
GOWORK=off make test
# Start infrastructure only for the integration targets you need:
make up
make conformance
make test-m1
make test-m2
make test-replicas
```

Run live targets sequentially without manually running examples that register the same service names. `make test-replicas` uses two Backplane processes against a scratch database and exercises shared rule delivery, configuration, bound events, crash/rejoin and Nexus retirement with retained manifests.

For frontend contracts use `make web-check` or the narrower package/unit/browser target relevant to the edit. `yarn test:packed` installs package tarballs in fresh consumers, so workspace aliases cannot hide missing exports or packaged files.

## Full verification and releases

`make verify` is an explicitly requested full-stack check with an isolated Compose project. Normal CI selects checks from changed paths. `make release` performs no local builds or tests; the release workflow requires successful commit CI and packages the release.

Do not report a skipped integration test as a passed live check. Record which dependencies were present and whether the result proves a local contract, a backend integration or an end-to-end user operation. See [development reference](../reference/development.md).
