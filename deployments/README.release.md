# Backplane release archive

This archive contains the Linux server (`bin/backplane`), the complete production
console (`console/`) and the license. No Node.js runtime is needed.

From the extracted directory, provide your deployment's PostgreSQL, Consul,
Valkey and operator credentials, then run:

```sh
export BACKPLANE_PG_DSN='postgres://backplane:password@postgres:5432/backplane?sslmode=require'
export BACKPLANE_CONSUL_ADDR='consul:8500'
export BACKPLANE_VALKEY_ADDR='valkey:6379'
export BACKPLANE_ADMIN_TOKEN='your-operator-secret'
export BACKPLANE_CONSOLE_ASSETS_DIR="$PWD/console"
export BACKPLANE_CONSOLE_PREFIX='/backplane'
./bin/backplane
```

The console listens on :8081, platform probes on :9400.
Expose the console through HTTPS; the cookie is secure by default. Configure
NATS, Temporal and the
OTel/Victoria stack are optional; enable only the capabilities you use.

Verify downloads with `sha256sum --ignore-missing -c SHA256SUMS` from the release.
The console is built for `/backplane/`; another asset base requires rebuilding
with `BACKPLANE_CONSOLE_BASE` and setting the matching console prefix or host.

Full configuration and examples:
https://github.com/gopherex/backplane/blob/master/deployments/README.md
