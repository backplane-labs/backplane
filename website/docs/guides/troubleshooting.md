# Troubleshooting

Start with the failing boundary and the exact error. Infrastructure status helps locate an outage, but a green endpoint probe does not validate routing, schema compatibility or data delivery.

| Symptom | Check |
| --- | --- |
| `ERR_CONNECTION_REFUSED` | Container state, published port and the console listener on 8081 or the service's own API port |
| Docker permission denied | Access to the active Docker socket/context; this precedes application startup |
| Login returns 503 | PostgreSQL availability and server logs; do not rotate the token as a first response |
| Login rejected or rate-limited | Correct operator credential, clock/session settings and retry interval |
| Login works but WebSocket fails | Same-origin proxy, upgrade headers, configured prefix and origin policy |
| Assets return HTML or 404 | Console build base matches backend prefix; proxy preserves assets and plugin paths |
| Service absent | Consul endpoint, advertised address, registration/health and version identity |
| Saved configuration not active | Per-instance applied/rejected status and custom validator reason |
| Hook unavailable | Temporal capability, binding existence/validity, worker health and task queue |
| Event not processed | NATS capability, durable consumer, rule pause and DLQ; rules also require Temporal |
| No telemetry rows | Range, source labels, Collector export, backend retention and query expression |
| Module standalone works, embedded fails | Shared runtime versions, SDK major, relative lazy assets and production CSP |
| API type/reference missing | Route schema, transitive descriptor imports or bundled relative `$ref` path |

## Inspect the development stack

```bash
docker compose --profile full -f docker-compose.yaml \
  -f docker-compose.observability.yaml -f docker-compose.dev.yaml ps
make dev-logs
```

Use the same Compose files that started the installation. Stop `make dev` before running conformance processes that own the same ports/service names.

## Authentication versus availability

An expired/revoked cookie requires login. A backend outage requires recovery. Preserve drafts and distinguish these states. Never put the operator token in a URL, browser local storage or a screenshot shared for debugging.

## Unknown mutation outcome

Record the operation ID and inspect Audit plus the target runtime. A timeout can occur after the backend accepted the action. Do not use repeated clicks as a recovery mechanism.

## Telemetry and audit

Check the Collector's receiver, processing and exporter paths separately. Browser proxy traffic cannot enter trusted application audit. Audit ingest can partially reject invalid/oversized records while committing valid ones; inspect rejection metrics rather than retrying malformed records forever.

## Before reporting a defect

Include release/commit, deployment mode, affected service/instance, reproduction steps, error code and whether the failure is deterministic. Redact credentials and business payloads. State which checks actually ran; skipped integration tests do not establish live behavior.
