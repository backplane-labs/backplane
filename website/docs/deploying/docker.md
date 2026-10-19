# Containers and binary archives

The release image includes the Linux server and production console. It runs as a non-root user and needs no Node runtime or package registry credentials.

```bash
docker pull ghcr.io/backplane-labs/backplane:0.1.1
```

Both Linux amd64 and arm64 are published. For reproducible deployments, use the immutable digest from the release's `image-digest.txt` instead of the moving `latest` tag.

## Connect existing infrastructure

Create a deployment-owned environment file, with restricted permissions, replacing the example hosts and secret placeholders:

```ini
BACKPLANE_PG_DSN=postgres://backplane:REPLACE@postgres:5432/backplane?sslmode=require
BACKPLANE_CONSUL_ADDR=consul:8500
BACKPLANE_ADMIN_TOKEN=REPLACE_WITH_OPERATOR_SECRET
BACKPLANE_INTERNAL_SECRET=REPLACE_WITH_INSTALLATION_SECRET
```

Run on a network that resolves those hosts:

```bash
docker run --name backplane --restart unless-stopped \
  --network platform --env-file ./backplane.env \
  -p 127.0.0.1:8081:8081 \
  ghcr.io/backplane-labs/backplane:0.1.1
```

`platform` must be your existing Docker network. Terminate HTTPS in front of the loopback-published console. Secure cookies are the default. Enable `BACKPLANE_CONSOLE_INSECURE_COOKIE=true` only for deliberate plain-HTTP local development.

The image defaults to `/app/console` and `/backplane`. Platform port 9400 should only be reachable by appropriate internal peers. Add NATS/Temporal/observability variables only for enabled capabilities.

## Healthchecks

The images are distroless: no shell, curl or wget. Every binary built on
the SDK — the backplane server and every service — probes itself instead:

```sh
/app/bin/<binary> probe [readiness|liveness|startup]   # readiness by default
```

It asks its own platform port (`BACKPLANE_INTERNAL_PORT`, from the same
file and environment the service reads) for `/healthz/<kind>` on
127.0.0.1 and exits 0 when the probe passes, 1 when it does not.

```yaml
services:
  backplane-kratos:
    image: ghcr.io/backplane-labs/backplane-kratos:<version>
    healthcheck:
      test: ["CMD", "/app/bin/kratos", "probe"]
      interval: 5s
      timeout: 6s
      retries: 30
  kratos:
    depends_on:
      backplane-kratos: {condition: service_healthy}   # the overlay is written
```

In Kubernetes use the HTTP probes on the platform port directly.

## Binary archive

Download the architecture-specific archive and `SHA256SUMS` from the same release:

```bash
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf backplane_0.1.1_linux_amd64.tar.gz
```

From the extracted directory set `BACKPLANE_CONSOLE_ASSETS_DIR` to the absolute `console` path, configure the same infrastructure variables and run `./bin/backplane`. The archive includes launch instructions. Keep binary and console from the same version.

## Build from source

```bash
docker build --secret id=github_token,env=NODE_AUTH_TOKEN \
  -f deployments/Dockerfile -t backplane:local .
```

The BuildKit secret is used for GitHub Packages reads during installation. It is not a build argument and is not included in the runtime image. The release workflow instead assembles the image from its already built archive trees.

For a complete local fixture, use [quickstart](../quickstart.md); it provisions the development infrastructure and examples rather than expecting your external endpoints.
