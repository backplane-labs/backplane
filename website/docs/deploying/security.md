# Authentication and trust boundaries

Different credentials serve different interfaces. Do not reuse an operator token as a browser ingest key or put an installation secret in a module bundle.

| Interface | Boundary |
| --- | --- |
| Console login | Operator credential exchanged for an HttpOnly cookie |
| Console RPC and protected assets | Session validation and browser origin policy |
| Internal service RPC/assets | Installation secret and private network |
| Calls between services (`api.MTLS`) | Mutual TLS with Consul's CA; Consul intentions decide who may call |
| Public OTLP admission | Optional ingest keys plus bounded admission; browser keys are public |
| Application audit ingest | Trusted Collector network and optional audit keys |
| Telemetry store reads | Deployment-owned backend authorization; never forwarded caller credentials |

## Console

Use HTTPS. Keep the Secure cookie default and preserve WebSocket upgrades through the reverse proxy. Same-origin checks protect browser entrypoints. Configure a console host or prefix consistently with the compiled asset base.

PostgreSQL-backed login protection coordinates attempts across replicas. An unavailable protection backend must not turn into unrestricted login. Operators can inspect/revoke sessions; changing connection state must clear data from a replaced session in the UI.

## Internal services

Protect platform ports and set installation secrets. Rotation supports previous secrets where documented; keep the overlap bounded operationally. A reachable private port without a configured authentication boundary is not safe merely because its route is called internal: serve APIs only services call with [`api.MTLS()`](../sdk/apis.md#calls-between-services-mutual-tls), and protect certificate issuance with Consul ACLs (a token with `service:write` on a service can get its certificate).

The module host runs trusted same-origin JavaScript. Review module code and its dependency supply chain. The route contract, SDK version check and CSP do not make hostile modules safe to install.

## OTLP and audit

Public OTLP is opaque forwarding with request/response limits, concurrency/rate bounds, cancellation and no redirect following. It strips caller credentials and adds a proxy marker. Preserve that marker's treatment in the Collector so public telemetry cannot impersonate trusted application audit.

Application audit stores record bodies, attributes and resources. Redact sensitive fields before they enter the trusted audit pipeline. Control audit is narrower and stores allowlisted operation metadata.

## Telemetry isolation

`service.namespace` and other resource attributes are untrusted labels, not authorization scopes. This installation model does not turn a single-tenant backend into a multi-tenant one. Separate deployments or enforce isolation at authenticated ingestion/storage boundaries when required.

Backend query credentials, retention, TLS and tenant routing belong to deployment configuration. They are not UI settings. See [OTLP limits](../reference/otlp.md) and [observability access](../reference/observability-api.md).
