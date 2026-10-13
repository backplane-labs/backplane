# Local development

## Module author

From `web`, run `yarn dev:module`. The template owns its router and supplies an
explicit fixture client. It starts without any platform services. The same
routes/components build into a remote with `yarn workspace @backplane/module-template build`.
See [template instructions](../web/templates/module/README.md).

## Platform installation

For UI-only iteration against a running installation, use
`rtk proxy yarn dev:console` from `web/`. It builds shared packages once and
starts Vite at `http://127.0.0.1:5173/backplane/`. Shell TSX/CSS changes update
without rebuilding Go binaries or restarting containers. Shared package changes
still require their package build and a browser refresh.

Vite proxies only `auth/*`, `ws` and `plugins/*` to the existing installation at
`http://127.0.0.1:8081`; application routes/assets and HMR stay on Vite. Cookies
remain HttpOnly; module clients use the host backend socket. HMR has its own
socket, not a second RPC transport. The proxy verifies the browser origin before
rewriting it for backend origin checks. It binds to loopback and does not change
backend authentication or production CSP.

Set `BACKPLANE_DEV_TARGET` to another installation origin if needed, without
credentials, a path or query. `BACKPLANE_CONSOLE_BASE` defaults to `/backplane/`
and must match the backend prefix. After the initial package build,
`rtk proxy yarn workspace @backplane/console dev` starts Vite directly.
This command requires a running backend; it does not start Docker or seed data.

Run `make dev` at the repository root. Requirements: Docker Compose, Go 1.26,
Node >=22.12, Yarn 1.22.22 and GitHub Packages read credentials in the user's
package-manager configuration. `make configure` installs pinned generators.

The command builds packages, the standalone/remote module and the live platform
console; builds static Go binaries; starts the base stack plus independent OTel
Collector/Victoria stores and backplane/hello/formatter containers; waits for
operator endpoint and all three Consul health checks; installs `hello.Greet -> formatter.Format` and the
`hello.Greeted -> formatter.Record` rule; and sends a sample greeting.

Open `http://127.0.0.1:8081/backplane/`. The local default operator token is
`dev-admin-token-change-me`; override it with `DEV_ADMIN_TOKEN`. The development
console uses an operator token to create a cookie session. Its centered login
screen explains the credential, supports dark/light themes and token visibility,
and distinguishes rejected credentials from rate limits or connection failures.
It prevents duplicate submissions and keeps the requested deep link after login.
The token is not prefilled or saved in browser storage.
The development
host loads the hello plugin from hello's platform port through backplane's
protected, hashed bundle proxy. Its internal `GetStats` client and the platform
clients share one WebSocket. The landing page is Services; Wiring, Explore, Errors, Audit and Infrastructure
are the remaining platform entries in the left navigation. Service groups contain pages
declared by their modules. Service administration is under `/services/:service`.
The technical kit acceptance fixture remains directly accessible at
`/backplane/dev`; it is not part of the product navigation.

The sidebar collapses to an icon rail, expands on hover/keyboard focus and can
be pinned. Drag its edge or use Left/Right/Home/End on the focused separator to
resize; double-click restores the default. Width, pinning and theme are local
preferences. On narrow screens, the header button opens the navigation drawer.
The console asset prefix is configured at build time with
`BACKPLANE_CONSOLE_BASE` (default `/backplane/`), matching the backend prefix.

`make dev-logs` follows service logs. `make test-dev` runs live API/component and
console navigation acceptance, including both themes, keyboard/mobile behavior
and axe accessibility checks on the shell.
To run it through Vite, use
`rtk proxy env BACKPLANE_DEV_URL=http://127.0.0.1:5173 BACKPLANE_DEV_TARGET=http://127.0.0.1:8081 make test-dev`.
`BACKPLANE_DEV_TARGET` selects the console upstream. Direct service requests such as `/hello/` use `BACKPLANE_HELLO_URL` (default `http://127.0.0.1:8080`). From `web/`, `yarn test:console-dev` checks HMR;
it temporarily edits and restores shell TSX/CSS, so do not edit those files
concurrently.
`make dev-down` stops the installation and keeps database/telemetry volumes.
Running `make dev` again rebuilds the services and reapplies the named example
binding/rule; it does not delete stored data. Configuration edits made in the
fixture persist. Server validation checks the schema; service-specific live
validators may still reject a revision. Check per-instance applied/rejected
status before treating it as deployed. The fixture's settings page explicitly saves only local state.

The compose overlays use the same base project as `make up`. Do not run
conformance processes that register the same services alongside the
live installation. After `make dev-down`, `make up` restores the host-process
conformance topology. The development overlay exposes direct console and service listeners; host-process tests allocate their own ports.

The container runtime uses built binaries and read-only asset mounts. It does
not install frontend dependencies or receive package-registry credentials.
Services advertise their resolved container IPs. The OTel exporters send to the
independent Collector; backplane reads Victoria through ObsService and offers
opaque OTLP admission at `/backplane/otlp/v1/{logs,metrics,traces}`. No telemetry
backend deployment controls appear in the UI. Trace search languages depend on
the backend capabilities; TraceQL is not advertised unless enabled by deployment.

## Acceptance commands

- `make web-check`: packages/fixtures, types, unit cases, both browser suites and
  a real Go-console cookie/relay/bundle test.
- `cd web && yarn test:packed`: all public packages installed as tarballs into
  fresh external consumers; standalone and embedded browser checks.
- `make test-dev`: real running installation; does not start it implicitly.
- `make lint` and `GOWORK=off go test -race ./...`: backend checks.

See the [frontend contract](frontend-contract.md), [API reference](api-reference.md)
and [English handbook](../website/docs/intro.md) for current integration boundaries.

For the console without optional infrastructure, use `make dev-minimal`.
PostgreSQL, Consul and Valkey remain required. The **Infrastructure** page shows
configured dependencies as OK/NO/Stale and separately lists deployment capabilities.
See [deployment modes](../deployments/README.md#server-capabilities-and-infrastructure-health) for
Compose profiles and endpoints. Service **Workflows** includes administrative
schedule creation, editing and deletion; module SDKs never reconcile schedules.

## CI and release verification

Pushes to master and pull requests select checks from the changed paths:

- Go changes run Go lint, vet and race tests without starting infrastructure.
  The Go build/test cache is restored and saved per revision; unchanged test
  results are reused, including automatic invalidation when dependencies change.
- Web changes build workspace packages, typecheck and run the web unit suite.
  This checks shared package consumers together. Browser, Storybook and packed
  consumer suites are reserved for full verification.
- Proto, SQL and generated API changes also check generation and proto compatibility,
  and run both Go and web checks.
- Compose/deployment changes validate Compose configurations without starting containers.
- Markdown-only changes skip application checks. CI/build configuration changes and
  unknown paths conservatively select all quick checks.

The small path-selector regression suite always runs. A pull request compares
against the merge base; a push checks the entire pushed commit range, including
deletions and both sides of renames. There is no full stack startup on a normal push.

After `make configure` and `cd web && yarn playwright install --with-deps chromium`,
run `make verify` from a clean committed checkout. The CI workflow's manual
**Run workflow** action runs the same full verification, as does `make check`.
Full verification runs only when explicitly requested. It checks generated files, lint,
Go race tests with the full containerized infrastructure, browser schedule CRUD
and timer execution, frontend suites, packed consumers, and the minimal Compose
installation in both themes. The runner owns only the `backplane-verify` Compose
project and stops it on exit; infrastructure ports used by the development stack must be free. The minimal
console check uses port 18081, leaving a separately running console untouched.
Results are retained under `bin/verification`.

`make release` checks that the working tree is clean and the branch is synchronized
with its upstream, then creates and pushes the selected version tag. It does not
build, install dependencies, or run tests. The Go module keeps `vX.Y.Z` tags.

Tags trigger [release packaging](releases.md): Linux archives with the console,
multi-platform GHCR images, GitHub Packages and a GitHub Release with checksums.
The release requires successful push CI for the tagged commit. Its manual run
rehearses the builds without publication.

## Frontend source map

| Path | Responsibility |
| --- | --- |
| `web/apps/console/src/main.tsx` | Production session, theme and i18n bootstrap; always uses the real client |
| `web/apps/console/src/console/` | Shell, navigation, product routes and service administration |
| `web/apps/console/src/console/registry.ts`, `ConsoleRuntime.tsx` | Catalog and module ownership outside shell Fast Refresh |
| `web/apps/console/src/dev.tsx` | Explicit live composition acceptance page at `/backplane/dev` |
| `web/apps/catalog/` | Storybook and visual compatibility fixture |
| `web/tests/fixtures/embedding/` | Isolated module host used by browser and external-package acceptance |
| `web/templates/module/` | Standalone author template and the hello remote used by the dev installation |
| `web/build/` | Shared internal library build presets |

The console always builds to `web/apps/console/dist`. There is no fixture mode
or HTML switch in the product bootstrap. The embedding fixture uses its own
explicit fake client and is never part of the release console.

After changing a package, rebuild it with `yarn workspace <package> build`.
Adding or renaming an export requires restarting Vite: federation fixes the
shared export list at startup. Catalog ownership stays outside the shell so
TSX/CSS hot updates preserve the module draft, session and platform socket.

`yarn test:console-dev` checks those guarantees by temporarily editing two shell
files and restoring them; run it without concurrent editing. Cleanup refuses to
overwrite concurrent changes. Packed-consumer verification needs roughly 1.3 GB
and 70,000 inodes in `TMPDIR`; use disk-backed temporary storage if necessary.
