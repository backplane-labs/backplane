# Hello and formatter

`make dev` builds two independent Go services with the operator console. Open the console at `http://127.0.0.1:8081/backplane/` with the local operator token. Open Hello's module and refresh its statistics: the browser calls Hello's `AdminService.GetStats` through the authenticated relay. The relay cannot call the public greeting service.

```sh
curl 'http://127.0.0.1:8080/hello/?name=ann'
curl 'http://127.0.0.1:8082/formatter/?name=ann'
```

Hello renders its own greeting. Its greeter records the result and emits application audit through OTel. Formatter independently renders its prefix, name and suffix as a JSON string; it does not subscribe to Hello. Edit `greeter.suffix` or `formatter.prefix` under the corresponding service's Configuration page.

## Code and verification

- `hello/cmd/hello`: managed HTTP, gRPC and ws-proto listeners, an independently owned listener, live configuration, component health and an internal console API.
- `hello/internal/greeter`: service logic, tracing, counters and audit. `hello/internal/admin`: the separate console contract. `formatter/internal/formatter`: HTTP with live settings.
- `make test` checks the Go packages. With a dedicated stack from `make up`, `make test-m1` checks config, relay and bundles; `make test-replicas` checks config repair and audit across a replica crash. Do not register the demo services on that stack while running conformance.
- `make test-dev` exercises the running development installation. `make dev-down` stops it without clearing its storage.
