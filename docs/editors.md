# Editors

`@backplane-labs/backplane-editors` exports lazy CodeMirror controls: `CodeEditor`,
`InlineCodeEditor`, `QueryEditor`, `JSONViewer`, `DiffViewer` and `EditorActions`.
The author supplies an accessible label, controlled string value and theme mode.
English resources are exported as `editorsEnglish` for `backplane.editors`.
CodeMirror and its merge view are separate dynamic imports.

Query language choices are explicit source capabilities. CEL, MetricsQL, PromQL,
LogsQL and TraceQL have lexical highlighting and keyword completion. Highlighting
does not establish that an expression is valid: callers supply authoritative
`validate` and `complete` callbacks. These receive cancellation signals; stale
results are discarded. Completion output is bounded to 500 entries, diagnostic
output to 1,000 entries. Ranges are UTF-16 code-unit offsets and are clamped to
the document. JSON uses CodeMirror's JSON parser diagnostics; YAML has syntax
highlighting and relies on the caller for diagnostics.

`complete` may answer a list (replacing the word before the cursor) or
`{ from, options }` (replacing from an explicit offset: a field after `x.`, a
YAML key). Without `validate`, the `diagnostics` prop is the lint result, so a
lint pass never clears them. `hover(position)` gives a tooltip text, `reveal`
selects and scrolls to a range whenever its `key` changes, `onCursor` reports
the cursor, `height="fill"` takes the container's height and `hideMessages`
leaves the list of diagnostics to the host.

Ctrl/Cmd+Enter runs a query. Escape followed by Tab leaves the editor; Tab otherwise
indents. Completion, readonly editing, cancellation, language changes, both themes
and accessibility are checked in Storybook. Diagnostic/suggestion failure keeps
the text and reports unavailability. No query or mutation is automatically retried.

The editable value and diff inputs are strings. Raw JSON text preserves numeric
lexemes above 2^53 without parsing them through `Number`. For object previews,
bigints become decimal strings; use protobuf JSON if type distinctions must be
represented. Preview text is escaped by React/CodeMirror. Copy/download operates
on the original string, not the rendered syntax spans.
