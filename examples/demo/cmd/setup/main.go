// setup connects hello and formatter using the console API.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/backplane-labs/backplane/examples/demo"
)

var errToken = errors.New("BACKPLANE_ADMIN_TOKEN is required")

func run() error {
	token := os.Getenv("BACKPLANE_ADMIN_TOKEN")
	if token == "" {
		return errToken
	}

	base := os.Getenv("BACKPLANE_DEMO_URL")
	if base == "" {
		base = "http://localhost:8081"
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	conn, err := demo.Connect(ctx, base, token)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer conn.Close()

	id, err := demo.Install(ctx, demo.Unary(conn))
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	fmt.Fprintf(os.Stdout, "hello.Greet -> formatter.Format; hello.Greeted -> formatter.Record (rule %s)\n", id)

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
