// backplane is the platform server: a service on its own SDK. The tree and
// what it serves are assembled in internal/server; this is the process.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/backplane-labs/backplane/internal/server"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/standard"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	svc, err := backplane.Open(ctx, server.NewState, standard.Drivers(), backplane.Name(server.Name))
	if err != nil {
		return err //nolint:wrapcheck // prefixed by backplane
	}

	server.Declare(svc)

	return svc.Run(ctx) //nolint:wrapcheck // prefixed by backplane
}
