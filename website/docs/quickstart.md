# Run a complete installation

This walkthrough starts Backplane, hello and formatter with containerized infrastructure. It demonstrates live configuration, module pages and telemetry.

## Prerequisites

- Docker with Compose and permission to use the Docker daemon.
- Go 1.27, Node 24 and Yarn 4 (`corepack enable`).
- Git and GitHub Packages read credentials for the `@backplane-labs` and `@gopherex` packages.

Configure the registry in your user package-manager configuration. Keep the token in the environment or your credential manager; never commit it:

```ini
@backplane-labs:registry=https://npm.pkg.github.com
@gopherex:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}
```

The token needs package read access. GitHub Packages requires authentication even for public npm packages. The published container is public and does not require these credentials at runtime.

```bash
git clone https://github.com/backplane-labs/backplane.git
cd backplane
git checkout v0.1.1
make configure
make dev
```

`make dev` builds the console, module bundles and Go binaries, starts the Compose installation, waits for registration and health, then sends a greeting. Temporal runs inside Compose. No host Temporal process is needed.

## Open the console

Open **http://127.0.0.1:8081/backplane/**. The local development operator credential is `dev-admin-token-change-me`; set `DEV_ADMIN_TOKEN` before starting the stack to change it. This is a development default, not a production credential.

Login creates an HttpOnly cookie. The frontend does not store the operator token. Services is the landing page; the sidebar also exposes platform tools and module-owned pages.

## Exercise the services

```sh
curl 'http://127.0.0.1:8080/hello/?name=ann'
curl 'http://127.0.0.1:8082/formatter/?name=ann'
```

Hello returns `Hello, ann!`; formatter returns the JSON string `"Welcome, ann!"`. Each handles its own request. In the Services page open Hello's module and refresh its statistics. The module calls Hello's internal API through the console relay, not its public HTTP handler. Use Configuration to change `greeter.suffix`, then repeat the first request.

## Logs, shutdown and restart

```bash
make dev-logs
make dev-down
```

Shutdown retains volumes. Running `make dev` again rebuilds services; it does not reset the database. For the smallest console installation use `make dev-minimal` after stopping the full stack; see [dependency modes](concepts/dependencies.md).

For frontend-only iteration against the running backend:

```bash
cd web
yarn dev:console
```

Open **http://127.0.0.1:5173/backplane/**. For a module without any platform, run `yarn dev:module` instead. See [development details](reference/development.md) for targets, ports and acceptance commands.
