// formatter is hello's independently compiled companion service.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gopherex/backplane/examples/formatter/internal/formatter"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/api"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/drivers/standard"
)

type Config struct {
	config.Backplane `json:"backplane"`
	Formatter        formatter.Config `json:"formatter"`
}
type State struct{ Formatter *formatter.Formatter }

func newState(root backplane.Root[Config]) (*State, error) {
	cfg := root.Config()
	return &State{Formatter: formatter.New(root, &cfg.Formatter)}, nil
}

func run(ctx context.Context) error {
	svc, err := backplane.Open(ctx, newState, standard.Drivers())
	if err != nil {
		return fmt.Errorf("formatter: %w", err)
	}

	svc.HTTP("/formatter/", svc.State().Formatter.HTTP(), api.OpenAPI(formatter.API))

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
