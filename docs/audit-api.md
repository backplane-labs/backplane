# Durable control audit and application audit

`backplane.console.v1.AuditService` uses the console cookie and ws-proto transport.
It serves two separate histories:

- **Control audit** (most of this document): platform control operations, not
  telemetry, arbitrary relay traffic, reads, watches or automatic application
  workflow steps.
- **Application audit** ([below](#application-audit)): log records services and
  third parties mark `backplane.audit=true`, forwarded by the deployment's
  Collector and kept by backplane.

The control audit records platform control operations only. Execution details remain
in Temporal and event payloads remain in JetStream.

## Persistence and coverage

Configuration save/rollback, binding save/delete/rollback, rule save/delete/
rollback/pause and session create/revoke/revoke-others/expiry append an entry and
an outbox row (when export is enabled) in the same PostgreSQL transaction as the mutation. Rejected database
mutations create no success entry. Session touch is not a control mutation.

External commands persist `intent` before dispatch: test event publication,
dead-letter redrive/purge, workflow start/cancel/terminate/signal, schedule
create/update/delete/pause/resume/trigger, hook calls, activity runs, binding/rule test and run cancel.
The result has the same `operation_id` and one of these outcomes:

| Outcome | Meaning |
| --- | --- |
| `succeeded` | The API confirmed completion or acceptance of the requested command. Starting a workflow does not mean the workflow completed. |
| `rejected` | Invalid input, failed precondition, missing subject, an existing subject (a workflow id that is already running), permission refusal or structured validation violations. |
| `failed` | A returned call result contains a failure, rule evaluation failed, or every attempted batch item failed. |
| `partial` | A dead-letter batch contains both completed and failed items. |
| `unknown` | Dispatch returned an ambiguous transport/backend failure, cancellation or deadline. |

Intent without a result also means unknown. There is no distributed transaction
with Temporal/NATS. Failure to persist intent returns `Unavailable` and does not
dispatch. Failure to persist the result returns `Aborted` with the operation ID;
the command may already have completed. Never automatically replay it. Consult
the operation audit and the external run/event state.

Metadata is allowlisted: actor/session UUID, operation/entry UUID, action,
subject identifiers, revision/rollback reference, changed configuration key
names, pause flag, fixed diagnostic code, workflow/run IDs (the started run, or
the run a cancel/terminate/signal addresses) and affected session count. The
operator's words on run and schedule commands are kept, cut to 256 characters:
the signal name of `workflow.signal` (never its argument), the reason of
`workflow.terminate` and the note of `schedule.pause`/`schedule.resume`, in
both the intent and the result. Request/output payloads, config values,
revision comments, credentials, tokens and arbitrary error messages/types are
excluded. In particular, `TestRule.event` is
payload and is never copied to the subject. Identifiers are caller-visible
metadata; authors should not put secrets into names or run IDs.

## Ordering and delivery

Updating the singleton audit clock locks it until the mutation transaction
commits. This intentionally serializes audit sequence allocation across replicas:
a higher visible sequence cannot overtake a lower uncommitted entry. Rollback
removes the mutation, entry and outbox together. Sequence allocation is part of
the existing serializable transaction and its serialization retry.

Workers claim at most 16 outbox records with PostgreSQL `SKIP LOCKED`, a unique
lease and 60-second lease timeout. Publish is bounded to 3 seconds per record;
failed records become available after 2 seconds. All claimed records receive an
attempt even if one fails. A crashed worker's lease expires; a stale worker
cannot acknowledge a newer claim. Delivery is at least once, can be reordered
across replicas, and uses entry UUID as the stable OTel `backplane.audit.id` deduplication ID.
Consumers deduplicate by ID and use sequence when ordering matters.

Every entry names the service it is about in `service`: the service whose
configuration was saved, the owner of a bound hook (`hello` for `hello.Greet`),
the owner of the event a rule reacts to, or the service a command addresses
(its `service`/`subscriber` field, the owner of its hook, activity or event, the
event owner of a rule id, or the service of the run it acts on). A run's service
comes from backplane's workflow ids — `console/<service>/…`,
`console/activity/<service>/…`, `hook/<service>/…`, `binding/<hook>/…`,
`test/<hook>/…`, and `rule/<id>/…`/`test/rule/<id>/…` (the rule's event owner).
Any other id is asked of Temporal once (DescribeWorkflowExecution, bounded to
2 s): the run's task queue `<service>` or `<service>.hooks` names the service;
when Temporal cannot answer, a schedule run id `<service>/<Name>-<time>` still
does. Installation-wide entries such as sessions leave it empty. The field
is computed when the entry is written and stored in its own indexed column;
entries written before the column existed were attributed by the same rules in
the migration. Filtering by `service` therefore covers entries whose subject
does not contain the service name, such as rule ids.

Platform audit export uses OTLP Logs, independently of NATS. Set
`BACKPLANE_AUDIT_EXPORT_URL` to the full OTLP/HTTP logs endpoint (for example
`http://collector:4318/v1/logs`), and optionally
`BACKPLANE_AUDIT_EXPORT_AUTHORIZATION`. No export endpoint means no publisher
or new outbox work; PostgreSQL history and AuditService remain available.
Already pending records are retained and resume on re-enabling export, including
records queued by installations that previously used event delivery.

The outbox acknowledges only a successful synchronous OTLP response; partial
rejection, invalid responses and network failures retry. Records carry the stable
entry UUID and `backplane.audit.origin=platform`. The Collector's backplane pipeline
filters these records, and ingest acknowledges echoes without inserting them as
application records. Their authoritative row is already committed in PostgreSQL.
The optional export's JSON body contains only the same allowlisted metadata;
sequence is a decimal string. The API uses protobuf `uint64` (`bigint` in TS).


## Methods

The console reads one feed: platform entries and application records alike
(`AuditRecord.source`), a platform entry's detail as its `attributes`. Every
method takes the same `AuditFilter`: an inclusive-start/exclusive-end range,
a text (case-insensitive substring of the message, action, subject or
attributes) and conditions that must all hold. A condition targets a fixed
field (source, service, action, actor, subject, outcome, operation, severity,
trace id) or an attribute key, with an operator: `IS`/`IS_NOT` (any of the
values; attributes compare JSON values exactly), `CONTAINS`/`NOT_CONTAINS`,
`PREFIX`, `EXISTS`/`NOT_EXISTS`, and `GT`/`GTE`/`LT`/`LTE` for attributes
holding numbers. At most 32 conditions of 64 values, 2048 bytes each.

- `SearchAudit(filter, page_size?, page_cursor?)` — newest first by time, then
  id; 100 per page, 500 at most. `next_page_cursor` continues with the
  identical filter and is empty on the last page. A live view repeats the
  first page and merges it over what it shows.
- `AuditHistogram(filter, buckets?)` — counts of platform, application and
  failed (failed, rejected, partial, unknown) records per bucket; 60 buckets
  by default, 240 at most, of a round width over the range (from the first
  matching record when it has no start, until now when it has no end).
- `AuditFields(filter)` — the attribute keys of the matching records with
  counts, 200 at most: the choices of the filter builder.
- `AuditFacets(filter, targets, limit?)` — each target's most frequent values
  (10 by default, 50 at most) and how many records set it. A target's own
  conditions are left out, so the other choices stay visible.

The queries are sqld typed dynamic queries over the view `backplane.audit_feed`
(`internal/store/queries/audit_feed.sql`): unset parts of the filter drop
their condition; equality on fields runs on indexes, single-valued attribute
equality on the GIN index, the other conditions on the range's rows.

Cursors are opaque, versioned and bound to installation and filter. They are
pagination state, not authorization credentials. All reads require the same
authenticated console session independently of the cursor.

| Code | Handling |
| --- | --- |
| `InvalidArgument` | Correct the filter, range, limit or mismatched/malformed cursor. |
| `Unavailable` | Retry the read after the database recovers. |
| `Canceled` / `DeadlineExceeded` | Stop or reconnect according to the caller's lifecycle. |

Session authentication/revocation follows the common console contract.

## Deployment retention

`BACKPLANE_AUDIT_RETENTION=0s` (default) retains everything. A positive duration
enables hourly cleanup, at most 5000 entries per pass. Only a contiguous delivered
prefix older than the cutoff is removed; an undelivered entry blocks removal of
later entries. Entries, outbox and cursor floor change in one transaction.
Retention is neither editable nor displayed in UI. It applies to the control
audit only: application audit is never expired by backplane.

## Application audit

An application audits what happens in it by writing an ordinary OpenTelemetry
log record with the attribute `backplane.audit=true` (a boolean). The log goes
wherever logs go; the deployment's Collector also forwards every marked record
to backplane, which keeps it in PostgreSQL with no expiry. Nothing is required
of the application beyond the label, so third-party services (an identity
server's audit log, say) are audited by labelling their records in the
Collector.

| Attribute | Meaning |
| --- | --- |
| `backplane.audit` | `true`: the record is audit. Required; anything else is not stored. |
| `event.name` | The action (`identity.deleted`); else the record's `event_name`, else its body cut to 256 characters. |
| `backplane.audit.actor` | Who did it (`user:7`). |
| `backplane.audit.subject` | What it was done to (`identity/42`). |
| `backplane.audit.outcome` | `succeeded`, `failed`, `rejected` by convention. |
| `backplane.audit.id` | Deduplicates retries within the service; without it the record's content (with its resource) does. |

Every other attribute, the resource, severity, body and trace/span ids are kept
as sent and are filterable. The Go SDK names these keys (`backplane.AuditLabel`,
`AuditActor`, …) and `backplane.Audit()` is the label as an `xlog` field:

```go
log.Info("identity deleted", backplane.Audit(),
    xlog.String("event.name", "identity.deleted"),
    xlog.String(backplane.AuditSubject, "identity/"+id))
```

### Delivery

The Collector's backplane pipeline (`deployments/otel-collector.audit.yaml`,
merged with the main configuration) keeps the marked records, and exception
records with them ([errors.md](errors.md)), and exports them over
OTLP/gRPC (`opentelemetry.proto.collector.logs.v1.LogsService/Export`) to
backplane's audit listener, `BACKPLANE_AUDIT_LISTEN` (`:4317`). It is internal:
never publish it through the direct service listener. With `BACKPLANE_AUDIT_KEYS` (a JSON array of
keys, each at least 16 characters) the listener requires one as
`authorization: Bearer <key>`.

A call returns after the records are committed; any failure is `Unavailable`,
and the Collector's persistent queue (`file_storage`, retries without a time
limit) keeps them until backplane is back. Retries are deduplicated. Exception
records go to the error store and are not audit. Records
without the label, over 64 KiB, or containing text PostgreSQL cannot store
(NUL or invalid UTF-8, including attribute keys and nested values) are counted
in `partial_success.rejected`
and not stored: a Collector filter that forwards everything shows up there and
in the metric `backplane.audit.ingest.records{outcome=stored|duplicate|rejected}`.
Valid records in the same batch are still committed. Non-finite OTLP doubles
are preserved as the JSON strings `NaN`, `Infinity` and `-Infinity` in bodies,
attributes and resources; they do not turn a batch into an endlessly retried
database failure.
The pipeline needs the `filter` and `attributes` processors and the
`file_storage` extension (otelcol-contrib or a custom build).

Backplane's public OTLP proxy adds `X-Backplane-Ingest: proxy` to everything
it forwards (browsers, ingest keys that are public by nature). The audit
pipeline receives it as client metadata (`include_metadata`), sets it as the
`backplane.ingest` attribute and drops marked records carrying `proxy`: a
browser cannot write audit. Services that send to the Collector directly are
trusted as the Collector trusts them; restricting who can reach the
Collector is the deployment's.

### Reading

Application records are part of the feed ([Methods](#methods)): filter
`source is application`, an attribute (`tenant is acme`), a service. The
console's Audit page shows both sources in one table with a histogram, value
counts per field and a record drawer with a link to the record's trace.

## Acceptance

`TestRecords` and `TestAuditFeedLive` (internal/audit, against PostgreSQL):
labels, action fallbacks, keys, deduplication of retries, both sources in one
feed, every operator on fields and attributes (a platform entry's detail
too), text, range, pages, facets, fields and the histogram. `make test-dev` reads a greeting
hello audits through the Collector in the console.

`make test-audit`: rollback atomicity, commit ordering across replicas, durable
intent before dispatch, metadata exclusion/outcome handling, failed publisher
recovery, expired lease takeover, stale acknowledgments, consistent pages,
resume/filter validation and actual retention/cursor expiry against PostgreSQL.

`make test-replicas`: real backplane processes, crash/rejoin, configuration,
bindings/rules/sessions/calls and matching audit records received by an OTLP collector. `make test-m1` and `make test-m2` cover existing
control workflows with transactional audit enabled.
