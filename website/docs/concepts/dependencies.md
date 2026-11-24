# Dependencies and capability modes

| Dependency | Platform use |
| --- | --- |
| PostgreSQL | Required: durable configuration, sessions, login protection, audit and errors |
| Consul | Required: discover instances and module endpoints; distribute live configuration |
| NATS / Temporal | Optional: probe JetStream availability / Temporal frontend health |
| Collector | Optional: OTLP admission and audit export |
| Telemetry stores | Optional: query the signals configured for this deployment |

`PlatformService.GetCapabilities` describes configuration, not health. `GetInfrastructure` reads the last completed check on the answering replica. The platform does not provision a stream, consumer, worker, schedule or Nexus endpoint.

## SDK import boundaries

Import `pkg/backplane/drivers/consul` for discovery and live configuration. `pkg/backplane/infra/{postgres,nats,temporal,valkey}` provide native clients as lifecycle dependencies; importing the SDK alone does not import those clients. Use `deps.NewDependency` for a required resource and `deps.NewOptional` when the service can start without it.
