# OTLP proxy and independent telemetry stack

Backplane has two integration boundaries: an OTLP/HTTP proxy like Komeet's
gateway and an authenticated query API through storage drivers (Explore,
[Errors](errors.md)).
The Collector and telemetry stores are independent deployment-owned services.
They ingest, process and retain telemetry without Backplane; applications may
send directly to the Collector. Backplane does not manage their lifecycle.

The console listener can proxy OTLP/HTTP at
`<console-prefix>/telemetry/v1/logs`, `/v1/traces`, and `/v1/metrics`.
Set `BACKPLANE_OTLP_URL` to the Collector base URL to enable these routes.
An empty URL disables them. They require no operator session, registry lookup,
module declaration or IAM call. This is HTTP admission; OTLP/gRPC admission is
not implemented. The server process still has its ordinary startup dependencies.

The console itself sends logs to `<console-prefix>/auth/telemetry/v1/logs`.
This POST-only route requires a valid operator session and passes the console's
origin checks. It shares the public admission handler's rate, size, concurrency
and timeout budgets, but uses session authentication instead of an ingest key.
It accepts logs only, strips credentials before forwarding, and marks traffic
as `X-Backplane-Ingest: proxy` just like the public routes. The Vite console
proxies it with the other `/auth/` routes.

The proxy treats signal payloads as opaque bytes. It never parses or re-encodes
OTLP messages, reserves attribute names, injects storage metadata, duplicates
records or archives metric points. It marks every forwarded request with the
header `X-Backplane-Ingest: proxy` (replacing any the client sent): the
Collector's backplane pipeline drops audit records that came this way, so
browsers and public ingest keys cannot write audit
([audit-api.md](audit-api.md#application-audit)); exception records from the
proxy are stored as errors marked `ingest=proxy` ([errors.md](errors.md)).
Gzip is decoded only to enforce the body
budget; the uncompressed payload is forwarded unchanged. Protocol validation,
redaction, enrichment, batching and export belong to the external OTel pipeline.

Only POST and browser preflight OPTIONS are accepted. JSON and protobuf are
supported, with identity or gzip encoding. Client credentials, cookies,
forwarding headers, baggage and URL query parameters never reach the Collector.
Public resource attributes remain untrusted source data. The proxy does not
retry an uncertain export or follow Collector redirects. Collector responses,
including OTLP partial-success bodies and Retry-After, pass back to the sender.
HTTP acceptance is not a guarantee of durable storage.

## Deployment controls

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `BACKPLANE_OTLP_URL` | empty | Collector base URL; empty disables admission |
| `BACKPLANE_OTLP_BODY_BYTES` | 2097152 | Maximum compressed **and** decompressed request bytes |
| `BACKPLANE_OTLP_CONCURRENT` | 16 | Concurrent body reads/decompression/forwards per replica; no waiting queue |
| `BACKPLANE_OTLP_RATE_PER_MINUTE` | 300 | Per-IP token refill rate |
| `BACKPLANE_OTLP_BURST` | 120 | Per-IP bucket capacity |
| `BACKPLANE_OTLP_MAX_IPS` | 4096 | Maximum active IP buckets per replica |
| `BACKPLANE_OTLP_IDLE_TTL` | 10m | Idle bucket expiry; cannot be shorter than full bucket refill time |
| `BACKPLANE_OTLP_TIMEOUT` | 10s | Deadline covering body read and Collector response |
| `BACKPLANE_OTLP_TRUSTED_PROXIES` | empty | JSON array of CIDRs allowed in the forwarded-address chain |
| `BACKPLANE_OTLP_KEYS` | empty | Optional JSON array of ingest-only bearer keys, each at least 16 characters |

Rate exhaustion returns 429. Full concurrent-request or IP-table capacity returns
503. Both include Retry-After. Expired IP entries are reclaimed; new addresses
cannot evict active entries and reset their rate limits. Proxy chains are walked
from the right only through configured trusted networks. Excessive forwarded
headers are ignored. Response bodies from Collector are bounded to 64 KiB.
Memory for active request buffers scales with body size times concurrency,
including allocation overhead; the IP table is independently bounded.

Limits apply separately on each replica. Installation-wide budgets and network
connection limits belong at the deployment edge. Optional key rotation uses an
overlapping configured set and a rollout; no key-management API or UI exists.
Keys do not grant reads/admin access. A key shipped in a browser is public.

## Local pipeline and verification

`docker-compose.observability.yaml` starts a separate optional Collector/Victoria
stack. Use a dedicated Compose project for tests:

```sh
docker compose -p backplane-obs-check -f docker-compose.observability.yaml up -d
make test-otlp
docker compose -p backplane-obs-check -f docker-compose.observability.yaml down
```

The sample Collector uses a memory limiter before export and a container memory
limit. Its queues, asynchronous batching and exporter retries are disabled;
the original sender handles transient failures. Internal query/Collector ports
are published on loopback for development only. Storage retention is configured
in Compose and never exposed in platform UI.

Tests cover gzip expansion and chunked limits, slow body deadlines, IP spoofing,
IP-table bounds/expiry, concurrency exhaustion, cancellation, upstream timeout,
credential stripping and optional key rotation overlap. The live test sends
logs, metrics and a cross-service trace through the proxy and Collector, then
reads Victoria through its native query APIs. It also closes the proxy and sends
directly to the Collector, verifying that telemetry ingestion remains independent.
Opaque JSON/protobuf forwarding tests cover unknown fields, custom names and
Collector error/partial-success responses. The observability live suite also
reads stored signals through ObsService; see [read API acceptance](obs-api.md#acceptance).

## Storage and query boundary

Storage representation is owned by the selected telemetry backend. Backplane's
query driver exposes that backend's data and capabilities without introducing
another persistent representation or inferring types that the backend discarded.

The pinned VictoriaLogs maps structured log bodies to flattened string-valued
search fields. The live fixture verifies the decimal digits of int64
`9007199254740993`, nested search-field values and nanosecond timestamps; it does
not claim reconstruction of the original typed OTLP envelope. VictoriaTraces is
read through its trace API and VictoriaMetrics through its time-series API.
The platform UI and module APIs consume the driver's query results.

Queries require the operator cookie or the protected internal service API's
installation secret. Collector/storage endpoints and credentials are deployment
configuration and are not exposed to browser plugins. The query API remains a
separate implementation step; the proxy needs no storage credentials.
