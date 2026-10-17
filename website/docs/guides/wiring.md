# Connect hooks and events

Bindings implement declared hooks. Rules react to declared events. Both use the same typed step language and activity catalog, with YAML and graph editors over one draft.

## Bind hello to formatter

With the [example installation](../quickstart.md) running, open Wiring and select `hello.Greet`. This is the actual demo binding:

```yaml
hook: hello.Greet
description: Greets through the formatter service.
steps:
  formatted:
    activity: formatter.Format
    input: {name: req.name}
result: {text: formatted.text}
```

`req` is the hook input. A step's output is available by its name (`formatted`). String values in expression trees are CEL expressions; use a quoted CEL string literal when you mean constant text rather than a field reference. Structured objects and arrays preserve their shape.

Analyze the definition, correct reported violations, test it with a name, then save. The demo setup already installs this binding, so edits should use its current version.

## Add an event rule

```yaml
event: hello.Greeted
description: Records every greeting except "skip".
when: event.name != "skip"
steps:
  recorded:
    activity: formatter.Record
    input: {name: event.name, text: event.text}
```

`event` is the payload, not its transport envelope. Metadata is available through the rule's declared environment. A rule needs at least one step. It requires both event delivery and Temporal execution. Pausing it changes operator state; a reactor written in a service remains an independent consumer.

## Compose a flow

Data references establish dependencies between steps. Explicit `after` adds ordering where no value flows. `when` conditionally runs a step. Activity options control retries and timeouts; compensation (`undo`) must be designed for the actual external effect, not assumed to be a transaction rollback.

For-each steps run an activity or a subflow over a list. Choose item naming, concurrency, error policy and a limit deliberately. Body steps see their item and allowed enclosing values. A bounded parallel loop can still create substantial downstream load; set concurrency to the capacity of the target operation.

The graph shows data, condition and explicit-order edges differently. Dragging ports writes references into the same YAML draft. Nested loops are frames; run overlays show per-item progress. Renames use server analysis rather than string replacement.

## Retries

Every step has a retry policy: `attempts` (default 3), `initialInterval`
(1s), `maxInterval` (30s) and `backoff` (2). Both runtimes run it per step
and per for-each item: a failed item is retried alone, the items that
succeeded are not called again. Every attempt carries the same idempotency
key (`activity.InfoOf(ctx).Key`) and a growing attempt number.

An activity decides what is retried: `activity.NonRetryable(err)` fails the
step at once, `activity.RetryAfter(err, d)` asks for the next attempt no
sooner than `d` (a rate limit, a `Retry-After` header), anything else is
retried by the policy. When the attempts run out, the run fails; a rule's
run is then retried as a whole by event redelivery.

## Validate, test, publish

1. The editor checks syntax and shape immediately.
2. `WiringService` analyzes expressions against current manifests and reports source locations, expected types and reads.
3. Test runs execute the draft; they can cause real activity side effects.
4. Save uses a base version. On conflict, compare with the latest version before continuing.
5. Inspect the run and its step timeline. A successful save only publishes the definition.

History supports review and rollback. A removed/changed service contract can invalidate an existing definition; the UI marks broken definitions rather than silently rewriting them. Unsaved drafts survive switching items and require confirmation before leaving.

## Keep wiring in files

An installation can keep bindings and rules in YAML files instead of saving
them from the console. Point `BACKPLANE_WIRING_DIR` at a directory: a
mounted Kubernetes ConfigMap, a directory baked into the image, a checkout.
Every replica reads it at start, every `BACKPLANE_WIRING_POLL` (5s) and on
every new manifest, and keeps the platform's versions equal to it. You do not
need an init container or a job: an entry waits until the services it names
register, then applies.

```yaml
# wiring/kratos.yaml
bindings:
  - hook: kratos.Deliver           # a BindingDefinition, as the YAML editor shows it
    steps:
      rendered:
        activity: templates.Render
        input: {template: "'kratos.' + req.template_type", data: {code: req.code}}
      mail:
        activity: courier.SendMail
        when: req.channel == "email"
        input: {to: req.to, subject: rendered.subject, text: rendered.text, html: rendered.parts.html}
---
rules:
  - name: record greetings         # the rule's identity: renaming it replaces the rule
    event: hello.Greeted
    when: event.name != "skip"
    steps:
      recorded: {activity: formatter.Record, input: {name: event.name, text: event.text}}
```

The directory is read recursively. Only `*.yaml` and `*.yml` files are read, and
names starting with a dot are skipped, so a ConfigMap's `..data` links work as
they are. A file may hold several documents. A hook or rule name may appear in
one file only.

What the files define, the console only reads:

- A changed entry becomes the next version. Its author is `wiring` and its
  comment `from <file>`. The version and the editor show the file it came from.
- The console's Save, Delete and Rollback are refused for that binding or rule
  (`FAILED_PRECONDITION`). Drafts can still be edited and tested. Pausing a
  rule stays the console's.
- An entry removed from every file is deleted (a tombstone,
  `removed from <file>`), and the console owns that hook or rule again. Moving
  an entry between files only changes the file it is attributed to.
- A definition that does not compile waits. The version in force stays, and
  the platform logs why once. If everything it lacks is an undeclared hook,
  event or activity, it waits at `info` level for the service to register.
  Any other violation is logged at `warn`.
- If the directory does not read whole (a YAML error, an unknown key, a
  duplicate name), the platform changes nothing until it is fixed. An empty
  directory, on the other hand, means "no wiring" and deletes everything the
  files defined before.

To roll back, revert the file. History stays in the platform as for any version.

See the [language reference](wiring-language.md), [WiringService](../reference/services/wiring-service.md), [BindingService](../reference/services/binding-service.md), [RuleService](../reference/services/rule-service.md) and the [editor behavior reference](../reference/platform-ui.md#wiring).
