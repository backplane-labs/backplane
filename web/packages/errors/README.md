# @backplane-labs/backplane-errors

Browser exception capture for any web frontend reporting to a Backplane
installation: each captured exception is one OpenTelemetry log record carrying
the exception, the application's registered state and a bounded history
(breadcrumbs). Records go to Backplane's public OTLP ingest; the console's
Errors section reads them back with their trace and related logs. Importing
the package installs no listeners and sends nothing.

```ts
import { createOtlpClient } from '@backplane-labs/backplane-errors/otlp';

const errors = createOtlpClient({
  // Backplane's public OTLP/HTTP ingest (the console's /telemetry route).
  url: 'https://example.com/backplane/telemetry/v1/logs',
  // Optional ingest key (BACKPLANE_OTLP_KEYS). A key shipped to browsers is
  // public: it filters stray traffic, it does not authenticate users.
  headers: { authorization: 'Bearer <ingest key>' },
  resource: { 'service.name': 'shop-web', 'deployment.environment.name': 'production' },
});

errors.registerState('cart', { read: () => cart.summary() });
errors.addBreadcrumb('checkout.submit', { items: 3 });
try { await pay(); } catch (error) { errors.captureException(error, { handled: true }); }
```

- `.` — `createClient` over a LoggerProvider you own.
- `/otlp` — `createOtlpClient`, owning a provider and the OTLP/HTTP protobuf
  exporter, with an optional IndexedDB outbox that survives reloads and
  offline periods. It runs in a page or in a dedicated or shared worker.
- `/browser` — opt-in fetch, XHR and navigation breadcrumbs and a React error
  handler (no React dependency).
- `/protocol` — the wire types (`DebugEnvelopeV1`, `DebugSnapshotV1`).

Policies: `sanitize` (and `redactKeys`), `filter`, `rateLimit`, bounded
exception causes. `client.snapshot()` returns local state and history for an
application's own bug reports without sending anything. `client.stats()` and
`client.outbox.stats()` expose delivery diagnostics without user data.

Errors are stored by the platform, grouped by fingerprint and kept for its
configured retention (90 days by default).
See `docs/errors.md` in the Backplane repository for the wire format, the
ingest and what the console shows.
