# Service contracts

A manifest is the catalog's description of a service version. It is not a deployment specification: ports and routing metadata describe a running service, while the deployment remains responsible for starting it and making advertised addresses reachable.

| Contract | Meaning | Consumer |
| --- | --- | --- |
| Configuration schema | Valid fields, defaults, annotations and live paths | Console forms and configuration API |
| Hook | A typed capability the service asks the installation to implement | Binding editor and hook caller |
| Activity | A typed operation the service offers to other flows | Bindings, event rules and direct runs |
| Event | A named asynchronous fact with a payload schema | Reactors and event rules |
| Workflow | A durable entrypoint offered by a service | Workflow runs and administrative schedules |
| API | Service protocol, local prefix/port and optional API document | Service API viewer |
| Internal API | Protobuf methods intended for authenticated console access | Generated module clients and relay |
| UI bundle | Module metadata, compatible SDK major and assets | Console module host |
| Telemetry selectors | Optional related source presets | Service-scoped observability views |

## Payload evolution

Platform envelopes are protobuf. Service-defined business payloads are JSON, with optional schemas used for documentation, forms and analysis. Prefer additive changes and tolerate unknown fields. Removing or changing a field used by a binding can invalidate that binding even when the transport remains compatible.

Names are stable identifiers. Renaming an event or durable consumer creates a different delivery identity; moving source files does not justify changing one. Pin reactor consumer names where continuity matters.

Do not put secrets in identifiers. They can appear in catalog entries, workflow IDs and audit metadata. Configuration secret annotations mask fields in supported views; arbitrary event and activity payloads are not automatically safe to expose.

## Validation boundaries

The platform checks definition shape, available manifest contracts and CEL expressions. A service remains responsible for domain validation. Schema acceptance at the server is not proof that a service's custom `Validate` method will accept a live update.

Missing schemas are different from empty objects. UI consumers must show a raw input or an explicit unavailable state when type information is absent. Recursive and imported API types retain references instead of being silently flattened away.

## Exact values

Protobuf `int64`/`uint64` become `bigint` in generated TypeScript. Keep nanosecond timestamps, revisions and large identifiers exact. Do not cast them through JavaScript `Number` for storage or request construction. Telemetry JSON can also contain integers above `2^53`; trace views use lossless parsing and retain the raw response.

## Change workflow

1. Update the owning service's declarations and schemas.
2. Regenerate protobuf clients when the RPC contract changes.
3. Build a new service version with consistent manifest metadata.
4. Check existing bindings, rules and module compatibility before rollout.
5. During rollout, inspect instance configuration status and invalid-definition indicators.

See [API document bundles](../sdk/service-api.md) for multi-file contracts and [API reference](../reference/api.md) for console RPC semantics.
