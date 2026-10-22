# Typed and live configuration

Embed `config.Backplane` in your application configuration and give domain fields JSON names. Those names become stable configuration paths.

```go
type GreeterConfig struct {
    Salute string `json:"salute" schemapb:"default=Hello" desc:"Word before the name."`
    Suffix config.Live[string] `json:"suffix" schemapb:"default=!" desc:"Greeting suffix."`
    Excited config.Live[bool] `json:"excited" schemapb:"default=false" desc:"Add an exclamation."`
}

type Config struct {
    config.Backplane `json:"backplane"`
    Greeter GreeterConfig `json:"greeter"`
}
```

The `desc` tag is the field's description: the console shows it under the
field. Every field of the SDK's own sections (`backplane`, and the `nats`,
`postgres`, `temporal`, `valkey` connection sections) has one.

Normal fields are startup configuration. `config.Live[T]` marks fields that can change without restarting. Read them with `Get()` at the point of use; copying a live value into an ordinary field would freeze it.

```go
suffix := cfg.Greeter.Suffix.Get()
cfg.Greeter.Suffix.Watch(func(value string) {
    // React to an accepted value; keep callbacks short.
})
```

Values that only make sense together belong in one live struct: it changes as
a whole, so one `Get()` reads a consistent pair and a watcher sees each pair
once. The console stores it as one value and writes it in one transaction.

```go
type Limits struct {
    Min int `json:"min" schemapb:"default=1"`
    Max int `json:"max" schemapb:"default=10"`
}

func (l Limits) Validate() error {
    if l.Min > l.Max {
        return fmt.Errorf("min %d above max %d", l.Min, l.Max)
    }
    return nil
}

type Config struct {
    config.Backplane `json:"backplane"`
    Limits config.Live[Limits] `json:"limits"`
}
```

A field the update leaves out takes its default. `Validate` on the value's type
runs like a section's, so a pair it refuses is never applied. Separate
`Live[int]` fields, by contrast, change one at a time and a reader can see one
new bound with the other old.

A live value that has to reach something outside the process (a pool size, a
client, a limiter) is carried there by `Apply`, before anyone sees it:

```go
cfg.Pool.Apply(func(size int) error {
    return pool.Resize(size) // an error rejects the update
})
```

Appliers run after `Validate`, on every update (the value loaded at start is
the component's to apply). An error or a panic rejects the whole revision: no
live field changes, `Watch` callbacks do not fire, appliers that already took
it are called again with their previous value, and the instance reports the
revision rejected with `apply <field>: <error>`, as a failed `Validate` does.
The SDK does not retry; the next revision is applied as usual.
`backplanetest.SetLive` runs the appliers too and returns their error.

Live fields are the service's own settings. The SDK's connections (the
`backplane` block's NATS, Temporal and Consul, and the `infra` PostgreSQL,
Valkey and NATS sections) are startup configuration: a new address or
credential takes a restart. The block's one live field is `log_level`.

## Sources and precedence

The service combines file configuration, environment overrides and the distributed live configuration source. Environment variables override file values. The Consul source is filtered to declared live paths, so an operator cannot use live configuration to rewrite a startup-only listener or credential. See [exact configuration names](../reference/configuration.md).

Schemas use the shared schemapb runtime for defaults, constraints, CEL validation and masking. Keep schema annotations and application validators consistent. Configuration schema construction errors should be fixed before deploying the service.

## Domain validation

Implement `Validate() error` for constraints beyond field annotations. The hello example rejects an empty salute and a suffix longer than eight characters or containing a newline. Validation runs at startup and on candidate live updates; a rejected live candidate must not become the active value.

A server can save a schema-valid revision that an instance rejects with its custom validator. The console therefore exposes per-instance desired/applied/rejected state. Application acceptance is a separate step from database persistence.

## Secrets

Use secret annotations and deployment secret injection. Do not copy a masked value back as if it were the secret. Keep startup-only secrets outside the operator's live-edit surface. Custom validation errors and watch logs must not disclose values merely because the schema masks them elsewhere.

For the operator workflow, optimistic revisions and rollback, see [change configuration](../guides/configuration.md).
