# Deploy an installation

Backplane runs as an ordinary server process or container. It discovers services and reconciles platform state; deployment tooling starts processes, configures credentials and operates infrastructure.

## Minimal and full modes

The minimal server needs PostgreSQL, Consul and Valkey. It provides catalog, configuration, console/module hosting and durable audit. Add optional endpoints to enable events, workflows, routing and observability. The [dependency matrix](../concepts/dependencies.md) maps each capability to its runtime.

The repository includes:

| File | Purpose |
| --- | --- |
| `docker-compose.yaml` | Base development infrastructure with optional profiles |
| `docker-compose.minimal.yaml` | Minimal console without Temporal, NATS, gateway or telemetry |
| `docker-compose.dev.yaml` | Built Backplane, hello, formatter and seeded development wiring |
| `docker-compose.observability.yaml` | Independent Collector and Victoria stores |
| `deployments/otel-collector.yaml` and audit overlay | Telemetry routing and trusted audit forwarding |

These are development/reference configurations. Replace local passwords, unsecured endpoints and host mappings for your environment. Do not expose administrative infrastructure ports through a public ingress.

## Deployment sequence

1. Provision persistent PostgreSQL, Consul and Valkey with backups and appropriate access controls.
2. Choose optional runtimes and create their deployment resources.
3. Configure Backplane identity, endpoints, operator credential and internal secret.
4. Mount the matching console assets, or use the release image that includes them.
5. Route the console through HTTPS with WebSocket upgrades and the configured prefix.
6. Start services with reachable advertised addresses and matching integration settings.
7. Verify login, catalog/health, configuration application and one operation for each enabled capability.
8. Verify backup/restore and rolling behavior before relying on the installation operationally.

## Assets and paths

Release assets are built for `/backplane/`. `BACKPLANE_CONSOLE_PREFIX` must match. To use another base, rebuild the frontend with `BACKPLANE_CONSOLE_BASE` and configure the backend consistently. Module and schema assets are served through protected routes beneath this prefix.

See [container instructions](docker.md), [security](security.md), [availability](availability.md) and the exhaustive [configuration reference](../reference/configuration.md).
