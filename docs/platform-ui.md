# Platform compositions

`@backplane-labs/backplane-platform-ui` connects the kit to the generated console APIs.
It consumes `BackplaneProvider`; it does not create another connection, router
or console shell. English resources are exported as `platformEnglish` for
`backplane.platform`. Components accept the host's dark/light mode and are
router-agnostic: navigation leaves them through callbacks (`onNavigate`,
`onOpenService`, `onStateChange`).

| Export | Behavior |
| --- | --- |
| `ServiceCatalog`, `ServiceInspector` | Instances (phase, uptime, address, applied/rejected revision, readiness) with an instance drawer (transports, node readiness, per-key configuration source, effective masked configuration); component tree; metadata |
| `ConfigurationPanel` | Live settings grouped by top-level key; each path shows title, description, unit, per-instance source and effective value; an override switch opens a typed editor (switch, number, text, choice, a nested schema form for an object, JSON for lists, maps and raw JSON) seeded with the effective value in the form an override accepts (durations at any depth); dirty markers, validation per path, save with comment; rollout per instance; revision history with a diff drawer and rollback. Startup settings (every other leaf of the schema) are listed read-only beside it: declaration (title, kind, unit, description, default), the value each instance runs with (secrets masked, "differs between instances" when they disagree) and a mark that a change takes a restart or a redeploy |
| `AuditExplorer`, `defaultAuditState`, `encodeChips`/`decodeChips` | The audit feed: platform entries and application records in one table (time, source, service, action, actor, subject, outcome, details, trace links; new rows light up on refresh). Filters are chips — a field or attribute, an operator, values — built in a popover with value suggestions, or with one click on a field value in the table, the fields panel (Alt: exclude) or the record drawer (+/−); plus text search, a `TimeRangeControl` range and a `RefreshControl` interval. A histogram over the range (drag to zoom), value counts per field, paging, a drawer with every field and attribute and the resource. State (`range`, `chips`, `text`, `live`) goes through `onStateChange` for the URL; `service` pins a service condition |
| `ErrorsExplorer`, `defaultErrorsState`, `parseStack` | Errors from the log store ([errors.md](errors.md)): the same feed parts as Audit (`FeedFilterBar`, `FeedTimeBar`, `FeedHistogram`, `FeedFacets`, `useFresh`, exported for other feeds) over `ErrorService`; the table shows origin (browser SDK or OTel log), service, type, message, environment, release and a trace mark; a row opens a drawer with the cause chain and parsed frames (V8, Firefox, Safari, Go) and the raw stack, the SDK's state and history, the trace waterfall, related logs by relation and every stored field with keep/exclude filters. `service` pins a service condition |
| `ExplorePanel`, `ObsResults`, `TraceLookup`, `TraceView` | See below |

## Layout contract

Compositions fill the height of their container and scroll inside their panels:
a page header or filter bar stays in place, lists and tables scroll with sticky
headers, panel footers (save bars, paging) stay visible. Give the component a
bounded-height flex or grid parent; without one it grows with its content.

## Explore

Explore is a query builder over the native languages the deployment advertises.
Filters with known values are pickers (service from stored sources, level,
metric names, span names, group-by labels); each change rewrites the native
expression, which stays editable. Field values from the field browser add a
filter. State (signal, query, language, range, limit, open trace) is reported
through `onStateChange` so the console keeps it in the URL.

- Logs: level-stacked volume histogram from LogsQL statistics over the window,
  newest-first lines with level and service, wrap and highlight, live tail,
  loading older lines up to the window start, line drawer with attributes and a
  link to its trace.
- Metrics dashboard: until a metric is chosen or a query typed, Explore shows a
  generated dashboard of every stored metric (scoped to the service when one is
  selected), grouped by family — the service's own metrics, HTTP/RPC,
  Backplane, runtime and the rest. Histograms (`_bucket/_count/_sum`) collapse
  into one p95 panel, series that only grow are shown as their rate, others as
  a sum by service. Panels load when scrolled into view (four queries at a
  time); a panel opens its metric in the builder. The service Telemetry tab
  opens on this dashboard.
- Metrics: metric picker, aggregation (raw, rate, sum, p50/p95/p99 for
  histograms) and group-by; chart in the local time zone and a legend table
  with last/min/avg/max per series that toggles series.
- Traces: spans are grouped into traces (root span, services, span count,
  errors, duration). The trace view is a collapsible span tree on a shared time
  ruler, colored by service, with span search, expand/collapse all and a span
  panel (status, timing, attributes, resource, events, links, related logs).

Query bounds use exact nanoseconds. Tempo JSON uses `lossless-json`; OTLP base64
IDs become hex for correlation; the raw response stays available.

## Runtime behavior

`usePlatformQuery` cancels old requests on key, provider, session or connection
replacement and never shows data of an old session. Read failures keep
same-session data for an explicit retry. `usePlatformAction` suppresses duplicate
in-flight submission and never retries mutations; a failure keeps the draft and
states that the outcome must be checked before repeating. Destructive or
installation-changing actions go through a confirmation dialog.

Audit uses `SearchAudit` with filter-bound pagination cursors for older records.
Live refresh repeats the first page; it is not a replay stream. Filter changes
reset pagination. See [audit query semantics](audit-api.md#methods).

The catalog's `PlatformFixture` is an explicit local transport covering every
composition, storage failure, no mutation retry, cursor advancement/expiry,
session replacement, schema input and integers above 2^53. Live acceptance runs
against the dev installation; see [development](development.md).
