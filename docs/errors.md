# Errors

The console's Errors section lists exceptions from frontends and services with
their stack, the frontend's state and history, their trace and related logs.
Errors are ordinary OpenTelemetry log records in the installation's log store
(VictoriaLogs): Backplane copies nothing, and an error is kept as long as the
log store keeps logs. There is no deletion and no grouping into issues.

## Where errors come from

| Source | Record | In the list |
| --- | --- | --- |
| Frontends with `@backplane-labs/backplane-errors` | one log record per capture: the exception, the app's registered state and a bounded history (breadcrumbs, state snapshots) as a JSON envelope (`app-debug` v1) in the body, the small fields as `app.debug.*` and `exception.*` attributes | origin `sdk`, one row per event id (retries collapse) |
| Services on the Go SDK | `backplane.CaptureError(ctx, log, err, fields...)`; recovered panics of gRPC, ws-proto and HTTP handlers and of activities | origin `otel-log` |
| Anything else | any log record with `exception.type`, `exception.message`, `exception.stacktrace` or `event.name=exception` | origin `otel-log` |

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
  captureUnhandled: true, // window error and unhandledrejection
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

## Reading

`backplane.console.v1.ErrorService`, over the log (and trace) store drivers of
[obs-api.md](obs-api.md):

- `SearchErrors(filter, page_size?, page_cursor?)`: newest first, 50 per page
  (200 at most); SDK retries of one event collapse.
- `ErrorHistogram(filter)`: occurrences per bucket, at most 120 buckets.
- `ErrorFacets(filter, fields, limit?)`: service, environment, type, release …
  with counts and last seen; a field's own conditions are left out.
- `GetError(ref)`: the stored fields, the stack, the SDK envelope checked
  against its schema (`internal/errors/envelope`); what did not check out is a
  warning (`invalid_payload`, `index_payload_mismatch`,
  `event_id_body_conflict`), not a failure.
- `RelatedLogs(ref, relation)`: logs of the occurrence's span, trace, SDK
  runtime, or its service within ±15 minutes. The trace itself is
  `ObsService.GetTrace`.

The filter is a range (required, at most 31 days), a text (substring of the
message, case-insensitive) and conditions on service, environment, type,
message, release, trace id, runtime id, group key and origin with is / is not
/ contains / starts with / exists. Queries are LogsQL built by
`internal/obs/logsql`: values only through its quoting functions, fields only
from a fixed list; no LogsQL is accepted from callers.

## Console

Errors (and a service's Errors tab) is a feed like Audit: filter chips with a
builder and value suggestions, text search, time range, refresh interval, a
histogram (drag to zoom), value counts per field (click keeps, Alt+click
excludes) and the table (time, origin, service, type, message, environment,
release, trace). A row opens the stack with its causes, the SDK's state and
history, the trace waterfall, related logs and every stored field.

## Tests

`TestErrorsLive` (`make test-otlp`): SDK and OTel records through a real
Collector into VictoriaLogs, read back with every method. The SDK's unit tests
(`web/tests/errors-*.unit.ts`) and browser tests (`yarn test:errors-browser`,
Chromium) cover capture, breadcrumbs, React errors and the IndexedDB outbox.
