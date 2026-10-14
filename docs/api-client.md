# Console client contract

[All console methods](api-reference.md) are generated from every service in
`backplane.console.v1`, including ObsService and AuditService. Imports use
`@backplane-labs/backplane-api`; transport/session ownership is in
`@backplane-labs/backplane-client`; React integration is in `@backplane-labs/backplane-react`.
Packages live in this repository and target GitHub Packages in the backplane-labs
organization. Nothing is published to npm by development/build commands.

## Authentication and transport

The host owns one `BackplaneClient` and passes it through `BackplaneProvider`.
Embedded modules consume `useClient(ServiceClient)` and never log in, dispose the
host client, or create their own root router. Standalone modules receive an
explicit author-supplied fixture/runtime. They do not need backplane.

```ts
import { create } from '@bufbuild/protobuf';
import { CatalogServiceClient, GetServiceRequestSchema } from '@backplane-labs/backplane-api';
import { BackplaneClient } from '@backplane-labs/backplane-client';

const runtime = new BackplaneClient({ baseURL: `${location.origin}/backplane` });
await runtime.start(); // checks the existing HttpOnly cookie
// The host presents login when runtime.getSnapshot().connection === 'anonymous'.
// await runtime.login(operatorToken); the token is never persisted by the client.
const api = runtime.client(CatalogServiceClient);
// Invoke after connection === 'connected'.
const service = await api.getService(create(GetServiceRequestSchema, { name: 'hello' }), {
  signal: new AbortController().signal, timeoutMs: 10_000,
});
console.log(service);
// The host calls runtime.dispose() on application teardown.
```

`baseURL` includes the console deployment prefix. HTTP auth endpoints are
`auth/login`, `auth/session`, `auth/logout`; WebSocket is `ws` with subprotocol
`wsrpc.v1`. Requests use same-origin cookies. Cookie contents never enter JS.
Use a same-origin development proxy for live backend access. Deployment secrets
and OTLP ingest keys are not browser client options.

Confirmed HTTP 401 clears the session. Network failures, 429 and 5xx preserve it
and retry with bounded jitter and `Retry-After`. Failed logout keeps local state
so the UI does not falsely claim server-side revocation. Cookie replacement in
another tab terminates the old transport/in-flight work on the next session
check. WebSocket reconnect never replays a unary request. An ambiguous mutation
failure must be resolved using audit/run state before a user retries it.

Connection states: idle → checking → anonymous or connecting → connected;
socket failure gives reconnecting/offline, disposal is terminal. Every new
socket increments `connectionId`. Subscribe through `useConnection()` or the
runtime external-store API. A transient error is distinct from authentication
loss and must not open a login screen over an existing session.

## Values, errors and cancellation

Every generated method accepts `CallOptions`: abort signal, deadline,
metadata/header/trailer callbacks. Pass an AbortSignal for component lifetime
and cancel on query/filter changes. A deadline can expire after a server-side
mutation was committed. Server-stream iteration is lazy; always consume it with
`for await`, and close it on unmount. The transport bounds frames to 16 MiB and
its receive queues; overload surfaces explicitly rather than growing forever.

Protobuf 64-bit values are `bigint`; protobuf JSON encodes them as decimal
strings. Use `toJson`/`fromJson` rather than `JSON.stringify` on protobuf objects.
Obs metric values/timestamps are strings. Native trace JSON must be decoded by
the lossless observability adapter; plain `JSON.parse` may lose integer precision.
Rendering a chart may approximate numeric coordinates, but copying/exporting
must use the original exact value.

`WsStatusError.code` is the gRPC numeric status. A successful RPC can still
contain validation `violations`, parse `errors`, a failed `CallResult`, rule
evaluation `error`, or dead-letter `failed` items. These are product results,
not transport exceptions. Unknown status codes remain visible to the caller.

## Common

All methods inherit transport cancellation (`Canceled` 1), deadline
(`DeadlineExceeded` 4), bounded-resource rejection (`ResourceExhausted` 8),
connection loss (`Unavailable` 14) and authentication failure
(`Unauthenticated` 16). A server handler can also fail with `Internal` 13.
Authorization middleware can report `PermissionDenied` 7. Only read operations
are eligible for automatic retry. See each family below for backend mappings;
they are conservative service-wide sets, not a promise every method emits every
code. External command wrappers additionally return `Unavailable` when intent
could not be stored (no dispatch), and `Aborted` 10 when result storage failed
(dispatch may have completed). See [audit outcomes](audit-api.md).

## Catalog

`GetService`: `NotFound` 5 for an unknown service. Lists return current registry
snapshots. `WatchCatalog` can coalesce intermediate snapshots for slow consumers.

## Session

Session queries/revocations return `Unavailable` for database failure,
`InvalidArgument` for a malformed session ID and `NotFound` for an absent session.
Revocation closes the target session's active connections. Login/logout are HTTP
endpoints, not generated RPCs. Never log session cookies or operator tokens.

## Config

`Unavailable`: registry not synchronized. `NotFound`: service/revision absent.
`FailedPrecondition`: no manifest. Unexpected storage/reconciliation failures
map to `Internal`. Validation failures are structured `violations`, including
save/rollback responses; a nil revision with violations is not success.

## Binding

`Unavailable`: catalog/executor not synchronized or configured dependency unavailable.
`FailedPrecondition`: execution is disabled by deployment.
`NotFound`: binding/version absent. `FailedPrecondition`: tombstoned binding.
`Aborted`: `base_version` is set and the current version is another one (an
edit raced another editor; reload and reapply). Malformed run/request
identifiers may return `InvalidArgument`. Unexpected backend failures map to
`Internal`; executor statuses propagate. Validation violations are response
fields. Test calls return `CallResult`.

## Rule

Binding-family statuses also apply to rules. Run commands additionally map
invalid UUID/request to `InvalidArgument`, missing/wrong run to `NotFound`, and
disabled execution to `FailedPrecondition`, configured-but-unavailable Temporal to `Unavailable`. Temporal statuses propagate. A test can
return validation violations, an evaluation error, matched=false, or a call
result; matched=false alone is a successful test with no run.

## Wiring

`Unavailable`: registry not synchronized. `InvalidArgument`: a rename without a
definition. Validation violations (analysis, a refused rename) are response
fields; nothing is saved.

## Operations

Event, workflow, schedule and call services map bad input to `InvalidArgument`,
disabled execution to `FailedPrecondition`, an unsettled or unavailable configured backend
to `Unavailable`, undeclared/missing subject to
`NotFound`, and invalid operation state to `FailedPrecondition`. Unexpected
failures become `Internal`. Temporal's recognized status codes propagate, so
`AlreadyExists`, `PermissionDenied`, `ResourceExhausted` and other backend codes
must remain distinguishable. Redrive/purge can partially succeed; inspect every
failed sequence. Cancel is a request to workflow code; terminate ends a run
without cleanup. Neither should be retried blindly after connection loss.

## Audit

`InvalidArgument`: bad filter, range, limit or cursor. `Unavailable`: database
failure. `Internal`: malformed stored metadata. Full [feed, ingest and outbox
contract](audit-api.md).

## Observability

`InvalidArgument`: signal/language/query/range/limit invalid.
`FailedPrecondition`: signal or language is not enabled. `NotFound`: trace
absent. `ResourceExhausted`: concurrency/size/result budget.
`DeadlineExceeded`: query deadline. `Unavailable`: storage/network failure.
Backend messages are sanitized. A successful response may carry `truncated`,
`partial` and warnings; show those states. Full [driver contract](obs-api.md).

## Errors

`InvalidArgument`: bad filter, range, limit, reference or cursor.
`FailedPrecondition`: the log store is not configured. `ResourceExhausted`:
concurrency or size budget. `DeadlineExceeded`, `Unavailable`: the log store is
slow or down; its messages are not echoed. A response may be `partial` with
warnings; show that state. Full [errors contract](errors.md).

## Snapshot watches

`WatchCatalog`, `WatchConfig`, `WatchBindings`, `WatchRules` begin with a complete
snapshot. Replace the previous value; do not append or interpret index gaps as
missing events. `useSnapshotWatch` restarts on connection changes and transient
stream failure, including unexpected EOF. It retains the same query's value as
stale while retrying and discards it when the provider/session/query changes.
Memoize its `open` callback. Nonretryable errors stay explicit.

```tsx
const api = useClient(CatalogServiceClient);
const open = useCallback((signal: AbortSignal) =>
  api.watchCatalog(create(WatchCatalogRequestSchema), { signal }), [api]);
const state = useSnapshotWatch(open);
// Render loading, ready, stale-with-value, and terminal error separately.
```

`watchWithRetry` is available without React for long-lived read watches. It
retries statuses 4, 8 and 14 with abortable capped jitter. It does not retry
validation, permission, authentication or cursor-expiry errors. Do not use it
for a mutation or a finite stream.

## Audit feed

The audit feed is read by page; a live view repeats the first page on an
interval and merges it by record ID over what it shows (new records on top,
the rest kept). Older pages follow `nextPageCursor` with the identical filter.

```ts
const api = runtime.client(AuditServiceClient);
const filter = create(AuditFilterSchema, {
  start: timestampFromMs(Date.now() - 3_600_000),
  conditions: [{ target: { case: 'field', value: AuditField.SERVICE }, op: AuditOperator.IS, values: [fromJson(ValueSchema, 'iam')] }],
});
const page = await api.searchAudit(create(SearchAuditRequestSchema, { filter }), { signal });
const older = page.nextPageCursor
  ? await api.searchAudit(create(SearchAuditRequestSchema, { filter, pageCursor: page.nextPageCursor }), { signal })
  : undefined;
```

The caller owns `filter` and `signal`.

## Verification

`make gen` updates protobuf clients, the method inventory and callable examples
for all methods. `yarn typecheck` checks those examples. Unit tests cover cookie
failures, logout, late responses, no unary replay, 64-bit round trips, stream
retry/cancellation/backoff and terminal statuses. `make web-check` adds browser
acceptance against the real Go console and service-owned bundle delivery.

## Platform

`PlatformService.GetCapabilities` returns configuration, independent of health.
Feature names: `events`, `workflows`, `schedules`, `bindings`, `rules`, `metrics`,
`logs`, `traces`, `telemetry`, `gateway`, `audit`, `audit_export`.
`GetInfrastructure` reads cached per-replica probes with `ok`, `checked_at`,
`reason` and `required`. Disabled dependencies are absent. Empty `checked_at`
means the initial check is pending, not an observed outage. Reasons are safe
codes (`checking`, `unavailable`, `timeout`); connection strings never leave the
server. Common authentication/transport errors apply. Health is available while
the control plane and its session store can serve requests.

Disabled runtime RPCs return `FailedPrecondition`; configured but unavailable
backends return `Unavailable` (or a request deadline). Binding/rule definitions
remain editable when their execution engine is disabled.

### Administrative schedules

Temporal stores schedules; the SDK only declares and executes workflows.
`CreateSchedule` validates the workflow and its JSON input against the manifest,
and accepts cron or a positive interval with a time zone. New schedules skip
overlapping executions. `GetSchedule` returns an opaque revision and normalized
Temporal timing. `UpdateSchedule` requires that revision, keeps pause/note,
policies and advanced action settings, and only replaces timing when explicitly
supplied. A stale or concurrently rejected update returns `Aborted`. Reopen the
schedule before retrying. The server serializes schedule commands across
replicas with a PostgreSQL advisory lock and waits for update confirmation.
`DeleteSchedule` leaves existing workflow runs intact. Listing, reading and
deleting work even after the module disappears from the registry. SDK declarations
never resurrect deleted schedules or reset an operator's pause.

Mutations have durable audit intent/result. Never automatically retry them after
an ambiguous transport failure; inspect the current schedule and audit first.
The generated Go/TS clients call the same administrative API and need no Temporal
SDK. CLI clients may use the existing authenticated console WebSocket transport.

Schedule listing uses bounded pages (default 20, maximum 50) and filters by service
in Temporal. Pass `next_page_token` to continue with the same service filter; an
empty page may still carry a token. Each request has a ten-second backend budget.
Definitions remain editable when execution capabilities are disabled; the host
passes these capabilities to `PlatformExecutionProvider` for Wiring controls.
