// formatter is hello's independently compiled companion service.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/backplane-labs/backplane/examples/formatter/internal/formatter"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	consuldriver "github.com/backplane-labs/backplane/pkg/backplane/drivers/consul"
)

type Config struct {
	config.Backplane `json:"backplane"`
	Formatter        formatter.Config `json:"formatter"`
}
type State struct{ Handler http.Handler }

func newState(root backplane.Root[Config]) (*State, error) {
	cfg := root.Config()
	return &State{Handler: formatter.HTTP(&cfg.Formatter)}, nil
}

func run(ctx context.Context) error {
	svc, err := backplane.Open(ctx, newState, consuldriver.Driver())
	if err != nil {
		return fmt.Errorf("formatter: %w", err)
	}

	svc.HTTP("/formatter/", svc.State().Handler)

	if err := svc.Run(ctx); err != nil {
		return fmt.Errorf("formatter: %w", err)
	}

	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
