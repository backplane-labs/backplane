# Frontend package and integration contract

This document records the frontend package and integration contract. The
[English handbook](https://backplane-labs.github.io/backplane/) describes current
usage, deployment and implementation boundaries. Component coverage is tracked
in the [UI matrix](ui-components.md); the live console and module workflow are
documented in [development](development.md).

## Package boundaries

The `web/` Yarn workspace publishes only to GitHub Packages, under `@backplane-labs`.
All public `backplane-*` packages in a release have the same version. The major
version is the plugin `sdk_major`. Third-party packages remain ordinary package
dependencies; the public npm registry is not a publication target.

| Package suffix | Responsibility | Must not own |
| --- | --- | --- |
| `api` | Generated protobuf-es messages and ws-proto clients for all `backplane.console.v1` services and their imported types | React, login, connection lifecycle |
| `client` | Cookie sessions, one ws-proto connection, typed connection/session states and client factories | Rendering, module navigation |
| `react` | Client provider, `useClient`, subscriptions and cancellation tied to component lifetime | A second transport or router |
| `theme` | Both palettes, semantic tokens, typography, CSS and Grafana theme bridge | Business data |
| `ui` | Primitives, forms, tables, layout, feedback and accessible interaction | Platform network requests |
| `editors` | Lazy code, JSON, diff and query editors | Backend credentials |
| `charts` | Time series, stat, gauges, legends and visualization controls | Queries or storage-specific result parsing |
| `observability-ui` | Logs, attributes, trace waterfall, span details, stack traces, correlation links | Error grouping, incident state, error queries |
| `schema-forms` | schemapb forms, validation presentation and JSON fallback | Configuration writes |
| `platform-ui` | API-aware service, operation, audit and telemetry compositions | A second console shell |
| `plugin-sdk` | Typed plugin definition, relative routes, navigation registry and host context; `usePluginErrorReporter()` reports a module's handled errors to the installation's Errors, marked `backplane.module` | Service deployment |
| `errors` (`@backplane-labs/backplane-errors`) | Browser error capture for any frontend (the console, modules, applications) over OTLP logs; see [errors.md](errors.md) | Storage, queries, sourcemaps |
| `plugin-build` | Pinned build preset, shared dependency contract, remote and manifest output | Running a local platform for authors |

Names are `@backplane-labs/backplane-<suffix>`. Create packages when they have a real
implementation; do not publish empty placeholders. `web/apps/console` is the
production console. The isolated compatibility host lives in
`web/tests/fixtures/embedding`. `web/apps/catalog` documents the kit. Package exports keep heavy editors and charts
out of simple API consumers. Public APIs do not expose Grafana implementation
details unnecessarily. Copied code retains its upstream notices and revision.

## Shared UI contract

React and TypeScript are mandatory for modules. Primitives use shadcn with
Tailwind; missing complex controls use internal adapters around `@grafana/ui`.
There is one platform Button, Dialog, Select and form vocabulary. A Grafana
component may use its own internal controls, but it must receive the same theme
and fit the same focus, spacing and overlay conventions. Authors may build domain
components using the platform kit and tokens.

HyperDX supplies the visual reference: neutral dense surfaces, green accent,
IBM Plex Sans and IBM Plex Mono. Both dark and light modes are required from the
first implementation; dark is the default. One typed token source produces
CSS custom properties and resolved `GrafanaTheme2` colors. Do not feed unresolved
CSS variables to Grafana color calculations. Typography, radii, spacing, focus,
semantic status colors and visualization colors belong to that source too.
The kit distributes compiled CSS; consumers do not scan its source with Tailwind.

Use i18next/react-i18next with English resources only. All product strings,
validation messages and accessible labels use keys. Plugins register isolated
namespaces. There is no language switcher or Russian translation. Number/date
formatting uses the locale; resource values, identifiers and raw backend errors
are data, not translation keys. Vendored/adapted controls must support these
requirements before they count as covered in the matrix.

## Plugin author workflow

An author runs `yarn dev` or the template's ready container and opens their UI.
Backplane, Consul, NATS and Temporal are not prerequisites for this workflow.
The template includes a working standalone entry, sample routes, local fixtures
and a configurable client provider. Whether the author uses real local services,
mocks or a remote environment is their choice. The template documents that setup;
the platform does not control it through a plugin development runner.

The standalone entry provides the same context contract that the embedded entry
receives from the host: theme, i18n, router location/base, platform client,
service-client transport and service context. Fixtures are explicit development
providers, never a silent fallback for failed production calls. A standalone
entry owns its router/provider; the embedded entry consumes the host's instances.

The author controls the number of pages and their content. All routes are
relative to the module's `/s/<service>/` base. The platform controls the root
router, its pages and the registry of module navigation entries. A module cannot
replace the root router or register another module's paths. The SDK validates
route/navigation declarations, supports deep links and disposes module effects
on unmount. The shell has a header and a left navigation tree: Services, Wiring, Explore,
Errors, Audit and Infrastructure first, followed by services with module-owned
page leaves. The sidebar
supports a collapsed rail, hover/focus expansion, pinning and adjustable width.
See [console guide](../website/docs/guides/console.md).

Production delivery remains service-owned: a built remote is served by the
module's platform port, cached/proxied by backplane, and loaded from the console
origin. The build emits `plugin.json`, `mf-manifest.json`, relative asset URLs
and the `./Routes` and `./Nav` exports. The host validates `sdk_major` before
executing the remote. React, React DOM, router and context-bearing platform
packages are shared singletons with compatible versions. Incompatible remotes
fail visibly; bundling another React copy is not a compatibility strategy.
Build output must work under a console prefix and a hashed plugin URL without
external scripts, fonts or inline scripts forbidden by the console CSP.

The platform's `make dev` is a different workflow: run the installation, backplane,
hello and formatter with a seeded binding/rule so console developers see live
data. Full installation tests validate embedding; they do not gate standalone
module development.

## API and transport

Generate from the checked-in protobuf sources using pinned `protoc-gen-es` and
`protoc-gen-ws-es`; generation includes dependencies and is reproducible with
`make gen`. Preserve protobuf int64 values losslessly; timestamps, sequence
numbers and identifiers must not pass through JavaScript `Number` implicitly.
Service authors generate their own internal clients using the same toolchain.

The browser uses the current cookie login endpoints and one ws-proto connection.
The host supplies both platform and service clients over that connection. Tokens
and cookies are not exposed in plugin context or saved in local storage.
Network failures, 429 and 5xx retain the session and use bounded backoff with
jitter and `Retry-After` where available. A confirmed expired/revoked session
requires login. A failed WebSocket handshake alone is not proof of logout:
recheck the session endpoint. Abort and discard obsolete work on logout or
provider replacement.

Do not automatically replay mutations after a connection loss with an uncertain
outcome. `Watch*` recovery is method-specific: snapshot watches replace state;
cursor streams resume only where the API documents replay and gaps. Unmounting
aborts outstanding calls. Consumers can distinguish loading, reconnecting,
empty, stale, partial and terminal failure states.

The API reference documents every method: request/response fields, validation,
gRPC codes, retry safety, pagination and stream initial state, ordering,
coalescing, cursor expiry and resubscription. Examples use the generated clients.
Keep protocol comments authoritative and check reference coverage in generation.

## Audit

Persist operator control actions: configuration, bindings, rules, test/manual
runs, cancellation and other workflow commands, schedules, event redrive/purge
and session changes. Reads and Watch subscriptions are not audit entries.
Ordinary relay access logs remain distinct from semantic control audit; opaque
service payloads are never copied into audit records.

For a PostgreSQL mutation, the audit entry and its optional OTLP outbox row commit in
the same transaction as the change. For an external command, persist intent
before dispatch and append its result after the outcome is known. A crash after
dispatch may leave an unknown outcome: never invent atomicity across PostgreSQL,
Temporal and NATS, nor blindly retry a non-idempotent operation. Intent and result
share an operation ID. Failed dispatch is distinguishable from rejected input.

Audit entries include stable ID, time, actor/session identity, action, subject,
outcome, operation ID and redacted structured detail. Do not store credentials,
session tokens or secret configuration values in details/diffs. The durable
outbox exports OTel Logs through the configured OTLP endpoint, independently of NATS; delivery is
at least once, and consumers deduplicate by entry ID. Multiple replicas claim
outbox work safely and recover after a crash.

The implemented [AuditService](audit-api.md) provides filtered pagination and
resumable watching with a transactionally locked ordering clock: IDs allocated before commit alone cannot guarantee
that a late commit is not skipped. Cursor expiry is explicit, requiring refetch.
Audit retention, partitioning and cleanup are deployment settings. There is no
retention configuration or retention display in the UI.

## Platform telemetry

The entire OTel stack is independent of Backplane: Collector processing,
exporters, telemetry stores, retention and their lifecycle belong to deployment.
Backplane integrates with it through an OTLP proxy like Komeet's gateway and
an authenticated storage-query API. It is also an ordinary telemetry
producer. Direct ingestion into the Collector continues without Backplane.
The first query driver uses VictoriaMetrics, VictoriaLogs and VictoriaTraces.

The implemented proxy and deployment knobs are documented in
[telemetry admission](telemetry-admission.md). Signal payloads pass unchanged;
Backplane adds no canonical blobs, duplicate records or metric-point archive.
The implemented [ObsService](obs-api.md) reads the same stored signals through
the authenticated query API; its acceptance tests also cover native discovery.

External applications can send ordinary OTLP without registering as modules.
Errors are part of the platform: `@backplane-labs/backplane-errors` captures them in
any frontend, `ErrorService` reads them from the log store and the console
shows them ([errors.md](errors.md)). Storage credentials do not reach plugins.

One installation is one tenant/trust boundary. Applications and environments
are resource attributes, not tenant isolation. Isolated tenants use separate
deployments. UI reads use the operator cookie. Trusted server reads use the
existing installation `BACKPLANE_INTERNAL_SECRET` on a protected internal API;
there is no second class of service read keys. External OTLP admission works
without a user session or mandatory ingest key, including before login and when
the application or identity service is unavailable. Public telemetry is untrusted;
resource attributes do not authenticate an application or user. An optional
ingest-only key requirement is deployment configuration, not a platform key
issuance/revocation API. A browser key is public and grants no read/admin access.
Client Authorization, Cookie, Baggage and proxy credentials are not forwarded to
the Collector; deployment-owned upstream credentials remain separate.

Admission enforces a streaming body limit (including chunked requests), per-IP
rate/burst limits, a total concurrent-request bound, bounded memory and an
upstream timeout. The IP limiter itself has bounded cardinality and expiration;
only configured trusted proxies can supply forwarded client addresses. Per-IP
and total bounds are per replica; deployment edge controls enforce an installation
budget across replicas. Saturation fails promptly with an explicit retry response
and Retry-After; the proxy never creates an unbounded waiting queue or retries
an uncertain upstream write. Decode and processing budgets include decompressed
payloads when compression is supported. Paths, methods and content types are
allowlisted. SDK capture limits and bounded queues complement server protection,
but are not trusted admission controls. Unlike Komeet's application-specific
metric allowlist, generic OTLP preserves module-defined metric names/attributes;
deployment processors and storage budgets constrain cardinality. All limits and
optional key configuration belong to deployment, with no UI management pages.

An OTLP signal passes through the proxy's transport limits to the independent
Collector. The proxy does not parse or re-encode messages, impose a metric schema,
or change body/resource/scope attributes. Protocol decoding, redaction, enrichment,
aggregation and export belong to the external OTel stack. Collector status codes,
partial-success responses and retry instructions are forwarded without interpreting
signal counts. Acceptance is not a guarantee of durable storage. Retention and
resource budgets are configured in deployment only, never exposed in UI.

Storage formats and their capabilities belong to the backend. The query driver
preserves values and precision returned by its API and documents mappings and
limitations. It does not reconstruct lost types or introduce a parallel storage
format to compensate for backend behavior.

### Discovery and optional declarations

Telemetry exists independently of module declarations. The source catalog is
discovered from stored signals, including infrastructure and external apps.
The default grouping is `service.namespace → service.name → service.instance.id`;
missing fields stay missing, and original source identities remain visible.
Namespace is a grouping attribute, not an access-control boundary.

Related components can share a namespace while retaining separate names: an
identity wrapper and Kratos are both visible under the identity group. Deployment
can set standard OTel resource attributes or enrich them in the Collector when
the source is known. Do not guess service/instance identity from a shared proxy
address or force an existing namespace to change.

A module may declare selectors for one or more sources. Deployment can override
them. Resolution order is **deployment override → module declaration → OTel
convention matching**. The resulting selectors prefill module telemetry views;
they never govern admission, storage or visibility in global Explore. An explicit
empty selector set is different from no declaration. A shared namespace does
not create a trace relationship; correlation requires actual trace context.

### Query contract

The API includes typed source/attribute discovery, search, filters, aggregations,
time-series queries and complete trace lookup by trace ID. Expose all attributes and bodies available through the backend, with explicit
field mappings and capabilities, so modules are not limited to prebuilt
service-card queries. Do not promise lossless OTLP reconstruction from a backend
that returns flattened fields. Results distinguish empty data,
partial data and backend unavailability, with explicit bounds and cancellation.

Advanced queries carry signal, language, expression, time bounds and limits.
The Victoria driver supports MetricsQL/compatible PromQL for metrics and LogsQL
for logs and raw spans. TraceQL support is capability-gated against the pinned
VictoriaTraces version and tested syntax subset; it is not a promise of full
Tempo compatibility. Complete trace lookup is a typed operation independent of
the search language. Unsupported signal/language combinations return a clear
error. Editors expose only supported languages and fetch field suggestions through
the platform API. Queries are not rewritten silently into a different language.

## Verification boundaries

Standalone development must work without platform infrastructure. Embedding checks
must exercise the module under the host router, shared providers and production CSP.
Packed-consumer checks install actual package tarballs outside the workspace.
Live backend tests verify real persistence, transport and storage behavior; mock
results alone are not evidence of a working telemetry or execution pipeline.

See [development and verification](development.md) for the supported commands.
