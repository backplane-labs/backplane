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

## Determinism

Workflow code must use Temporal workflow APIs for time, concurrency and activity execution. Do not read changing process configuration, access a database or publish an event directly during workflow replay. Perform those operations in activities and return their results into the workflow history.

The hello `Welcome` workflow calls the `Greet` hook with `workflows.CallHook`. On success it executes a recording activity; when the hook is unbound it executes the local composition activity. This keeps greeting events and audit on the activity side of the deterministic boundary.

## Schedule ownership

The SDK creates no schedules. Operators use `ScheduleService` or the service Workflows page to create, update, delete, pause, resume and trigger schedules. A service rollout must not recreate an operator-deleted schedule or clear its pause.

Declare stable workflow names and keep scheduled payloads compatible across rolling versions. Before removing a workflow, inspect schedules and in-flight executions that still reference it. Revision checks protect concurrent administrative updates; read the latest definition after a conflict rather than overwriting it blindly.

## Run identity and retries

Use workflow IDs deliberately. A transport timeout after a start command does not prove the workflow failed to start. Read the run and [operation audit](../guides/audit.md) before issuing another start. Activity retries can repeat external side effects, so the activity must honor a business idempotency key where needed.

See [workflow operations](../guides/workflows.md), [WorkflowService](../reference/services/workflow-service.md) and [ScheduleService](../reference/services/schedule-service.md) for the current API.
