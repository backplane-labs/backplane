# Deploying services and backplane

Backplane manages platform contracts, configuration, hooks, events, workflows,
audit and the console. External routing is entirely deployment-owned. Neither the
server nor SDK configures a gateway or supplies xDS/EDS.

## A service on the SDK

Build with identity stamped by `make hello`, `make formatter` or your application's
build. Set `BACKPLANE_ADVERTISE` to an address reachable by the Consul agent and
Backplane. In Kubernetes use `POD_IP`; in Compose use routable container addresses.
The platform port (`9400` by default) serves health checks, internal module RPCs,
UI bundles and schema files. Keep it private. Managed APIs use the public port
(`8080` by default) or their own `api.Listen` address.

The SDK registers the primary public port in Consul Catalog and checks readiness
through the platform port. Manifest versions and ephemeral instance state are stored
in Consul KV. This registration does not publish the application externally.
Every start, a one-off run included, publishes its version's manifest; the first
one is kept. The manifest carries the API routes with their ports and the node
tree of the configured drivers, so a start with another configuration under the
same version stops with `different manifest already published for this version`,
naming the fields that differ (`apis`, `nodes`, ...). Start it with the service's
configuration, or publish the build under a new version.
If your deployment registers services itself, set `BACKPLANE_CONSUL_REGISTER=false`.

### SDK block: every variable

Durations are Go durations (`30s`, `2m`); lists are JSON (`["a","b"]`), and an
empty list or map variable counts as unset; TLS material is PEM content, not paths.
A variable that does not parse fails the start naming the variable, not its value. Secrets are masked in logs and in
the instance state.

| variable | default | meaning |
|---|---|---|
| `BACKPLANE_CONSUL_ADDR` | — | Consul agent; empty: no Consul (no registration, no KV layer) |
| `BACKPLANE_CONSUL_TOKEN` | — | ACL token (secret); else `CONSUL_HTTP_TOKEN(_FILE)` |
| `BACKPLANE_CONSUL_DATACENTER` | agent's | datacenter of queries |
| `BACKPLANE_CONSUL_TLS_ENABLED` | `false` | HTTPS to Consul; replaces the `CONSUL_*` TLS variables entirely |
| `BACKPLANE_CONSUL_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME` | — | CA (empty: system pool), client certificate and key (together), server name |
| `BACKPLANE_CONSUL_TLS_INSECURE_SKIP_VERIFY` | `false` | development only |
| `BACKPLANE_CONSUL_REGISTER` | `true` | `false` when the deployment registers the service (consul-k8s, Nomad) |
| `BACKPLANE_CONSUL_TAGS` | — | tags of the catalog registration, JSON list |
| `BACKPLANE_CONSUL_CHECK_INTERVAL`, `_CHECK_TIMEOUT`, `_DEREGISTER_AFTER` | `10s`, `5s`, `1m` | gRPC health check of the registration |
| `BACKPLANE_CONSUL_SESSION_TTL` | `30s` | session of the instance state (clamped to 10s..24h) |
| `BACKPLANE_NATS_URL` | — | NATS; empty: no events |
| `BACKPLANE_NATS_CREDS` | — | content of a `.creds` file (secret) |
| `BACKPLANE_NATS_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME`, `_TLS_INSECURE_SKIP_VERIFY` | `false`, … | TLS to NATS, as for Consul |
| `BACKPLANE_NATS_PUBLISH_TIMEOUT` | `5s` | bound of a publish whose context has no deadline |
| `BACKPLANE_NATS_MAX_AGE`, `_MAX_BYTES` | `168h`, `0` | retention of the service's event stream `bp_<service>`; `0`: unlimited |
| `BACKPLANE_NATS_REPLICAS` | `1` | replicas of the streams the process creates: its events, its dead letters and a missing stream it subscribes to (1..5) |
| `BACKPLANE_NATS_DEDUP_WINDOW` | `2m` | dedup window of the event stream, at most `max_age` |
| `BACKPLANE_NATS_DLQ_MAX_AGE` | `720h` | retention of dead letters `bp_dlq_<service>`; `0`: unlimited |
| `BACKPLANE_TEMPORAL_ADDR` | — | Temporal frontend; empty: no hooks, activities, workflows |
| `BACKPLANE_TEMPORAL_NS` | `default` | namespace |
| `BACKPLANE_TEMPORAL_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME`, `_TLS_INSECURE_SKIP_VERIFY` | `false`, … | TLS (mTLS) to Temporal, as for Consul |
| `BACKPLANE_TEMPORAL_API_KEY` | — | Temporal Cloud API key (secret); turns TLS on by itself |
| `BACKPLANE_TEMPORAL_DIAL_TIMEOUT` | `2s` | one connection attempt; later ones run in the background |
| `BACKPLANE_TEMPORAL_HOOK_TIMEOUT` | `30s` | deadline of a hook call nothing else bounds |
| `BACKPLANE_TEMPORAL_WORKER_ENABLED` | `true` | `false`: this replica runs no activity/workflow worker (hook calls still work) |
| `BACKPLANE_TEMPORAL_WORKER_MAX_CONCURRENT_ACTIVITIES`, `_MAX_CONCURRENT_WORKFLOW_TASKS`, `_ACTIVITY_POLLERS`, `_WORKFLOW_POLLERS` | `0` | worker tuning; `0`: Temporal's default |
| `BACKPLANE_SHUTDOWN_TIMEOUT` | `25s` | whole stop budget |
| `BACKPLANE_SHUTDOWN_DRAIN` | `3s` | pause between deregistration and closing the listeners |
| `BACKPLANE_SHUTDOWN_LISTENERS` | `10s` | in-flight requests of the listeners and the internal API |
| `BACKPLANE_SHUTDOWN_RESERVE` | `5s` | kept for components, dependencies, telemetry; `drain + listeners + reserve <= timeout` |
| `BACKPLANE_HEALTH_INTERVAL`, `_HEALTH_TIMEOUT` | `5s`, `3s` | probe evaluation; `timeout <= interval` |
| `BACKPLANE_SERVER_READ_HEADER_TIMEOUT`, `_IDLE_TIMEOUT`, `_MAX_HEADER_BYTES` | `10s`, `2m`, `1048576` | HTTP limits of both port classes |
| `BACKPLANE_SERVER_GRPC_MAX_RECV_MSG_SIZE` | `4194304` | gRPC message limit |
| `BACKPLANE_SERVER_GRPC_KEEPALIVE_MIN_TIME`, `_GRPC_PERMIT_WITHOUT_STREAM` | `30s`, `true` | gRPC keepalive enforcement  |
| `BACKPLANE_SERVER_GRPC_KEEPALIVE_TIME`, `_GRPC_KEEPALIVE_TIMEOUT` | `2h`, `20s` | gRPC server pings a quiet connection and closes it without an answer; shorten for long streams behind NAT or load balancers |
| `BACKPLANE_SERVER_GRPC_MAX_CONNECTION_IDLE`, `_AGE`, `_AGE_GRACE` | `0s` | gRPC GOAWAY to idle / old connections and the grace of their streams; `0s`: never |
| `BACKPLANE_SERVER_GRPC_MAX_SEND_MSG_SIZE`, `_GRPC_MAX_CONCURRENT_STREAMS` | `0` | gRPC send limit and streams per connection; `0`: unlimited |
| `BACKPLANE_INSTANCE` | `<service>-<hostname>` | instance id = Consul service id |
| `BACKPLANE_ADVERTISE` | `POD_IP`, else hostname's IP | address registered in Consul |
| `BACKPLANE_ENVIRONMENT` | — | `deployment.environment.name` |
| `BACKPLANE_TELEMETRY_METRICS_INTERVAL`, `_TIMEOUT` | `0s` | metric export timing; `0s`: `OTEL_METRIC_EXPORT_INTERVAL` / `_TIMEOUT`, else `30s` / `10s` |
| `BACKPLANE_TELEMETRY_TRACES_SAMPLER`, `_RATIO` | `always_on`, `1` | trace sampler: `always_on` records every span whatever the parent decided (sampling is the collector's); `always_off`, `traceidratio`, `parentbased_always_on`, `parentbased_always_off`, `parentbased_traceidratio`; `OTEL_TRACES_SAMPLER` does not apply |
| `BACKPLANE_TELEMETRY_METRICS_HISTOGRAMS` | — | histogram rules, a JSON list, first match wins ([histograms](../website/docs/guides/telemetry.md#metric-export-and-histograms)) |
| `BACKPLANE_MESH_DEFAULT` | `allow` | mutual TLS between services (`api.MTLS`): what a caller no Consul intention names gets, `allow` or `deny` |
| `BACKPLANE_INTERNAL_PORT` | `9400` | platform port |
| `BACKPLANE_INTERNAL_SECRET` | — | secret backplane presents on the platform port (secret); empty: the internal API and bundle are open to whoever reaches the port |
| `BACKPLANE_INTERNAL_SECRET_PREVIOUS` | — | also accepted while the secret rotates |
| `BACKPLANE_PUBLIC_PORT` | `8080` | shared public port of managed APIs |
| `BACKPLANE_LOG_LEVEL` | `info` | log level; live from Consul KV `config/<service>/backplane/log_level` |
| `BACKPLANE_PPROF` | `false` | `/debug/pprof/*` on the platform port, behind the secret |
| `BACKPLANE_CONFIG_FILE` | — | configuration file (not a block field) |

The standard Consul client variables (`CONSUL_HTTP_TOKEN`,
`CONSUL_HTTP_TOKEN_FILE`, `CONSUL_HTTP_SSL`, `CONSUL_CACERT`,
`CONSUL_CAPATH`, `CONSUL_CLIENT_CERT`, `CONSUL_CLIENT_KEY`,
`CONSUL_TLS_SERVER_NAME`, `CONSUL_HTTP_SSL_VERIFY`) fill what the block
leaves empty; only `BACKPLANE_CONSUL_ADDR` turns Consul on.

## Platform-in-a-box

PostgreSQL and Consul are the required server dependencies. NATS and Temporal
are optional; observability has a separate overlay. `make up` starts infrastructure
for host processes. `make dev` starts infrastructure, Backplane, hello, formatter
and a seed that configures a cross-service binding and event rule.

| Service | Local port | Role |
|---|---|---|
| PostgreSQL | 5433 | Durable platform state |
| Consul | 8500 | Catalog, health, manifests and live configuration |
| NATS | 4222 | Optional event transport |
| Temporal | 7233 | Optional workflows and hook execution |
| Console | 8081 | `make dev`: `/backplane/` |
| Hello | 8080 | `make dev`: public APIs |
| Hello own listener | 8083 | `make dev`: `/legacy/` |
| Formatter | 8082 | `make dev`: `/formatter/` |

Development application ports bind to loopback. There is no gateway profile or
control-plane listener. Production exposure and upstream discovery belong to the deployment.

## The backplane server

| variable | default | meaning |
|---|---|---|
| `BACKPLANE_PG_DSN` | — | **required**: PostgreSQL (secret); everything lives in schema `backplane`, created and migrated at start (the role needs `CREATE` on the database) |
| `BACKPLANE_PG_QUERY_LOG` | `warn` | lowest level of the per-query log (`trace`…`error`, `none`); arguments are never logged |
| `BACKPLANE_PG_POOL_MAX_CONNS`, `_MIN_CONNS`, `_MAX_CONN_LIFETIME`, `_MAX_CONN_IDLE_TIME` | `0` (pgx's default) | pool bounds; set `MAX_CONNS` explicitly in production — pgx's default follows the node's CPUs |
| `BACKPLANE_CONSUL_ADDR` | — | **required** for backplane (optional for services): the registry follows the catalog and `backplane/services/` |
| `BACKPLANE_CONSOLE_LISTEN` | `:8081` | console HTTP (`/`, `/ws`, `/auth`, `/plugins`), directly or behind a deployment-owned proxy |
| `BACKPLANE_CONSOLE_ORIGINS` | the request's own host | origins (`scheme://host[:port]`, exact) allowed to open `/ws` and to log in, JSON list |
| `BACKPLANE_CONSOLE_TRUSTED_PROXIES` | — | addresses/CIDRs whose `X-Forwarded-For` is trusted (trusted proxy addresses), JSON list |
| `BACKPLANE_CONSOLE_INSECURE_COOKIE` | `false` | `true`: the session cookie without `Secure` and no HSTS — plain-HTTP development only (`make run-backplane` sets it) |
| `BACKPLANE_ADMIN_TOKEN` | — | **required**: the console's admin token (secret), at least 16 characters |
| `BACKPLANE_OBS_METRICS_URL`, `_LOGS_URL`, `_TRACES_URL` | — | observability backends for the console |
| `BACKPLANE_WIRING_RUNTIME` | `auto` | runtime of bindings and rules that name none: `temporal`, `direct`, or `auto` (temporal when Temporal is configured) |
| `BACKPLANE_WIRING_DIR` | — | directory of bindings and rules kept in force; the console only reads what it defines ([wiring files](../website/docs/guides/wiring.md#keep-wiring-in-files)) |
| `BACKPLANE_WIRING_POLL` | `5s` | how often `BACKPLANE_WIRING_DIR` is read again; it is also read on every new manifest |

Listen ports must differ from each other and from the platform and public
ports. Readiness (`/healthz/readiness` on the platform port) waits for
PostgreSQL (migrated, pinging) and the first registry snapshot from Consul;
later Consul outages keep the last snapshot and readiness.

Locally, against platform-in-a-box, next to services on their default
ports:

```sh
make up
make run-backplane   # bin/backplane: Consul localhost:8500, PostgreSQL localhost:5433,
                     # platform port 9410, public port 8090, console admin token
                     # dev-admin-token-change-me (DEV_ADMIN_TOKEN=... to override)
make run-hello       # another terminal: the registry logs hello's manifest and instance
curl localhost:9410/healthz/readiness
```

## Console and module delivery

The console has its own HTTP listener, default `:8081`. Set
`BACKPLANE_CONSOLE_PREFIX=/backplane` to mount all paths under that prefix.
The shell, `/auth/*`, `/ws`, `/plugins/*`, `/schemas/*` and optional telemetry admission
use this listener. Module RPCs and files are fetched directly from service platform ports.

Set `BACKPLANE_ADMIN_TOKEN` to a secret of at least 16 characters. Sessions are stored
in PostgreSQL; brute-force counters are shared in PostgreSQL. For local plain HTTP only,
set `BACKPLANE_CONSOLE_INSECURE_COOKIE=true`. HTTPS deployments keep secure cookies
and terminate TLS before the application's listener.

A reverse proxy must preserve the prefix and support WebSocket upgrades. Configure
`BACKPLANE_CONSOLE_ORIGINS` for explicit allowed browser origins when needed, and
`BACKPLANE_CONSOLE_TRUSTED_PROXIES` for trusted forwarded client addresses. The platform
never chooses a public hostname or merges routing configuration with deployment files.

## Local development

```sh
make dev
curl --fail 'http://127.0.0.1:8080/hello/?name=Developer'
curl --fail 'http://127.0.0.1:8082/formatter/'
# Open http://127.0.0.1:8081/backplane/
make test-dev
make dev-down
```

`DEV_ADMIN_TOKEN` controls the local console credential (default
`dev-admin-token-change-me`). `make dev-ui` and `make dev-module` are independent
frontend fixtures. For console HMR use `cd web && yarn dev:console`; the default
upstream is the direct console on `8081`. `BACKPLANE_DEV_TARGET` overrides that console
upstream, and `BACKPLANE_HELLO_URL` overrides the public hello address in browser acceptance.

`make dev-minimal` starts Backplane with PostgreSQL and Consul only. Disabled
capabilities return explicit unavailable errors and the UI reflects their status.

## Conformance and multiple replicas

Stop the development application processes before host-process conformance: suites
own the reference service names in Consul. They allocate their own listener ports;
no external proxy is required.

```sh
make up
make test-m1       # configuration, revisions, reconciliation, relay, bundles, restart
make test-m2       # bindings, rules, deduplication, trace context
make test-replicas # two backplanes, crash/rejoin, cross-service calls, Nexus cleanup
make test-audit
make test-otlp     # start the observability overlay first
```

`make test` runs race-enabled Go tests; infrastructure suites skip without their
`BACKPLANE_TEST_*` variables. `make verify` explicitly runs the complete live,
frontend, package and minimal-installation verification. Ordinary push CI selects
checks by changed inputs and reuses cached unchanged Go test results.

## Server capabilities and infrastructure health

NATS enables events; Temporal enables workflows, schedules, binding execution and
hook/activity calls; rules need both. Storage drivers and the Collector are configured
independently. `PlatformService` reports capabilities and infrastructure status.
An optional `BACKPLANE_INFRASTRUCTURE_GATEWAY_URL` is only a deployment-supplied HTTP
health check, independent of any proxy vendor; it does not enable routing control.
Collector and storage health overrides follow the same model.

The SDK emits OTel telemetry to the configured Collector. Querying metrics, logs and
traces, audit persistence and OTLP admission do not require an external gateway.
See [telemetry admission](../docs/telemetry-admission.md), [audit](../docs/audit-api.md)
and [observability](../docs/obs-api.md) for their configuration and API contracts.

## Files here

- `Dockerfile` and `Dockerfile.release`: console and server containers.
- `otel-collector.yaml` and `otel-collector.audit.yaml`: independent telemetry pipelines.
- The root Compose files: base infrastructure, full development and minimal installation.
- [Release image instructions](README.release.md).

## Sharing a PostgreSQL database

Backplane owns the fixed `backplane` schema. All application tables and queries
are schema-qualified, every platform connection uses `search_path=backplane`,
and the migration ledger is `backplane.sqld_migrations`. A service can use
`public` or its own schema in the same database without sharing migration state.
Use separate roles and grant the platform access only to its schema where
appropriate. Backplane creates its schema on first startup and applies pending
migrations automatically; it does not create its database or move service data.

Login attempt windows and the shared failure backoff also live in this schema.
The default is five attempts per minute per IP; after ten failed token checks,
all replicas apply a shared exponential delay from one second up to one minute.
A successful login resets the failure count. PostgreSQL time controls expiry.
Expired IP counters are cleaned in bounded batches every minute. If the store
cannot check the protection state, login returns `503` rather than bypassing it.
