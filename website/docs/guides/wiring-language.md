# Wiring language reference

Bindings and rules use protobuf JSON definitions, commonly written as YAML. Keys use camelCase. The graph editor and YAML editor modify the same definition; `editor` layout metadata is stored but ignored by execution.

## Definition and step fields

| Field | Meaning |
| --- | --- |
| `hook` / `event` | Fully qualified trigger identity for a binding / rule |
| `steps` | Map from step identifier to definition |
| `result` | Binding output expression tree |
| `when` on a rule | CEL condition before executing the rule |
| `description` | Human-readable text |
| `editor` | Graph node positions and notes |

| Step field | Meaning |
| --- | --- |
| `activity` | Fully qualified target activity; omitted for a loop body made of steps |
| `input` | Expression tree passed to the activity; absent means `{}` |
| `when` | CEL boolean; false skips the step/item |
| `after` | Explicit additional step dependencies |
| `undo`, `undoInput` | Compensation activity and its input; absent undo input uses the completed step output |
| `retry` | `attempts`, `initialInterval`, `maxInterval`, `backoff` |
| `startToClose`, `heartbeat` | Attempt and heartbeat timeout |
| `forEach`, `as` | List/map expression and item variable name |
| `concurrency`, `onError`, `maxItems` | Loop execution bounds and failure behavior |
| `steps`, `result` inside a loop | Nested subflow and per-item result, instead of an activity body |

Step names are identifiers, not arbitrary labels. Reserved trigger variables, CEL names and enclosing scope names cannot be reused. Prefer names describing the produced value, because other expressions reference them directly.

## Expression values

Every string in `input`, `undoInput` or `result` is a CEL expression. Objects are constructed recursively, arrays element by element, and numbers/booleans/null are literal values:

```yaml
input:
  name: req.name
  prefix: "'Hello'"
  enabled: true
  retries: 3
  tags: ["'welcome'", req.group]
```

`"'Hello'"` is a literal string. Plain `Hello` would mean a CEL variable named `Hello`.

Bindings expose `req`. Rules expose `event` and CloudEvents metadata in `meta`. A completed step's output is available by its name. `steps.<name>.skipped` reports conditional skipping. Loops expose `item` by default (or the `as` name), and `<name>Index`.

CEL evaluation is deterministic and has no I/O, clock or randomness. Supported extensions include strings, encoders, math, lists and optional values, for example `req.?nickname.orValue("")`. Input/output schemas inform numeric and field types. Without a schema some errors remain runtime errors rather than editor errors.

## Dependency graph

Execution order comes from expression reads and `after`, not YAML key order. A step starts when its dependencies finish. Cycles are invalid. A skipped ordinary step produces an empty object; a dependent expression must handle missing fields intentionally. `undoInput` can refer only to its own step and ancestors.

On failure, compensation runs for completed effects in reverse completion order. This is a saga, not an atomic rollback. Compensation can itself fail and must be designed to be safe under retries.

## Loops

This illustrative flow assumes the named service activities are declared by your installation:

```yaml
hook: notifications.SendMany
steps:
  sent:
    forEach: req.recipients
    as: recipient
    concurrency: 5
    maxItems: 500
    onError: continue
    when: recipient.email != ""
    activity: mail.Send
    input:
      to: recipient.email
      subject: req.subject
result: {deliveries: sent}
```

Lists keep item order. Maps produce `{key, value}` items in key order. Loop output is a list in input order; skipped items and failures under `continue` occupy null slots. `steps.<loop>.failed` and `.errors` expose collected failures.

Concurrency defaults to 10 and is bounded to 100. `maxItems` defaults to 1000 and is bounded to 10000. `onError: fail` stops scheduling new items after the first failure, waits for in-flight work and fails the run. `continue` collects errors and allows the loop to succeed.

A loop can contain a nested `steps` map and per-item `result` instead of `activity`/`input`. Body steps can read their item, siblings and enclosing scope. They must not shadow enclosing names. A failed subflow compensates its completed work; successful subflows contribute their compensation to the enclosing run.

## Timeouts, retries and validation

Unset step options inherit the activity manifest, then platform defaults. Set explicit limits for operations whose safe retry behavior differs from the default. `attempts` counts the first attempt. Duration fields accept the editor's supported duration syntax, such as `500ms`, `30s` and `1m30s`.

Analysis checks declared targets, names, references, cycles, scope, options, CEL types and expression cost. Violations contain a JSON Pointer `path`, machine `code`, readable `message` and optional Unicode-code-point expression range with exclusive end. Important codes include `UNKNOWN_ACTIVITY`, `UNKNOWN_STEP`, `CYCLE`, `UNDO_REFERENCE`, `CEL_ERROR`, `COST`, `TYPE_MISMATCH`, `MISSING_FIELD` and `INVALID_OPTION`.

Saved definitions are re-evaluated against current manifests. Removing a referenced field can make a previously valid definition broken. Rollback is validated again; historical validity is not a guarantee of present compatibility.

For exact protobuf fields see [step.proto](https://github.com/backplane-labs/backplane/blob/master/backplanepb/console/v1/step.proto); for request/error examples see [WiringService](../reference/services/wiring-service.md).
