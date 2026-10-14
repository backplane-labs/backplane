# Dependencies and capability modes

Distinguish a **service using the Go SDK** from the **Backplane server**. A standalone service can run without platform infrastructure. The server needs shared state and discovery even in its minimal mode.

| Dependency | Platform server | Enables |
| --- | --- | --- |
| PostgreSQL | Required | Configuration and wiring revisions, durable audit, sessions, shared login protection and control state |
| Consul | Required | Service discovery, health, manifests and configuration distribution |
| Temporal | Optional | Hooks/bindings, durable runs, workflow-backed activities and administrative schedules |
| NATS JetStream | Optional | Events, reactors and dead-letter operations |
| NATS + Temporal | Both needed for rules | Event-triggered durable wiring execution |
| Collector | Optional integration | OTLP forwarding, trusted application audit pipeline |
| Victoria stores | Optional per signal | Logs, metrics, traces and Errors queries |

## SDK import boundaries

The core author APIs do not import the Temporal, Nexus, NATS, Consul, PostgreSQL or Valkey SDKs. The repository remains one Go module: these libraries can still appear in `go.mod` even when a particular binary's import graph excludes them.

Use `drivers/standard` when a service intentionally supports all standard integrations. Use individual driver packages for a smaller compilation boundary. Leaving an endpoint empty disables its integration. Configuring an endpoint without registering its driver is an error rather than an implicit dependency.

## Capability discovery

`PlatformService` exposes deployment capabilities and infrastructure health. Capability means configured support; health is an endpoint check. Neither establishes that a complete business operation or telemetry pipeline works.

The console shows unavailable capabilities explicitly. It does not offer deployment controls for enabling a storage backend or changing retention. Those changes happen in configuration and deployment tooling.

## Choosing a mode

- Start with PostgreSQL and Consul when you need catalog, configuration, module hosting and audit.
- Add NATS for asynchronous events and reactors.
- Add Temporal for durable workflows and hook bindings.
- Add both for event rules.
- Add telemetry backends by signal as needed; absent telemetry does not prevent platform startup.

See [deployment configuration](../reference/configuration.md) for exact environment variables and Compose profiles, and [availability](../deploying/availability.md) for failure behavior.

Valkey remains an optional SDK integration for author services. It is not used by the platform server. The Compose `cache` profile is available for testing or services that explicitly need it.
