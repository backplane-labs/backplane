# Errors

The console's Errors section lists exceptions from frontends and services:
grouped by fingerprint (one row per distinct error with its trend and counts)
or one occurrence at a time, each with its stack, the frontend's state and
history, its trace, related logs and where it came from. Backplane keeps the
occurrences itself, in its PostgreSQL database, for a configured retention
(90 days by default): the error record is kept whole, with its resource,
attributes and scope, so it survives log store retention and sampling. The
log store only serves the logs related to an occurrence.

## Where errors come from

| Source | Record | In the list |
| --- | --- | --- |
| Frontends with `@backplane-labs/backplane-errors` | one log record per capture: the exception, the app's registered state and a bounded history (breadcrumbs, state snapshots) as a JSON envelope (`app-debug` v1) in the body, the small fields as `app.debug.*` and `exception.*` attributes | origin `sdk`, one occurrence per event id (retries collapse) |
| Services on the Go SDK | `backplane.CaptureError(ctx, log, err, fields...)`; recovered panics of gRPC, ws-proto and HTTP handlers and of activities | origin `otel_log` |
| Anything else | any log record with `exception.type`, `exception.message`, `exception.stacktrace`, `event.name=exception` or `app.debug.kind=exception` | origin `otel_log` |

### Frontends

`@backplane-labs/backplane-errors` (web/packages/errors) runs in any web frontend,
not only in the console. It sends OTLP/HTTP protobuf logs to Backplane's
public OTLP ingest (`<console>/telemetry/v1/logs`, see
[telemetry-admission.md](telemetry-admission.md)); an ingest key there
(`BACKPLANE_OTLP_KEYS`) filters stray traffic but is public in a browser.

```ts
import { createOtlpClient } from '@backplane-labs/backplane-errors/otlp';
import { createReactErrorHandler, instrumentBrowser } from '@backplane-labs/backplane-errors/browser';

const errors = createOtlpClient({
  url: 'https://example.com/backplane/telemetry/v1/logs',
  headers: { authorization: 'Bearer <ingest key>' },
  resource: { 'service.name': 'shop-web', 'deployment.environment.name': 'production', 'service.version': '1.4.2' },
  captureUnhandled: true, // error and unhandledrejection of the window, or of a worker's self
  outbox: { name: 'shop' }, // optional: IndexedDB queue across reloads and offline periods
});
instrumentBrowser(errors, { fetch: true, xhr: true, navigation: true });
errors.registerState('cart', { read: () => cart.summary() });
createRoot(root, { onCaughtError: createReactErrorHandler(errors) });
```

- `captureException(error, { handled, state, groupKey, attributes })` captures
  one error; `addBreadcrumb(name, data)` and `recordState(name)` add history.
- `sanitize` (with `redactKeys`) and `filter` run before anything leaves the
  page; `rateLimit` bounds an error storm; causes are bounded
  (`exceptionLimits`).
- `snapshot()` returns local state and history for the app's own bug reports
  without sending anything.
- In a dedicated or shared worker the same client runs: the outbox replays on
  the worker's `online` event and its timer, `captureUnhandled` listens on the
  worker's `self`, and `instrumentBrowser` records fetch and XHR (navigation
  needs a page and throws `navigation_breadcrumbs_need_a_page`).
- Stacks are kept as text (no sourcemaps); the console parses V8, Firefox,
  Safari and Go frames from them.

The signed-in console reports its own errors as `backplane-console` through
`<console>/auth/telemetry/v1/logs`, using its HttpOnly session cookie and the
console's origin checks. This route shares the public proxy's limits and
Collector, but needs no ingest key in the frontend. Anonymous and expired
sessions cannot report through it; public applications still use ingest keys
on `<console>/telemetry/v1/logs` when configured. Modules report handled
errors with `usePluginErrorReporter()` from the plugin
SDK (marked `backplane.module=<service>`); their unhandled and render errors
are the console's.

### Services

```go
if err := charge(ctx); err != nil {
    backplane.CaptureError(ctx, c.Log(), err, xlog.String("order", id))
    return err
}
```

The record's type is the deepest cause of a telling type (a sentinel's
`*errors.errorString` and fmt's wrappers say nothing), its stack the caller's,
its trace the context's.

## Storage

The Collector's backplane pipeline (`deployments/otel-collector.audit.yaml`,
merged with the main configuration) forwards exception records, with the
application audit, over OTLP/gRPC to backplane's listener
`BACKPLANE_AUDIT_LISTEN` (`:4317`, keys `BACKPLANE_AUDIT_KEYS`; see
[audit-api.md](audit-api.md#delivery)). Records from the public proxy
(`backplane.ingest=proxy`: browsers, public ingest keys) are exceptions but
never audit. The Collector's persistent queue keeps records while backplane is
down; logs still go wherever the main configuration sends them.

Each record becomes one occurrence (`backplane.app_error`) in a group
(`backplane.app_error_group`):

- **Group**: the fingerprint and the service. The fingerprint is the SDK's
  `app.debug.group.key` when it sends one; else the type, the message with
  numbers, hex ids, UUIDs and quoted text replaced, and the functions of the
  top three stack frames (JavaScript and Go frames). A group counts every
  occurrence it was touched by and keeps when it was first and last seen.
- **Deduplication**: a retry of one record (the Collector's, the SDK's) is
  stored once, by the service and the SDK's event id, else by the record and
  its resource as sent.
- **Bursts**: a group stores at most `BACKPLANE_ERRORS_BURST_PER_MINUTE` (60)
  occurrences a minute and `BACKPLANE_ERRORS_BURST_PER_DAY` (2000) a day;
  past that it only counts. The console shows the stored and the counted.
- **Size**: a record over `BACKPLANE_ERRORS_MAX_RECORD_BYTES` (512 KiB) has
  its body and attributes replaced by a marker, a stack over 64 KiB is cut,
  small text fields over 4 KiB too; the occurrence is marked `truncated`.
  Text PostgreSQL cannot store (NUL, invalid UTF-8) and records without a
  readable resource are rejected and counted in
  `backplane.errors.ingest.records{outcome=stored|counted|duplicate|rejected}`.
- **Retention**: `BACKPLANE_ERRORS_RETENTION` (`2160h`, 90 days; `0` keeps
  for ever). An hourly pass deletes occurrences older than that, then groups
  last seen before it. `ErrorInfo` reports the retention and limits.

Occurrence ids are UUIDv7 and the API's `ref`; a fingerprint is shown as hex.
`BACKPLANE_ERRORS_*` are deployment-only.

### Triage

A group is `open`, `resolved` or `muted` (`SetErrorGroupState`, with a
note); the change is a platform audit entry `error_group.<state>` by the
console session that made it, with the note. Listings show open groups
unless asked for other states. A resolved group reopens on an occurrence
whose time is after the resolution and is marked regressed (`regressed_at`)
until its state is set again; a late record from before the resolution does
not reopen it. A muted group keeps storing and counting occurrences but stays
out of the open list until reopened. Occurrences themselves have no state.

## Reading

`backplane.console.v1.ErrorService`, over the database (related logs over the
log store of [obs-api.md](obs-api.md)):

- `SearchErrors(filter, page_size?, page_cursor?)`: occurrences newest first,
  keyset paged, 50 per page (200 at most).
- `SearchErrorGroups(filter, sort?, page_size?, page_cursor?)`: groups with
  occurrences in the range, by last seen (default), count or first seen, each
  with its total and in-range counts, a trend (stored occurrences per bucket),
  environments, releases, whether it is new (first seen in the range) and
  unhandled, and its newest occurrence's `ref`.
- `GetErrorGroup(fingerprint, service, filter?)`: one group with its buckets
  over the range.
- `SetErrorGroupState(fingerprint, service, state, note?)`: open, resolved or
  muted; the group as stored afterwards. `SearchErrorGroups` takes `states`
  (none: open only).
- `ErrorHistogram(filter)`: occurrences per bucket, at most 120 buckets.
- `ErrorFacets(filter, fields, limit?)`: service, environment, type, release,
  origin, ingest, instance, handled with counts and last seen; a field's own
  conditions are left out.
- `GetError(ref)`: the stored fields, the stack, the resource, attributes and
  scope as `Struct`s, the group, and for the SDK the envelope checked against
  its schema (`internal/errors/envelope`); what did not check out is a
  warning (`invalid_payload`, `unsupported_version`, `truncated`), not a
  failure.
- `RelatedLogs(ref, relation)`: logs of the occurrence's span, trace, SDK
  runtime, or its service within ±15 minutes, from the log store; without one
  the status is `unavailable`. The trace itself is `ObsService.GetTrace`.
- `ErrorInfo()`: retention, burst limits, the record size limit and whether a
  log store serves related logs.

The filter is a range (required, at most 31 days; without a filter the last
24 hours), a text (substring of the message or the stack, case-insensitive)
and conditions on service, environment, type, message, release, trace id,
runtime id, group key, origin (`sdk`, `otel_log`), fingerprint, handled
(`true`, `false`), ingest (`trusted`, `proxy`) and instance with is / is not
/ contains / starts with / exists where the field allows it. Everything is a
parameter of a generated query (`internal/store/queries/app_error*.sql`); no
SQL is built from callers.

## Console

Errors (and a service's Errors tab) is a feed like Audit: filter chips with a
builder and value suggestions, text search, time range, refresh interval, a
histogram (drag to zoom) and value counts per field (click keeps, Alt+click
excludes). The list is by group by default (type and message, service, a
sparkline of the range, in-range and total counts, first and last seen, new
and unhandled badges), sorted by last seen, count or first seen; "Occurrences"
lists them one by one; a State select shows open (default), resolved, muted
or all groups. A group opens its drawer: counts, first and last seen,
environments and releases, its state (who, when, why; "Regressed" when a new
occurrence reopened it), the trend and its occurrences, with Resolve / Mute /
Reopen taking an optional note. An occurrence opens
its own: the stack with its causes, the SDK's state and history, the trace
waterfall, related logs, the environment (resource, attributes, scope, ingest,
received time) and every stored field, with "Copy report" producing a text
summary for a ticket. The open group (`?fingerprint=&group_service=`) and
occurrence (`?ref=`) and the states listed (`?state=`) are in the URL and can
be linked.

## Tests

`internal/apperrors`: unit tests of classification, fingerprints and
truncation; `TestErrorsLive` (`BACKPLANE_TEST_PG`) stores, deduplicates,
bursts, searches, facets, groups, triages (with the audit entries and the
regression) and expires against PostgreSQL;
`internal/audit` `TestExceptionsRoutedToErrors` sends exceptions and audit
through one listener. `TestRelatedLogsLive` (`make test-otlp`) reads related
logs from a real Collector and VictoriaLogs. The SDK's unit tests
(`web/tests/errors-*.unit.ts`) and browser tests (`yarn test:errors-browser`,
Chromium) cover capture, breadcrumbs, React errors and the IndexedDB outbox;
the console's errors story is covered by `yarn test:storybook`.
