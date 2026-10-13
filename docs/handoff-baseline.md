# Engineering baseline for UI handoff

## Ownership and shared contracts

The UI agent owns visual design and product workflow composition in
`web/apps/embedding`, component styles/stories and corresponding UI tests.
Working notes for the UI are in [console UI](ui-handoff.md).
The backend lane owns `internal/`, `pkg/backplane/`, migrations and Go conformance.

Preserve these shared contracts during UI work. A needed change should identify
the affected screen, API method and consumer first; update source contracts,
generated clients, documentation and consumer checks together.

| Boundary | Baseline | Source of truth |
| --- | --- | --- |
| Console API | `backplane.console.v1`, 72 methods, protobuf 64-bit values as bigint | `backplanepb/console/v1/*.proto`, `web/packages/api/src/gen`, [API reference](api-reference.md) |
| Sessions | Operator-token exchange, HttpOnly cookie, relative login/session/logout endpoints | [client contract](api-client.md), `internal/console/http.go` |
| Transport | One host-owned ws-proto connection, cancellation, distinct snapshot/cursor semantics, no mutation replay | `web/packages/client`, `web/packages/react` |
| Module SDK | Major 0, relative routes/nav, host providers, catalog-validated hash/SDK, service-owned delivery | `web/packages/plugin-sdk`, `web/packages/plugin-build`, [frontend contracts](frontend-platform.md) |
| Routes | Platform `/services/:service/...`; module `/s/:service/*`; Services/Explore/Audit before module branches | [console structure](console-review.md) |
| Packages | Twelve `@gopherex/backplane-*` workspaces at 0.1.0; GitHub Packages target; pinned Yarn lockfile | `web/packages/*/package.json`, `web/yarn.lock` |
| Forms | schemapb 1.7.0 validation/defaults/masking; preserve large integer strings | [schema forms](schema-forms.md) |
| Audit | Database mutations/audit in one transaction; external command intent/results; cursor expiry explicit | [audit contract](audit-api.md) |
| Telemetry | Deployment-owned OTel; ObsService reads; capability-driven languages; optional declarations | [ObsService](obs-api.md), [admission](telemetry-admission.md) |

Coordinate edits to `Makefile`, compose, protobufs/generated clients, package
manifests/lockfile and plugin build/runtime contracts. Presentational APIs may
evolve with their consumers; breaking public runtime contracts needs an explicit
compatibility decision. Visual changes must not fork the transport or SDK.

## Checkpoint scope

The baseline includes accumulated audit/observability/backend integration,
frontend packages, author template, live examples, technical acceptance fixtures,
draft shell/login, documentation and the console development proxy. It is a
development starting point, not a production release or visual acceptance.
CI, production packaging/publication and application modules remain deferred.

The repository's main branch is named `master` and contains this foundation.
Create UI worktrees from `master` rather than the older `d3bb94a` base.
This is a development baseline, not a tagged release.

Review covers transaction/audit wiring, session errors, telemetry boundaries,
generated contracts, package/runtime ownership, acceptance tests and accidental
artifacts/credentials in candidate source files. It does not certify every
product workflow or load scenario.

## Validation status

- Go race suite passed using existing test cache. Environment-gated live
  conformance was not rerun as part of that command.
- Go/proto lint passed with zero issues.
- Full frontend package/application builds and TypeScript passed.
- 49 unit cases passed, including dev-proxy endpoint selection, origin checks,
  prefix handling and cookie preservation.
- Vite dependency optimization passed after isolating Grafana's private router
  versions from the host's shared router in the optimizer pipeline.
- Generation completed for Go, TypeScript, hello, store queries and the
  72-method API reference; all 66 captured generated/contract files matched their
  pre-generation content (no drift).
- `make web-check` passed: builds, TypeScript, 49 unit cases, seven browser
  cases, all 72 Storybook cases and the isolated Go console/browser integration.
  The Go integration uses its own server, operator token and catalog fixture;
  it cannot silently fall back to a running development installation.
- Live proxy/HMR acceptance passed in `web/tests/console-dev.mjs`: cookie login,
  module assets/RPC, deep links, CSS/React updates with the same document,
  preserved module draft and platform socket, foreign-origin rejection, logout.
  `ConsoleRuntime` keeps catalog subscriptions outside shell Fast Refresh so
  editing `Console.tsx` does not briefly unmount module pages.
- All twelve packed packages passed installation/build checks in external
  consumers, including standalone dev/build and embedded browser acceptance.
- The live API workflow passed through Vite after a fresh dependency
  optimization: service relay, configuration apply/restore, binding/hook,
  audit and stored logs/metrics. Lazy editor/chart dependencies are prebundled
  so their first navigation does not reload the page.
- `make test-dev` also passed through Vite: login rejection/outage recovery,
  the complete live API workflow, module navigation, sidebar resizing/pinning,
  both themes, mobile/keyboard behavior and accessibility checks.

## Reproduce verification

At repository root, `rtk proxy make dev` prepares the installation. From `web/`
in another terminal, start `rtk proxy yarn dev:console`. Then from `web/`:

```sh
rtk proxy yarn test:console-dev
rtk proxy env BACKPLANE_DEV_URL=http://127.0.0.1:5173 node tests/login-browser.mjs
rtk proxy env BACKPLANE_DEV_URL=http://127.0.0.1:5173 node tests/console-browser.mjs
rtk proxy env BACKPLANE_DEV_URL=http://127.0.0.1:5173 BACKPLANE_DEV_TARGET=http://127.0.0.1:8081 node tests/dev-browser.mjs
rtk proxy yarn test:packed
```

At repository root, run `rtk proxy make web-check` for Storybook scenarios
and real Go console/browser integration. Do not run host conformance
sharing example service identities alongside the same dev installation.

The HMR check verifies real cookie login, module assets/RPC, deep links,
CSS/React hot updates without draft/session loss, foreign-origin rejection and
logout. It temporarily changes two shell files and restores them; run it without
concurrent editing. Cleanup refuses to overwrite concurrent changes.
