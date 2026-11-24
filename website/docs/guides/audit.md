# Investigate durable audit

Audit combines platform control entries and trusted application audit records in a PostgreSQL-backed feed. It remains available when NATS or telemetry storage is disabled.

## Platform operations

Configuration changes and session mutations write their platform audit record in the same database transaction. Historical records remain readable. A module reports application decisions from its own service; the authenticated relay never decodes the payload for audit.

## Application audit through OTel

An application emits an ordinary log with boolean `backplane.audit=true`:

```go
log.Info("identity deleted", backplane.Audit(),
    xlog.String("event.name", "identity.deleted"),
    xlog.String(backplane.AuditSubject, "identity/"+id),
    xlog.String(backplane.AuditOutcome, "succeeded"))
```

The trusted Collector pipeline forwards marked records to Backplane's internal audit OTLP/gRPC listener. It commits before acknowledging and deduplicates retries. Third-party logs can receive the label in the Collector without changing the application.

Public browser OTLP is marked by the proxy and excluded from this audit pipeline. Exposing the internal audit listener publicly would bypass that trust boundary. Preserve the supplied pipeline separation and restrict direct Collector access to trusted senders.

## Read the feed

Use text, time range and field/attribute filters. The histogram supports narrowing a period; facets expose frequent values. Service views pin the relevant service, while installation-wide session entries have no service.

`SearchAudit` returns newest-first pages with opaque filter-bound cursors. Continue older pages using the same filter. A live view refreshes the first page; there is no `WatchAudit` replay stream. Keep exact IDs/timestamps and distinguish platform/application sources.

Application audit has no platform expiry. Control-audit retention is deployment-configured and defaults to retaining everything. Optional export outage can delay cleanup of pending records. See [complete persistence, ingest and API reference](../reference/audit.md).
