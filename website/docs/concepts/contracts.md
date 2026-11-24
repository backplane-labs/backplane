# Service contracts

A service publishes a versioned **platform manifest**: its identity, configuration schema and live paths, internal console service names, plugin bundle identity and optional telemetry selectors. Instance state reports where a running replica listens, its phase, transports, component health and applied configuration. A manifest is not a registry of application messages.

## Operator calls

A module owns its generated client and message schema. Backplane authenticates the operator, finds the unique owner of the internal gRPC service and relays the bytes. A public RPC cannot be reached through that relay. The service implements and authorizes the operation itself.

## Working traffic

An application chooses the protocol, encoding, retries and business semantics of its own calls and messages. Adding a product RPC does not require a Backplane release. Service authors may use native NATS and Temporal clients, or neither. Register a named listener for address discovery without describing what it serves.
