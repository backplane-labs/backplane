# Build a Go service

The SDK entrypoint is `github.com/backplane-labs/backplane/pkg/backplane`. A service builds a typed state tree, registers its public and internal interfaces, then runs under a managed lifecycle.

```bash
go mod init example.com/my-service
go get github.com/backplane-labs/backplane@v0.1.1
```

## Minimal service

This complete program uses the core SDK without selecting infrastructure drivers:

```go
package main

import (
    "context"
    "fmt"
    "net/http"
    "os"

    "github.com/backplane-labs/backplane/pkg/backplane"
    "github.com/backplane-labs/backplane/pkg/backplane/config"
)

type Config struct {
    config.Backplane `json:"backplane"`
}

type State struct{}

func NewState(root backplane.Root[Config]) (*State, error) {
    return &State{}, nil
}

func run(ctx context.Context) error {
    svc, err := backplane.Open(ctx, NewState)
    if err != nil {
        return err
    }
    svc.HTTP("/hello/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = fmt.Fprintln(w, "Hello")
    }))
    return svc.Run(ctx)
}

func main() {
    if err := run(context.Background()); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}
```

Build identity is normally set by your build pipeline. The repository demonstrates linker flags for `pkg/backplane/build.Service`, `Version`, `Commit` and `Date`. Use a stable service name and a distinct build version when changing its contracts. Every version is immutable, including versions with `+` build metadata. If Consul already holds different declarations for the same service/version, `Run` fails before this process publishes its instance state or registers in the catalog. Replicas with identical declarations may share a version. See the [configuration reference](../reference/configuration.md) for identity overrides and listener defaults.

## Add infrastructure intentionally

For the standard integrations, import `github.com/backplane-labs/backplane/pkg/backplane/drivers/standard` and call:

```go
svc, err := backplane.Open(ctx, NewState, standard.Drivers())
```

Then configure the endpoints your deployment provides. Empty optional endpoints disable those runtimes. Register individual drivers when you want only a subset; the [dependency guide](../concepts/dependencies.md) explains the import boundary.

`Open` can fail during configuration, schema construction or tree creation. Return the error. After successful construction, `Run` owns startup and shutdown. If setup fails before `Run`, close the service before returning, as the hello example does when its UI filesystem cannot be opened.

## Recommended service layout

```text
cmd/service/main.go       configuration, NewState, endpoint registration
internal/store/          dependency providers
internal/domain/         components, hooks, events and activities
internal/flows/          Temporal workflows, if required
internal/web/            HTTP handlers and API documents
proto/                   public and internal RPC contracts
ui/                      built module bundle and embedding
```

Pass dependencies through constructors. Keep domain components independent of global clients. The complete [hello example](https://github.com/backplane-labs/backplane/tree/master/examples/hello) includes managed listeners, another independently served listener described in the API catalog, configuration, reactors, hooks, workflows and an embedded module.
