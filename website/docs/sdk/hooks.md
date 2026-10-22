# Hooks and activities

A **hook** is an extension point owned by the caller. An **activity** is a capability offered by a service. A binding connects them without making either service import the other's implementation.

```go
type GreetIn struct { Name string `json:"name"` }
type GreetOut struct { Text string `json:"text"` }

greet := hook.Declare[GreetIn, GreetOut](component, "Greet",
    hook.Required(), hook.DefaultTimeout(5*time.Second),
    hook.Describe("greets a name"))
```

Declarations belong to a component during tree construction. The fully qualified identity includes the service (`hello.Greet`). `Required` communicates that the hook expects a binding; it does not implement a local fallback for you.

## Payloads

A payload is a Go struct encoded with `encoding/json`, or a generated proto
message encoded with protojson. The manifest's schema, which bindings and
rules are checked against, describes the payload exactly as it is sent:

- a struct: its `json` names and Go types;
- a proto message: its `SchemaPB()` when protoc-gen-go-schemapb generated
  one, otherwise its descriptor. Fields carry their **proto names**
  (`req.identity_id`), every field is written, unset too (a binding reads a
  zero `seq_no` as `0`), except unset oneof members and `optional` fields;
  enums are their value names (`"STATE_ACTIVE"`), `int64`/`uint64` are JSON
  strings that bindings read as integers, `Timestamp` and `Duration` are
  RFC 3339 and `"1.5s"` strings, wrappers are nullable scalars, `Struct`,
  `Value` and `Any` are free JSON.

A consumer decodes with the same rules: a proto message with protojson
(either field naming is accepted), a struct with `encoding/json` — for a
proto producer's 64-bit integers that is a string field or `json:",string"`.

## Offer an activity

```go
activity.Handle(component, "Format", format,
    activity.StartToClose(10*time.Second),
    activity.Retry(activity.RetryHint{Attempts: 3}),
    activity.Describe("formats a greeting"))
```

Here `format` is your typed `func(context.Context, Input) (Output, error)`. Declare realistic timeouts and retry hints. Mark permanent validation failures with `activity.NonRetryable(err)`. Long operations can send `activity.Heartbeat`; `activity.InfoOf(ctx)` exposes execution metadata including the idempotency key.

## Call and fallback

Call the declared reference from request code using its `Call` method. Handle `hook.ErrNoBinding` explicitly if the service has a local fallback. Propagate other failures rather than disguising an unavailable durable execution as an unbound hook.

In a Temporal workflow use `workflows.CallHook(ctx, ref, input, options...)`. Workflow code must remain deterministic; ordinary network calls belong in activities.

The hello example records the delivered greeting and emits `Greeted` after both bound and local execution. Side effects that describe the final result must run on both paths. An HTTP idempotency key can deduplicate hook execution without automatically deduplicating the whole HTTP handler or its later event emission.

## Skip work for an unbound hook

`ref.Bound()` reports whether a binding may be in force. Backplane publishes each service's bound hooks to Consul KV (`backplane/bindings/<service>`) and the SDK watches its own key, so the answer is local and follows saves in Wiring within seconds. It is `false` only when the published set is known and lacks the hook; without Consul it is `true` and `Call` decides. `ref.BindingChanges(ctx)` signals each change.

Use it to avoid work that matters only for a bound hook — configuring a third-party webhook, building an expensive payload — not as a substitute for handling `hook.ErrNoBinding`: a binding can be deleted between the check and the call. In `backplanetest`, a hook is bound once `Answer` serves it.

## Operator control

Bindings are revisioned definitions. Operators map the hook input to activity inputs with CEL, compose multiple steps and define a result. Use [Wiring](../guides/wiring.md) to analyze, test and publish them. Retrying a request after a lost response can duplicate side effects; inspect its operation/run state before repeating it.
