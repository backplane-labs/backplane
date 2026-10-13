# hello + formatter

`hello` demonstrates the SDK author API; `formatter` is an independently built
service with its own payload types. Neither service imports the other. The
installation connects them through the public console API:

```text
GET /hello/?name=ann
  -> hello.Greet hook -> binding -> formatter.Format
  -> response "Welcome, ann!"
  -> hello.Greeted {name, count, text}
     -> hello audit reactor
     -> formatter reactor (its own copy of the event schema)
     -> rule -> formatter.Record
```

## Run locally

From the repository root, start the infrastructure with `make up`. Then run
these commands in separate terminals:

```sh
make run-backplane
make run-hello
make run-formatter
```

All three use local Consul, NATS and Temporal. Public ports are backplane 8090,
hello 8080, formatter 8082, hello legacy 8083; console 8081; platform ports 9410, 9400, 9420.
The legacy port is declared in the versioned manifest and must stay stable
across restarts (`HELLO_LEGACY_LISTEN`).
Once both services appear healthy in the catalog:

```sh
make setup-example
curl 'http://localhost:8080/hello/?name=ann'
curl 'http://localhost:8082/formatter/'
```

Setup logs in with `BACKPLANE_ADMIN_TOKEN` (default `DEV_ADMIN_TOKEN` from
Makefile), opens the cookie-authenticated `/ws` and saves the binding and rule.
It reuses the named rule on repeated execution; definitions receive new
versions. `BACKPLANE_DEMO_URL` overrides the default `http://localhost:8081`,
including a console prefix if one is configured. The setup source and DSL live
in `examples/demo`; the same setup runs in conformance.

Without a binding, hello renders locally. Without infrastructure, its local
fallback still works. Bound HTTP greetings and bound `Welcome` workflow calls
record the returned text and emit `Greeted`, just like local greetings. The
rule excludes the name `skip`; the independent reactor still observes it.
Publication failure is logged and does not fail the greeting. `Idempotency-Key`
deduplicates the hook execution, not HTTP requests or their greeting events.

## SDK features and where to see them

| Capability | Example |
| --- | --- |
| Required/optional dependencies, probes, retry, shutdown | hello `internal/store`, `cmd/hello` |
| Lifecycle, goroutines, singleton, telemetry | hello `internal/greeter` |
| Live config, validation, watch | `greeter.suffix`, `greeter.excited`; `formatter.prefix`, `formatter.suffix` |
| Native gRPC, streaming, ws-proto | hello `HelloService`, `Countdown` |
| HTTP, GraphQL, route policies, separate listener | hello `internal/web`, `internal/legacy` |
| Internal console API and plugin bundle delivery | hello `internal/admin`, `ui` |
| Hook Call / workflows.CallHook, idempotency key | hello `Greet`, HTTP handler / `Welcome` |
| Activity, workflow-backed activity, execution metadata | hello `Echo`, `Welcome`; formatter `Format`, `Record` |
| Workflows available for administrative runs and schedules | hello `GreetMany`, `Report`; SDK creates no schedules |
| Events, reactors, delivery metadata, deduplication | hello `Greeted`, audit; formatter independent reactor |
| Cross-service CEL binding and event rule | `examples/demo` |
| Invalid event / DLQ / redrive | publish `hello.Greeted` with an empty name: both reactors reject it terminally; console EventService exposes DLQ and targeted redrive |

Formatter's public `/formatter/` endpoint reports `formatted`, `recorded`,
`observed` and `last_text`. Its example counters and deduplication window (4096
keys) are process-local; persistent business data belongs in a real store.
The UI bundle runs in the console Module Federation host with the shared client,
router, theme and translations. Authors can run the same pages independently;
see [module template](../web/templates/module/README.md).

## Verification

```sh
make test
make conformance
make test-m1
make test-m2
make test-replicas
```

Run live targets sequentially with no manually running example processes.
M1/M2/replicas use real binaries built with the race detector. `test-replicas`
starts two backplanes against one scratch database, plus hello and formatter.
It checks shared rule consumers, one workflow per event even after duplicate
publication, configuration reconciliation, bound greeting events, both reactor
and rule delivery, crash and rejoin of one backplane, and endpoint retirement
with retained manifests followed by service return. Test-owned processes,
streams, Consul keys, endpoint and database are cleaned up.
