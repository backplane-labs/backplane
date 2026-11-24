# Metrics

Every service on the SDK exports these through OpenTelemetry (OTLP, the
`OTEL_EXPORTER_OTLP_*` environment). Labels are bounded sets: names from the
manifest, outcomes, states, never an identifier, a URL or personal data. The
resource carries `service.name`, `service.version`, `service.instance.id` and
`deployment.environment.name`.

## SDK, every service

| Name | Type | Labels | What |
|---|---|---|---|
| `backplane.dependency.ready` | gauge | `node` | 1 while the dependency is provided |
| `backplane.dependency.provide.attempts` | counter | `node`, `outcome` | Attempts to provide a dependency |
| `backplane.config.updates` | counter | `outcome` | Live configuration updates applied or refused |
| `backplane.config.degraded` | gauge | — | 1 while the Consul layer is stale |
| `backplane.transport.connected` | gauge | `transport` (`consul`) | 1 while the transport is connected |
| `backplane.nats.connected` | gauge | `conn` | 1 while an `infra/nats` connection is up |
| `backplane.panics` | counter | `where` | Recovered panics |
| `backplane.postgres.acquire.timeouts` | counter | `pool` | Queries that found no free connection in time |

Instrumented libraries, with their own names:

| Source | Metrics | Labels |
|---|---|---|
| Go runtime and host (OpenTelemetry contrib `runtime`, `host`) | `go.goroutine.count`, `go.memory.used`, `go.memory.limit`, `go.memory.allocated`, `go.memory.allocations`, `go.memory.gc.goal`, `go.config.gogc`, `go.schedule.duration`, `go.processor.limit`, `process.cpu.*`, `system.*` | — |
| HTTP servers (`otelhttp`) | `http.server.request.duration`, `http.server.request.body.size`, `http.server.response.body.size` | `http.request.method`, `http.response.status_code`, `server.address`, `network.protocol.version` |
| gRPC servers and mesh clients (`otelgrpc`) | `rpc.server.duration`, `rpc.client.duration` and sizes | `rpc.service`, `rpc.method`, `rpc.grpc.status_code` |
| Temporal client and workers (`contrib/opentelemetry` metrics handler) | `temporal_request`, `temporal_request_failure`, `temporal_request_latency` (and `temporal_long_request*` for long polls), `temporal_workflow_completed`, `temporal_workflow_failed`, `temporal_workflow_endtoend_latency`, `temporal_workflow_task_execution_latency`, `temporal_workflow_task_schedule_to_start_latency`, `temporal_activity_execution_latency`, `temporal_activity_execution_failed`, `temporal_activity_schedule_to_start_latency`, `temporal_worker_task_slots_available` | `namespace`, `task_queue`, `workflow_type`, `activity_type`, `operation` |
| Valkey (`valkeyotel`) | `valkey_dial_attempt`, `valkey_dial_success`, `valkey_dial_conns`, `valkey_command_errors`, `valkey_do_cache_hits`, `valkey_do_cache_miss` and the command latency | `pool` |
| PostgreSQL pools | `pgxpool.*` (acquire count and duration, connections idle, acquired, total, max) | `pool` (`<name>.listen` for the LISTEN pool) |

Schedule-to-start latency is the task queue backlog: the time a task waited
for a worker.

## Platform

| Name | Type | Labels | What |
|---|---|---|---|
| `backplane.config.revisions` | counter | `outcome` | Configuration revisions |
| `backplane.config.kv.rewrites`, `backplane.config.kv.failures` | counter | `reason` | Consul KV writes |
| `backplane.audit.ingest.records` | counter | `outcome` (`stored`, `duplicate`, `rejected`) | Application audit records ingested |
| `backplane.errors.ingest.records` | counter | `outcome` | Error records ingested |
| `backplane.errors.upstream.records` | counter | `outcome`, `reason` | Error records forwarded upstream |
| `backplane.errors.upstream.queue` | gauge | — | Error records waiting upstream |
