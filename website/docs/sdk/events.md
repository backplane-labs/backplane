# Events and reactors

Events express asynchronous facts. Declare and publish a typed event under its owning component:

```go
type Greeted struct {
    Name string `json:"name"`
    Count uint64 `json:"count"`
    Text string `json:"text,omitempty"`
}

greeted := event.Declare[Greeted](component, "Greeted",
    event.Describe("a name was greeted"))

err := greeted.Publish(ctx, Greeted{Name: name, Count: count, Text: text},
    event.Key(name), event.ID(stableID), event.Header("source", "http"))
```

NATS JetStream is required for delivery. Decide explicitly whether publication failure should fail the business request, be retried through a durable outbox or be logged as a best-effort failure. The hello example chooses best effort; that is not a universal reliability guarantee.

## React to an event

```go
event.React(component, "hello.Greeted", handle,
    event.Consumer("formatter-greeted"),
    event.MaxDeliver(3), event.Concurrency(2),
    event.Timeout(5*time.Second),
    event.Redelivery(time.Second, 10*time.Second),
    event.StartAt(event.StartNew))
```

An event payload is encoded and described as a hook's ([payloads](hooks.md#payloads)). `handle` receives your locally defined compatible payload type. `event.DeliveryOf(ctx)` exposes the event ID, attempt and extension metadata. Use a stable consumer name to retain delivery position across component refactors.

## Idempotency and failures

Delivery can repeat. Persist a processed-event identity together with the business mutation when exactly-once business effects matter. A bounded in-memory set, as used in the examples, survives neither process restart nor an arbitrarily long redelivery delay.

Return `event.Terminal(err)` when retries cannot fix the event, such as missing required domain data. Transient failures follow the configured delivery policy. Exhausted/terminal work can be inspected through the dead-letter API and redriven intentionally after correcting the cause.

`StartNew` starts with new events; it is not historical replay. Choose consumer start policy before production use and preserve the durable identity during rollout.

## Reactors versus rules

A reactor is service-authored code consuming an event. A rule is operator-authored wiring triggered by an event and executed durably through Temporal. They can coexist and observe the same event independently. The hello rule excludes `name=skip`, while formatter's reactor still receives it.

See [event operations](../guides/events.md) for inspection, test publication and redrive.
