# Console UI: working notes

How the console UI is built, run and verified. The product layout is described
in [console layout](console-review.md); the building blocks in
[UI kit](ui-kit.md) and [platform compositions](platform-ui.md).

## Direction

The console is a desktop operator workspace in the family of Grafana, HyperDX,
SigNoz and GitLab, and of the Stroppy Cloud and Komeet admin panels: dense
rows, sharp corners, panels with their own scroll, color only for status. The
kit stays shadcn/Radix/Tailwind (`@gopherex/backplane-ui`) with Grafana used
where it is strongest (plots, time controls). Every surface — shell, platform
pages and module pages — is composed from the same kit, so modules look native
without module-owned styles. English only; dark and light themes.

## Code map

| Path | Responsibility |
| --- | --- |
| `web/packages/theme/src/tokens.ts` | Palette and semantic tokens for both themes; Grafana theme adapter |
| `web/packages/ui/src/theme.css`, `style.css` | Tailwind theme preset; the kit family stylesheet |
| `web/packages/ui/src/components/layout.tsx`, `status.tsx`, `time.tsx`, `entity-card.tsx`, `filter.tsx` | Page compositions |
| `web/packages/platform-ui/src/` | API-aware screens: services, config, automation, runs, events, operations, explore (`explore*.ts[x]`), audit, map |
| `web/packages/platform-ui/src/wiring/` | Wiring: YAML model and positions (`document.ts`), completion, catalog and shapes, draft and analysis, graph, inspector, history, workspace |
| `web/apps/embedding/src/main.tsx` | Runtime, session, theme and i18n bootstrap; live console vs SDK compatibility fixture |
| `web/apps/embedding/src/console/Console.tsx` | Routes and page frames |
| `web/apps/embedding/src/console/shell/` | Header, breadcrumbs, command palette, session menu |
| `web/apps/embedding/src/console/Sidebar.tsx` | Navigation tree, rail, pinning, resizing |
| `web/apps/embedding/src/console/services/`, `service/` | Services landing (cards, table, map) and service page with tabs |
| `web/apps/embedding/src/console/registry.ts`, `ConsoleRuntime.tsx` | Catalog watch, module loading and isolation, kept outside Fast Refresh |
| `web/apps/embedding/src/console/tailwind.css`, `console.css` | Shell utilities (over the kit preset) and chrome layout |
| `web/templates/module/` | Module template (hello): standalone shell and embedded pages built from kit compositions |
| `web/apps/catalog/` | Storybook with every composition against `PlatformFixture` |

The `embedding` app's `live` build is the product console; its default build is
the SDK compatibility fixture, which alone imports the template's
`standalone.css`.

## Run and inspect

At the repository root: `make dev`, then open
`http://127.0.0.1:8081/backplane/` and log in with the operator token
(`dev-admin-token-change-me` locally, `DEV_ADMIN_TOKEN` overrides it). The token
is never baked in, prefilled or stored.

For hot iteration run `yarn dev:console` in `web/` and open
`http://127.0.0.1:5173/backplane/`; it proxies to the running installation and
keeps drafts, the cookie session and the socket across HMR. After changing a
package, rebuild it (`yarn workspace <package> build`, or
`yarn build:packages`). Adding or renaming a package export requires restarting
the dev server: module federation fixes the shared export list at start.

The dev container serves `web/apps/embedding/dist-live`
(`yarn workspace @backplane/embedding build --mode live --outDir dist-live`,
then reload). The hello module bundle comes from
`web/templates/module/dist/plugin`; a manifest is immutable per service
version, so a changed bundle needs a hello binary built from a new version.

## Runtime boundaries

- `/services/:service/...` is platform administration; `/s/:service/*` belongs
  to the module. The shell never hardcodes a module.
- The host owns router, theme, i18n and the one transport; modules use shared
  providers. Descriptor, hash, SDK and CSP checks stay in place.
- Session data never survives logout or replacement. Transient network, 429
  and 5xx failures do not mean revoked credentials. Drafts survive a reconnect;
  uncertain mutations are never retried automatically.
- Exact integers stay `bigint`; nanosecond timestamps are never rounded.
- A saved configuration revision is not necessarily applied: the rollout shows
  applied/rejected per instance.
- Telemetry storage is deployment-owned; the UI queries it through ObsService in
  the languages the deployment advertises.

## Verification

- `make web-check`: package and fixture builds, typecheck, unit tests, browser
  specs (catalog, template, embedding), Storybook build and tests in both themes
  with axe, and the Go console integration test.
- `make test-dev` against the running installation: login, the live workflow
  (relay, configuration, binding, operation, audit, logs and metrics) and the
  console acceptance (service views and map, palette, tabs, module pages,
  navigation behavior, themes, axe).
- `yarn test:packed` after changing package exports or dependencies. It needs
  about 1.3 GB and ~70k inodes in `TMPDIR`; point `TMPDIR` at a disk-backed
  directory when `/tmp` is a small tmpfs.
- Inspect screenshots in both themes; automated checks are not visual approval.
