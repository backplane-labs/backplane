# @backplane-labs/backplane-errors

Exception capture for any JavaScript frontend reporting to a Backplane
installation: each captured exception is one OpenTelemetry log record carrying
the exception, the application's registered state and a bounded history
(breadcrumbs). Records go to Backplane's public OTLP ingest; the console's
Errors section reads them back with their trace and related logs. Importing
the package installs no listeners and sends nothing.

In a browser (a page or a dedicated/shared worker):

```ts
import { createBrowserClient } from '@backplane-labs/backplane-errors/browser';

const errors = createBrowserClient({
  // Backplane's public OTLP/HTTP ingest (the console's /telemetry route).
  url: 'https://example.com/backplane/telemetry/v1/logs',
  // Optional ingest key (BACKPLANE_OTLP_KEYS). A key shipped to browsers is
  // public: it filters stray traffic, it does not authenticate users.
  headers: { authorization: 'Bearer <ingest key>' },
  resource: { 'service.name': 'shop-web', 'deployment.environment.name': 'production' },
  captureUnhandled: true,
});

errors.registerState('cart', { read: () => cart.summary() });
errors.addBreadcrumb('checkout.submit', { items: 3 });
try { await pay(); } catch (error) { errors.captureException(error, { handled: true }); }
```

The core touches no browser API: it reaches its host only through ports the
caller passes in. On another host (React Native, Node) supply them yourself:

```ts
import { createOtlpClient } from '@backplane-labs/backplane-errors/otlp';

const errors = createOtlpClient({
  url, resource,
  runtime: { now: () => Date.now(), monotonic: () => performance.now(), randomId: uuidV4 },
  uncaught: { subscribe: (report) => installGlobalHandler(report) }, // returns its removal
  // outbox: { storage: myOutboxStorage, triggers: myTriggers }, // optional; otherwise batch in memory
});
```

- `.` — `createClient` over a LoggerProvider you own, `createReactErrorHandler`
  (React's `onCaughtError` / `componentDidCatch`, no React dependency), and
  the ports: `Runtime`, `UncaughtSource`, `DeliveryTriggers`, `OutboxStorage`
  (its contract is documented on the interface), `outboxLimits`, `retryDelay`.
- `/otlp` — `createOtlpClient`, owning a provider and the OTLP/HTTP protobuf
  exporter, with an optional persistent outbox on the given storage.
- `/browser` — the browser's ports (`browserRuntime`, `globalUncaught`,
  `browserTriggers`, `indexedDbOutbox`), `createBrowserClient` built on them,
  and opt-in fetch, XHR and navigation breadcrumbs (`instrumentBrowser`).
- `/protocol` — the wire types (`DebugEnvelopeV1`, `DebugSnapshotV1`).

Policies: `sanitize` (and `redactKeys`), `filter`, `rateLimit`, bounded
exception causes. `client.snapshot()` returns local state and history for an
application's own bug reports without sending anything. `client.stats()` and
`client.outbox.stats()` expose delivery diagnostics without user data.

Errors are stored by the platform, grouped by fingerprint and kept for its
configured retention (90 days by default).
See `docs/errors.md` in the Backplane repository for the wire format, the
ports, the ingest and what the console shows.
