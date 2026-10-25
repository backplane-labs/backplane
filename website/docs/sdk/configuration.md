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

A service ships its own defaults with `config.Defaults` (its fields) and
`config.BlockDefaults` (the SDK block), passed through
`backplane.ConfigOptions`. They sit above the schema's defaults and below the
file, the environment and Consul. `backplane.ServerKeepalive(30*time.Second)`
is the block default of `server.grpc_keepalive_time`, for services with long
streams behind NAT; `BACKPLANE_SERVER_GRPC_KEEPALIVE_TIME` still overrides it.

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

A `secret` mark masks a value in the console, and the direct runtime records
runs already masked. Temporal history, NATS and service logs keep payloads as
they were, unless history encryption is on (below).

## History encryption

`BACKPLANE_TEMPORAL_ENCRYPTION_*` (`backplane.temporal.encryption`, a seal
section) encrypts the payloads of workflow history. It is off by default.

```yaml
backplane:
  temporal:
    encryption: {key: ${HISTORY_KEY}}      # or vault: {...}
```

- **What it seals.** A payload codec seals every payload whole, metadata
  included: the inputs and results of workflows, activities, signals and
  hook calls, and schedule inputs.
- **What stays readable.** Temporal UI and `tctl` show ciphertext. The
  console decodes and then masks as before.
- **Hook replies.** Backplane also seals the unmasked reply it keeps for a
  keyed hook call.
- **Same key everywhere.** Every service of the installation and backplane
  must carry the same section: a worker opens what a workflow sealed.
- **Turning it on.** Payloads written before encryption pass through
  unchanged, so it can be turned on without a migration. Turning it off
  leaves the sealed history unreadable.
- **Rotation.** Same as for any seal section: a new `key`, the old one in
  `previous`.
- **Strict mode.** `require_encryption: true` makes workers and clients
  refuse payloads that are not sealed, so whoever reaches the Temporal API
  without the key cannot hand a worker plain input. Turn it on once no run
  from before encryption is left.
- **What it protects.** Confidentiality at rest, against whoever reads
  Temporal's database, backups or UI. A sealed payload is not bound to its
  run (the Temporal SDK's codec sees no workflow context), so whoever can
  write to Temporal could move one between runs; access to the Temporal API
  stays the boundary for that.

## Sealing what a service stores

A service that stores a secret it must read back, such as a receiver's
signing secret, seals it with `pkg/backplane/seal`. The section works like a
connection section and the sealer is a dependency:

```go
type Config struct {
    config.Backplane `json:"backplane"`
    Seal seal.Config `json:"seal"` // <SERVICE>_SEAL_KEY, <SERVICE>_SEAL_PREVIOUS
}

sealer := deps.NewDependency(root, seal.New(cfg.Seal), deps.Name("seal"))
sealed, err := sealer.Get().Seal(ctx, secret, []byte(rowID))
secret, err := sealer.Get().Open(ctx, sealed, []byte(rowID))
```

- **Algorithm.** Values are sealed with AES-256-GCM.
- **Associated data.** The last argument is associated data, such as the
  row's id. It isn't stored, `Open` needs the same value, and a sealed value
  copied to another row won't open there.
- **Key.** `key` is base64 of 32 random bytes (`seal.GenerateKey()`).
- **Rotation.**
  1. Set the new key as `key` and the old one in `previous` (comma-separated
     keys that only open).
  2. `Rewrap` the stored values.
  3. Drop `previous`.

### Vault Transit

The `vault` subsection seals through HashiCorp Vault's (or OpenBao's)
Transit engine instead of `key`:

```yaml
seal:
  vault: {addr: https://vault:8200, token: ${VAULT_TOKEN}, key: komeet, mount: transit}
```

- **Envelope encryption.** Every value gets a fresh data key from
  `transit/datakey`. The value is sealed locally with AES-256-GCM and its
  associated data, and keeps the wrapped data key. `Open` has Transit unwrap
  the key. The Transit key itself never leaves Vault.
- **Token.** The token needs `datakey`, `decrypt` and `rewrap` on the key.
- **A key of its own.** Grant no one `encrypt` on the sealer's Transit key.
  Whoever can encrypt with the key can wrap a data key of their choosing
  and forge a value `Open` accepts.
- **Connection.** Use https. The sealer follows no redirect, so the token
  never goes anywhere other than `addr`.
- **Probe.** The dependency probes `sys/health`.
- **Moving from `key` to Vault.** Keep `key` set while values are moved:
  it still opens what it sealed, and `Rewrap` re-seals those values through
  Vault.
- **Rotating the Transit key.** After a rotation in Vault, `Rewrap` moves
  values to the latest key version and leaves current ones as they are.

For the operator workflow, optimistic revisions and rollback, see [change configuration](../guides/configuration.md).
