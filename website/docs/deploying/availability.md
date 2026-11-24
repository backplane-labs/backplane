# Replicas, outages and retirement

Backplane replicas share PostgreSQL and Consul. Optional runtimes must address the same intended installation resources. Keep configuration and secrets consistent across replicas and advertise reachable service addresses.

## Concurrent control work

Database transactions and revision checks protect configuration and definition updates. Reconciliation is designed to run on multiple replicas. Audit outbox workers lease records with PostgreSQL `SKIP LOCKED`; a crashed worker's lease can be reclaimed. Export is at least once, so downstream consumers use stable audit IDs for deduplication.


## What an outage means

| Failure | Expected operational consequence |
| --- | --- |
| PostgreSQL | Control persistence and audit unavailable; mutations cannot safely proceed |
| Consul | Discovery/configuration reconciliation impaired; do not infer service absence from a failed read |
| Temporal | The configured Temporal check fails; catalog and configuration still work |
| NATS | The configured JetStream check fails; catalog and configuration still work |
| Victoria store | Queries for that signal fail; platform startup does not depend on contacting it |
| Collector/export | Telemetry delivery delayed/failed according to exporter queues; durable audit remains in PostgreSQL |

Infrastructure UI reports OK/NO/disabled for configured probes. It does not certify the whole path from application emission to stored query result.

## Departed services

The instance registration expires through Consul. Version manifests persist. No Backplane reconciler deletes product-owned resources in NATS or Temporal.

## Rollout and recovery checklist

1. Back up PostgreSQL and persistent runtime data using their native procedures.
2. Keep service/workflow contracts compatible with in-flight and scheduled work.
3. Roll replicas while observing registration, readiness and configuration application.
4. Inspect unknown external-command audit outcomes after interruptions.
5. Verify one end-to-end operation per enabled runtime after recovery.

`make test-replicas` exercises two real Backplane processes, configuration repair, audit delivery and crash/rejoin in the reference topology. It is evidence for those scenarios, not a substitute for testing your load balancer, database failover or storage recovery.
