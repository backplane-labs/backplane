# Replicas, outages and retirement

Backplane replicas share PostgreSQL and Consul. Optional runtimes must address the same intended installation resources. Keep configuration and secrets consistent across replicas and advertise reachable service addresses.

## Concurrent control work

Database transactions and revision checks protect configuration and definition updates. Reconciliation is designed to run on multiple replicas. Audit outbox workers lease records with PostgreSQL `SKIP LOCKED`; a crashed worker's lease can be reclaimed. Export is at least once, so downstream consumers use stable audit IDs for deduplication.

Rules share durable delivery/execution identities. A duplicate event or replica rejoin must not create an independent business execution merely because another server processed it. Business activities still need their own idempotency for retried external effects.

## What an outage means

| Failure | Expected operational consequence |
| --- | --- |
| PostgreSQL | Control persistence and audit unavailable; mutations cannot safely proceed |
| Consul | Discovery/configuration reconciliation impaired; do not infer service absence from a failed read |
| Temporal | Durable workflow/hook/schedule operations fail; core capabilities remain independently configured |
| NATS | Event delivery/operations impaired; no implication that a prior publish was rolled back |
| Victoria store | Queries for that signal fail; platform startup does not depend on contacting it |
| Collector/export | Telemetry delivery delayed/failed according to exporter queues; durable audit remains in PostgreSQL |

Infrastructure UI reports OK/NO/disabled for configured probes. It does not certify the whole path from application emission to stored query result.

## Retire departed services

Owned Nexus endpoints are checked on reconciliation. Retirement requires confirmed service absence, not merely failing health checks. The normal grace period is five minutes, with reconciliation on a one-minute interval. A Consul read failure defers deletion. Ownership/version checks prevent a stale replica from deleting a replacement endpoint.

Historical manifests remain available. Returning services can recreate their endpoint. This is resource cleanup, not a catalog-history purge or a deployment action.

## Rollout and recovery checklist

1. Back up PostgreSQL and persistent runtime data using their native procedures.
2. Keep service/workflow contracts compatible with in-flight and scheduled work.
3. Roll replicas while observing registration, readiness and configuration application.
4. Inspect unknown external-command audit outcomes after interruptions.
5. Verify one end-to-end operation per enabled runtime after recovery.

`make test-replicas` exercises two real Backplane processes, shared rules, configuration, crash/rejoin and endpoint retirement in the reference topology. It is evidence for those scenarios, not a substitute for testing your load balancer, database failover or storage recovery.
