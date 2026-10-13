# Backplane frontend

The workspace provides thirteen `@gopherex/backplane-*` packages: `api`, `client`, `errors`,
`react`, `theme`, `ui`, `schema-forms`, `editors`, `charts`, `observability-ui`,
`platform-ui`, `plugin-sdk` and `plugin-build`. It includes a Storybook catalog,
a standalone author template and an embedded host acceptance fixture. Product
console screens use the same packages and shared light/dark tokens.

The [package contract](../docs/frontend-platform.md) and
[component matrix](../docs/ui-components.md) describe the public scope.

Requirements: Node >=22.12 and Yarn 1.22.22. Gopherex dependencies are downloaded
from GitHub Packages; configure a GitHub token with package read access in your
user package-manager configuration. Do not commit credentials. Our packages are
configured to publish only to `https://npm.pkg.github.com`.

From `web/`:

```sh
yarn install --frozen-lockfile
yarn dev                 # component compatibility fixture
yarn dev:module          # standalone hello UI with explicit local fixtures
```

Both commands build the local workspace packages first. Neither starts or
requires backplane, Consul, PostgreSQL, NATS or Temporal. The template's local
fixtures are in `templates/module/src/fixtures.ts`; authors replace them with
their own backend setup. The platform never silently substitutes mocks when a
real connection fails.

```sh
yarn build               # packages, compatibility fixture, standalone and remote
yarn typecheck
yarn test:unit
yarn playwright install chromium  # once per machine/CI image
yarn test:browser
yarn storybook            # component stories at localhost:6006
yarn build:storybook
yarn test:storybook       # built stories, both themes and keyboard/a11y checks
yarn test:packed          # fresh external consumers of all 13 package tarballs
```

`make configure` from the repository root also installs locked frontend tools.
`make gen` generates Go and TypeScript APIs from the same protobuf sources.
The [method reference](../docs/api-reference.md) and [transport contract](../docs/api-client.md) cover all 85 RPCs.
The source output includes imported platform/schemapb messages and lossless
protobuf int64/uint64 handling. `web/scripts/api-index.mjs` exports every console
service client without colliding generated `CallOptions` declarations.

## Runtime example

```ts
import { create } from '@bufbuild/protobuf';
import { BackplaneClient } from '@gopherex/backplane-client';
import { CatalogServiceClient, ListServicesRequestSchema } from '@gopherex/backplane-api';

const runtime = new BackplaneClient({ baseURL: `${location.origin}/backplane/` });
await runtime.start(); // checks the existing HttpOnly cookie
// When anonymous, the login form calls await runtime.login(operatorToken).
const catalog = runtime.client(CatalogServiceClient);
try {
  const result = await catalog.listServices(create(ListServicesRequestSchema));
  console.log(result.services);
} catch (error) {
  // Present an error/connection state; do not replay mutations automatically.
}
// The owning host calls runtime.dispose() when it is destroyed.
```

React consumers wrap their content in `BackplaneProvider client={runtime}` and
call `useClient(CatalogServiceClient)` and `useConnection()`. Only the host owns
the client's lifetime. `useSnapshotWatch` restarts full-snapshot streams after
connection replacement, aborts on unmount and hides data from a replaced
session/provider. It is not an event/cursor replay helper.

## Technical integration findings

- The theme bridge resolves the same palette to CSS variables and Grafana JS
  colors. Grafana's theme factory requires an even base font size; the shared
  base is 14px. It is an upstream `@internal` export, isolated and pinned.
- Grafana's published package does not include its expected application icon
  directory. `grafanaAssets()` supplies the icons used by supported controls
  from Lucide, resolves URLs relative to the emitted assets, and installs the
  path before rendering. The compatibility tests reject HTML masquerading as SVG.
  This is a bounded adapter inventory, not support for arbitrary Grafana icons.
- Grafana still carries legacy router dependencies. The build preset preserves
  their installed private versions before MF rewrites bare package imports.
  Platform and module routing uses one shared React Router 7 context; Grafana
  navigation components are excluded from our public kit. Further Grafana
  adaptations must be checked individually.
- Embedded builds make context-bearing dependencies host-only shared singletons.
  `sdk_major` is 0 for the current 0.1.x package line. The produced remote
  passes a browser host integration test under `/backplane/` with the real Go
  console CSP. `loadPlugin` from `plugin-sdk/host` checks catalog identity,
  same-origin paths and SDK metadata before loading remote code. The fixture
  uses local API data. `TestBrowserConsole` runs the same host against the real
  Go console, with an in-memory registry/session store, to verify cookie auth,
  ws-proto catalog reads, the protected bundle proxy and 503 session recovery.
- The compatibility page currently includes its heavy controls eagerly. It is
  a technical fixture, not the console's loading/performance architecture.

## Live development installation

From the repository root, `make dev` builds and starts the stack, backplane,
hello and formatter with a seeded binding and event rule. Open
`http://127.0.0.1:8081/backplane/` and use `DEV_ADMIN_TOKEN` (the documented local
default is `dev-admin-token-change-me`). `Development acceptance` opens the live
kit composition fixture. See [development instructions](../docs/development.md).
`make test-dev` exercises service relay, configuration, hook execution, audit and
stored telemetry in the browser. This installation is not required by authors.

`yarn test:packed` writes 13 tarballs and independent consumers in a temporary
directory, rejects workspace symlinks, typechecks/builds the author template,
then runs standalone dev/build and embedding browser acceptance. It does not publish.
The standalone template also includes a ready static-server Dockerfile.

The [release workflow](../docs/releases.md) publishes all thirteen packages to
GitHub Packages on a coordinated version tag and attaches their tarballs to
GitHub Releases. A manual release run rehearses builds without publication.
