# Investigate application errors

Errors are OpenTelemetry log records in the configured log store. Backplane reads exceptions, stacks and related telemetry without creating another error database. Retention follows the log store. The current feature does not group occurrences into issues or delete them.

## Capture

Go services can call `backplane.CaptureError(ctx, log, err, fields...)`. SDK recovery paths also report supported handler/activity panics. Keep the active trace context when reporting so an occurrence can link to its trace.

Browser applications can use `@backplane-labs/backplane-errors` for exception capture, registered state, breadcrumbs and OTLP delivery. This package can run outside the console. It supports sanitization, filtering, rate limits and an optional IndexedDB outbox. Treat browser ingest keys as public admission keys, never operator credentials.

Embedded modules use the host's error reporter. Their handled occurrences can include the module identity; their unhandled/render errors belong to the host capture boundary. Avoid installing duplicate global capture hooks in every module.

## Investigate

1. Open Errors or the service's Errors tab.
2. Set a time range and filter by service, environment, type, message or release.
3. Select an occurrence to inspect its cause chain and parsed stack.
4. For browser SDK records, inspect captured state and bounded history.
5. Follow trace and related logs by span, trace, runtime or service.
6. Compare raw stored fields if the payload has validation warnings.

Malformed SDK envelopes are reported as warnings rather than hiding the entire stored occurrence. Stacks remain text; source-map symbolication is not part of this implementation. A missing trace can mean independent retention, sampling or delayed ingestion.

See [capture and query reference](../reference/errors.md) and [browser SDK](../reference/browser-errors.md) for complete options and examples.
