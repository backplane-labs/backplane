# Workflows and administrative schedules

Import `pkg/backplane/workflows` only in code that intentionally uses Temporal. The core service and generated console clients do not need Temporal types.

```go
workflows.Declare(component, "GreetMany", component.GreetMany,
    workflows.Describe("greets every name"))
workflows.Declare(component, "Report", component.Report,
    workflows.Describe("reports greetings so far"))
```

These typed workflow functions accept `workflow.Context`. Declaring one exposes its contract for administrative runs and schedules. Register its implementation dependencies with `workflows.Register` and the supplied Temporal worker registry. Use `workflows.Activity` when exposing a workflow-backed operation as an activity to bindings.

## Activities of your own workflows

An activity only your workflows run (a saga's step) is registered with
`workflows.HandleActivity`, not `activity.Handle`: it stays out of the
manifest, so no binding or rule can call it, and keeps everything else of
`activity.Handle` — payload encoding, `activity.InfoOf` (idempotency key,
attempt, deadline), `activity.Heartbeat`, `activity.NonRetryable`,
`activity.RetryAfter`, tracing, metrics and panic recovery.

```go
charge := workflows.HandleActivity(component, "ChargeCard", component.Charge)

// in workflow code
ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
receipt, err := charge.Execute(ctx, card)
```

Timeouts and the retry policy are the workflow's (`workflow.ActivityOptions`).
The name shares the service queue's activity names with `activity.Handle`;
one taken twice panics at declaration. `workflowtest.New` registers these
activities too.

## Reject newer payloads during rolling updates

Declare optional guards once per service, before `Run`:

```go
activity.GuardInputs(root, checkActivityJSON)     // func(context.Context, string, []byte) error
workflows.GuardWorkerPayloads(root, checkPayloads) // func([]*commonpb.Payload) error
```

`GuardInputs` receives the activity name and read-only JSON before either
`activity.Handle` or `workflows.HandleActivity` decodes it. Return the same
errors as from an activity implementation (`activity.NonRetryable`,
`activity.RetryAfter`, or Temporal's `ApplicationError`). A refusal never
invokes the handler. Accepted protobuf inputs use strict `protojson`
decoding; a decode failure is final. Numeric enum values still need the
service's own compatibility check. Without opt-in, decoding stays lenient.
Wire encoding, per-attempt deadlines and retry policies are unchanged.

`GuardWorkerPayloads` checks read-only Temporal payloads after decryption
and before the main worker's usual decoder. It does not change encoding,
the normal client, or the hook worker. Both guards must be safe to call
concurrently. Treat the payload guard as a deterministic check against the
worker's own build: no external calls or mutable state, and pass through
payload types the service does not own. A refusal on a supported workflow
decode path fails only the current Workflow Task, not the run. Temporal's
signal, update argument, query and memo paths do not get task-only retry
semantics. A retry does not guarantee a different poller.

## Calls between services of one product

Services of one product are released together, so they may call each other
through Temporal directly: an activity or a workflow runs on the owner's task
queue (the owner's service name), where only the owner's worker has it.

```go
// billing declares activity.Handle(root, "ChargeCard", ...) and
// workflows.Declare(root, "Settle", ...)
charge := workflows.ActivityOf[billing.ChargeIn, billing.ChargeOut]("billing", "ChargeCard")
settle := workflows.WorkflowOf[billing.SettleIn, billing.SettleOut]("billing", "Settle")

// in orders' workflow code
receipt, err := charge.Execute(ctx, in) // ctx with workflow.WithActivityOptions
out, err := settle.ExecuteChild(ctx, in) // ctx with workflow.WithChildOptions
// outside workflow code
run, err := settle.Start(ctx, c, in, client.StartWorkflowOptions{ID: "settle-" + id})
```

A code generator can emit these refs with the owner's types. A product
reaches an integration (courier, templates, kratos, ...) only through hooks:
the installation decides what serves them.

Each service is its own backplane service, opened with its own
`backplane.Name`: its queue, manifest, activities and registration are its
own. Several services may share one binary, with one service per process
(`<binary> serve <service>`), since a process holds one service's
telemetry, ports and lifecycle.

## Determinism

Workflow code must use Temporal workflow APIs for time, concurrency and activity execution. Do not read changing process configuration, access a database or publish an event directly during workflow replay. Perform those operations in activities and return their results into the workflow history.

The hello `Welcome` workflow calls the `Greet` hook with `workflows.CallHook`. On success it executes a recording activity; when the hook is unbound it executes the local composition activity. This keeps greeting events and audit on the activity side of the deterministic boundary.

## Schedule ownership

The SDK creates no schedules. Operators use `ScheduleService` or the service Workflows page to create, update, delete, pause, resume and trigger schedules. A service rollout must not recreate an operator-deleted schedule or clear its pause.

Declare stable workflow names and keep scheduled payloads compatible across rolling versions. Before removing a workflow, inspect schedules and in-flight executions that still reference it. Revision checks protect concurrent administrative updates; read the latest definition after a conflict rather than overwriting it blindly.

## Run identity and retries

Use workflow IDs deliberately. A transport timeout after a start command does not prove the workflow failed to start. Read the run and [operation audit](../guides/audit.md) before issuing another start. Activity retries can repeat external side effects, so the activity must honor a business idempotency key where needed.

See [workflow operations](../guides/workflows.md), [WorkflowService](../reference/services/workflow-service.md) and [ScheduleService](../reference/services/schedule-service.md) for the current API.
