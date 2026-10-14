# Investigate application errors

Errors are OpenTelemetry exception records that the Collector forwards to Backplane, which stores each occurrence whole in its own database, grouped by fingerprint, for a configured retention (90 days by default). Groups are triaged in place: open, resolved or muted, every change audited. Related logs and the trace are read from the telemetry stores when they are configured.

## Capture

Go services can call `backplane.CaptureError(ctx, log, err, fields...)`. SDK recovery paths also report supported handler/activity panics. Keep the active trace context when reporting so an occurrence can link to its trace.

Browser applications can use `@backplane-labs/backplane-errors` for exception capture, registered state, breadcrumbs and OTLP delivery. This package can run outside the console. It supports sanitization, filtering, rate limits and an optional IndexedDB outbox. Treat browser ingest keys as public admission keys, never operator credentials.

Embedded modules use the host's error reporter. Their handled occurrences can include the module identity; their unhandled/render errors belong to the host capture boundary. Avoid installing duplicate global capture hooks in every module.

## Investigate

1. Open Errors or the service's Errors tab. Groups come first: one row per distinct error with its trend, counts and whether it is new or unhandled.
2. Set a time range and filter by service, environment, type, message, release, handled or ingest.
3. Open a group for its counts, environments, releases, trend and occurrences; switch to Occurrences to list every one.
4. Resolve a group once fixed, or mute one that is known noise, with a note: it leaves the open list (the State select shows resolved, muted or all). A new occurrence after a resolution reopens the group marked "Regressed"; a muted group keeps counting silently.
5. Select an occurrence to inspect its cause chain and parsed stack.
6. For browser SDK records, inspect captured state and bounded history.
7. Follow trace and related logs by span, trace, runtime or service.
8. Check Environment for the resource, attributes, scope and how the record came in, and Fields for every stored value; "Copy report" writes a summary for a ticket.
9. Share the URL: the open group, occurrence and state filter are in it.

Malformed SDK envelopes are reported as warnings rather than hiding the entire stored occurrence. Past a group's burst limit occurrences are counted but not stored; a record over the size limit is cut and marked truncated. Stacks remain text; source-map symbolication is not part of this implementation. A missing trace or related log can mean the telemetry store's retention, sampling or delayed ingestion.

See [capture and query reference](../reference/errors.md) and [browser SDK](../reference/browser-errors.md) for complete options and examples.
