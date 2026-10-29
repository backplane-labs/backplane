# Platform compositions

`@backplane-labs/backplane-platform-ui` connects the kit to the generated console APIs.
It consumes `BackplaneProvider`; it does not create another connection, router
or console shell. English resources are exported as `platformEnglish` for
`backplane.platform`. Components accept the host's dark/light mode and are
router-agnostic: navigation leaves them through callbacks (`onNavigate`,
`onOpenService`, `onStateChange`).

| Export | Behavior |
| --- | --- |
| `ServiceCatalog`, `ServiceInspector` | Instances (phase, uptime, address, applied/rejected revision, readiness) with an instance drawer (transports, node readiness, per-key configuration source, effective masked configuration); contract panels for APIs, hooks with binding state, activities, published/consumed events, workflows, schedules; component tree; metadata |
| `ServiceAPI`, `ServiceAPIDocument` | External API reference: API selector, searchable operations and types, linked request/response types, visible examples with copy, schema constraints and nested fields, streaming RPC signatures, GraphQL queries/mutations/subscriptions, source preview and JSON download. OpenAPI JSON/YAML (3.x and Swagger 2.0), protobuf descriptor sets and GraphQL introspection. `ServiceAPI` reads the catalog; `ServiceAPIDocument` accepts a manifest directly. API/operation state leaves through `onStateChange`. |
| `ConfigurationPanel` | Live settings grouped by top-level key; each path shows title, description, unit, per-instance source and effective value; an override switch opens a typed editor (switch, number, text, choice, JSON); dirty markers, validation per path, save with comment; rollout per instance; revision history with a diff drawer and rollback |
| `AutomationPanel` | A service's wiring at a glance: its hooks with binding state, version and activities, the rules on its events with state, and the bindings and rules elsewhere that call its activities; every row opens it in Wiring (`onOpenWiring`) |
| `WiringWorkspace` | Wiring (see below): live lists of bindings and rules by service, the activity palette, an overview of what needs attention, and the editor of the open item; state (`target`, `view`) reported through `onStateChange` for the URL |
| `WiringEditor` | One binding or rule: Graph and YAML on the same draft, problems, inspector, versions, test and runs |
| `OperationSelector`, `ServiceOperations` | Declared hooks, activities, events and workflows of a service; input as a schemapb form (required fields and defaults filled) or as JSON scaffolded from the schema with key completion and schema diagnostics; elapsed time and Cancel while the call runs; outcome with status, timing, the handler's error and type |
| `WorkflowsPanel`, `WorkflowRuns`, `SchedulesPanel`, `RunDrawer`, `RunTable`, `RunInspector` | Declared workflows with start-from-schema, an optional workflow id and execution timeout (a running id is refused and says so); a warning when no worker polls a workflow's task queue. Runs switch between the service's workflows and its hook calls (`<service>.hooks` and the console's calls), filter by workflow type (declared and seen), status (including continued-as-new) and workflow-id prefix (a removable chip; a schedule's "Runs of this schedule" sets it), page with tokens and refresh after a start or trigger. The run drawer shows status, who started it (memo `source`) and the rest of the memo, parent and continued-as runs as links opening that run in the same drawer (with a way back), input/result (backplane envelopes with their payload as JSON), failure, every field of pending activities, history with expandable payloads and links to child runs, and cancel/terminate/signal (an empty signal payload sends no argument); a running run refreshes every 3 s while shown. Schedules are administered through the platform API: create for a declared workflow, edit with revision protection, delete without canceling existing runs; edits preserve pause. Timing changes are explicit, and the normalized current timing is inspectable. Legacy declaration metadata, when available, can still show: "Not in Temporal" (actions disabled), drift of the paused flag, next runs (up to 5), recent runs and running ids linking to their runs, missed/skipped counters, created/updated, owner; pause/resume ask for an optional note. `RunDrawer` accepts a `RunSource` so binding and rule runs load and cancel through their own APIs; their terminate and signal go through WorkflowService, as the controls say |
| `EventStreams`, `DeadLettersPanel` | Events the service publishes and consumes; stream state (`GetStream`); per event subscribers with pending/ack/redelivery/dead letters, newest-first message pages, message drawer, test publish from schema; dead letters by consumer with redrive and purge |
| `AuditExplorer`, `defaultAuditState`, `encodeChips`/`decodeChips` | The audit feed: platform entries and application records in one table (time, source, service, action, actor, subject, outcome, details, trace/run links; new rows light up on refresh). Filters are chips — a field or attribute, an operator, values — built in a popover with value suggestions, or with one click on a field value in the table, the fields panel (Alt: exclude) or the record drawer (+/−); plus text search, a `TimeRangeControl` range and a `RefreshControl` interval. A histogram over the range (drag to zoom), value counts per field, paging, a drawer with every field and attribute and the resource. State (`range`, `chips`, `text`, `live`) goes through `onStateChange` for the URL; `service` pins a service condition |
| `ErrorsExplorer`, `defaultErrorsState`, `parseStack` | Errors from the log store ([errors.md](errors.md)): the same feed parts as Audit (`FeedFilterBar`, `FeedTimeBar`, `FeedHistogram`, `FeedFacets`, `useFresh`, exported for other feeds) over `ErrorService`; the table shows origin (browser SDK or OTel log), service, type, message, environment, release and a trace mark; a row opens a drawer with the cause chain and parsed frames (V8, Firefox, Safari, Go) and the raw stack, the SDK's state and history, the trace waterfall, related logs by relation and every stored field with keep/exclude filters. `service` pins a service condition |
| `ExplorePanel`, `ObsResults`, `TraceLookup`, `TraceView` | See below |
| `SystemMap`, `collectWires` | Graph of services and their connections (bindings: hook owner → activity owner; rules: event owner → activity owner; subscriptions: event owner → consumer) with filters per kind and a connection list |

## Wiring

Bindings implement hooks that services declare, so "New binding" starts from
a hook: hooks without a binding first (required ones on top), then bound ones,
which open their binding; a new hook comes from a service manifest. "New rule"
starts from an event.

A binding or rule is edited as YAML (its protojson written as YAML: steps
by name, values as JSON trees whose strings are CEL) or as a graph; both
change the same draft text, so switching views loses nothing and the YAML
keeps its comments.

- YAML: syntax and shape errors immediately (unknown keys, durations such as
  `30s`, `1m30s`, `500ms`), server violations from `AnalyzeBinding` /
  `AnalyzeRule` 250 ms after typing stops, underlined at their place (a JSON
  Pointer mapped to the YAML node, an expression range inside the scalar).
  Completion: keys by position, hook/event/activity names from the catalog,
  step names, CEL variables and schema fields after `x.` — inside a for-each
  step also its item (typed from the list's schema when the list is a plain
  field path), `<item>Index` and the steps of its body. The panel beside it
  shows the path, CEL type, expected type, description, reads, and for a
  for-each step the item type at the cursor.
- Graph: the trigger (hook input or event and meta), one node per step with
  typed input ports (the expression of each field) and output ports, the
  result. Edges come from the analysis: data reads, `when` reads (dashed
  amber) and `after` (dotted). Drag an activity from the palette to add a
  step, drag an output port to an input port to write the reference, drag a
  node to save its position in `editor`, Delete removes a step (and its
  name from every `after`) or unsets an edge that is exactly a reference.
  "Add step…" adds a step without dragging; "Tidy" forgets saved positions
  and lays the graph out by levels; the mini-map is on demand. The inspector
  edits the selected step: rename (server-side, by syntax trees), activity,
  when, input fields with inline CEL and completion, after, undo, retry and
  timeouts; its fields commit on blur, one undo step each.
- For-each steps: one whose body is an activity is a step node with an
  "each <item> in <list>" band (concurrency, `continue`, limit) and a list
  output; one whose body is steps is a dashed frame holding them, its item
  a port inside, the body's result a port on its right. A step dropped into
  a frame joins that body; body steps connect only to what they see (their
  body, enclosing bodies, the item). Node ids and `editor` positions of body
  steps are `<for-each step>/<name>` (positions relative to the frame). The
  inspector turns a step into a loop ("Run for each item…") and edits the
  list, item name, concurrency, error policy, limit and a body's result;
  a body step links back to its loop. Run overlays count items (`ok/total`,
  failed) on loops and body steps; the run timeline lists calls as
  `<parent>/<step>[<item>]`.
- Save sends `base_version`; a concurrent save shows a banner with a diff
  against the latest and "continue from" it. Drafts survive switching items;
  leaving the page with drafts asks first. A version opens as a YAML diff
  against the current one, to roll back to or to open as a draft. Deleted
  rules stay in a collapsed group; their versions restore them. A rule needs
  at least one step.
  A broken definition (manifests changed) is flagged in the lists, the
  overview and the editor.
- Test runs the draft (unsaved) or the saved version; its run's step status
  and timing overlay the graph, as does any run opened from Runs.

## Publishing external API documents

The reference viewer opens all named types when a document contains at most eight.
Larger documents offer type search and expand/collapse controls. Clicking a type in
a request, response or field opens and focuses its definition, including imported
OpenAPI schemas. Nested inline properties are shown to four levels; named and
recursive references remain links. The complete raw definition is always available.
Protobuf types are limited to the transitive dependencies of this route's public
methods, including imported messages, enums and map values.

OpenAPI examples are shown directly, including named media examples and schema
examples; `x-codeSamples` supplies authored request snippets. Otherwise the viewer
builds a curl template with the declared server or a host placeholder. GraphQL
operations offer query templates when their argument types can be represented.
Protobuf messages offer sample JSON with placeholder strings. Examples are documentation,
not executed requests. Deployment-specific hosts, credentials and placeholders must
be supplied by the reader. The hello example publishes multi-file OpenAPI, protobuf
and GraphQL introspection; formatter publishes its real process-local statistics API.

The console's service **API** tab (`/services/<name>/api`) reads the latest
manifest from `CatalogService.GetService`. The route index and selected operation
are stored in URL query parameters. This is documentation, without executing
requests against the service. Missing and malformed documents have explicit
states; one malformed route does not prevent viewing another.

`Manifest.apis` contains any number of API declarations. Each API has at most
one schema: inline OpenAPI/protobuf/GraphQL bytes, or an `APISchemaBundle`
reference (`hash`, `entry`, `format`) for separately delivered OpenAPI/GraphQL
files. The manifest retains its 512 KiB limit; file payloads do not enter Consul KV.

For a multi-file OpenAPI description, embed the directory directly:

```go
import "embed"

//go:embed api
var apiFiles embed.FS

svc.HTTP("/api/", handler, api.OpenAPIFS(apiFiles, "api/openapi.yaml"))
// Or announce a handler served on an independent listener:
svc.API(api.HTTP("/api/", api.Port("legacy", legacyPort),
    api.OpenAPIFS(apiFiles, "api/openapi.yaml")))
```

The SDK snapshots regular files once and hashes both paths and contents. A later
change to an `os.DirFS` source does not change already published content. Bundles
are limited to 32 MiB total and 256 files; symlinks and invalid/missing entry paths
are declaration errors reported by `Service.Run`. Use `fs.Sub` to select a smaller
directory when needed. Separate routes can use separate bundles, or different
entry files in the same bundle. Identical snapshots share one platform handler.

Files are served at `/_backplane/api/<hash>/<file>` on the service's platform
port under the internal secret. The console authenticates the session and
proxies them at `/schemas/<service>/<hash>/<file>` under its configured prefix.
It selects an instance whose own version's manifest declares that hash, validates
the upstream ETag, and reuses the bounded plugin-file cache with separate keys.
API responses use `private, no-store` and attachment content; even cache hits
require a valid session. The browser client exposes `readSchemaFile` for this
delivery, and standalone fixtures may supply their own file reader.

The viewer resolves relative `$ref` files from the same snapshot, rewrites them
to internal pointers, and preserves recursive references without expanding them.
No build-time bundler or additional runtime dependency is needed. HTTP URLs,
absolute paths and paths escaping the bundle are rejected. The source preview
and download contain the resulting self-contained JSON document. The hello
example demonstrates a root YAML referencing `schemas/greeting.yaml`.

`api.OpenAPI([]byte)` remains available for small self-contained documents;
attach an embedded JSON/YAML byte slice as before. Inline documents support
internal JSON pointers and report unresolved references.

For managed gRPC and ws-proto registrations the SDK collects the registered
services' descriptors and **transitive imports** automatically into the shared
`Manifest.descriptors`. The API viewer limits the methods to the services
announced on the selected API. For independently served gRPC endpoints, pass
`api.Descriptors(fds)`; declarative `api.WSProto` takes `fds` directly.
Produce that binary descriptor set with imports included (for example,
`protoc --include_imports --include_source_info --descriptor_set_out=api.pb ...`)
and embed it as `[]byte`. Source comments are displayed when source info exists.
GraphQL takes one introspection JSON document, either `{"__schema": ...}` or the
ordinary response envelope `{"data":{"__schema": ...}}`.
For large introspection JSON, use `api.GraphQLFS(files, "schema.json")` and pass
nil for the inline introspection argument of `Service.GraphQL`/`api.GraphQL`.

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
