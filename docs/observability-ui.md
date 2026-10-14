# Observability presentation

`@backplane-labs/backplane-observability-ui` contains generic `LogViewer`, `TraceList`,
`TraceWaterfall`, `SpanDetails`, `AttributeTable`, `CorrelationLinks`, `StackTrace`,
`SourceContext` and `ErrorDetails`. It has no query client, error grouping engine,
issue workflow or service deployment knowledge. English resources are exported
as `observabilityEnglish` under `backplane.observability`.

Callers supply records with stable IDs and own query/loading/error/partial state.
Log columns are virtualized, sortable/filterable and configurable through the
shared data table. Selection is keyed by ID across updates. Search highlighting
renders plain text, including markup from log bodies. Detail dialogs show original
body, attributes and resource fields, with caller-owned surrounding-log and
correlation callbacks. Correlation never derives a relationship from a namespace;
trace/span IDs and explicit resource values pass to the caller unchanged.

Span timestamps are decimal Unix nanosecond strings. Formatting uses only the
millisecond part for the timezone calendar and appends the original nine-digit
fraction. Duration subtraction and waterfall offsets use bigint. Only the bounded
relative percentage becomes a JavaScript number for CSS positioning. Invalid or
reversed times are reported. Tree construction is iterative and repairs cycles,
duplicate IDs and missing parents visibly. Depths over 64 are split into additional
roots so the table renderer cannot recurse through an unbounded parent chain.
Unique span records remain inspectable. A waterfall input represents one trace;
IDs must be unique within it. Status text accompanies error coloring.

Span details include resource/attributes, events, links and correlation controls.
Stack frames use a virtualized table. Activating a frame displays at most 21
caller-supplied source lines around its location. Source paths are text, never
automatically fetched. Nested causes are limited to 50 and cycle checked; any
truncation is explicit. Structured payloads use the lazy JSON viewer and preserve
bigint as decimal text.

Storybook fixtures exercise 10,000 logs, 2,000 spans across two services, missing
and cyclic parents, event/link details, source context and nested causes in both
themes. Error queries and envelopes belong to platform-ui's Errors
([errors.md](errors.md)); these components only present.

`humanDuration` formats nanosecond spans (850 ns, 12.4 µs, 3.21 ms, 1.50 s);
`spanForest` and `spanPosition` are exported for custom trace views such as the
platform `TraceView`.
