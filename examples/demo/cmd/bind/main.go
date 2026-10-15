// Command bind saves binding definitions (YAML files, as the console's
// editor shows them) through the console API: a scriptable Wiring save for
// development stacks.
//
//	BACKPLANE_ADMIN_TOKEN=… go run ./examples/demo/cmd/bind binding.yaml…
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/examples/demo"
	"github.com/backplane-labs/backplane/internal/bindings"
)

var (
	errToken = errors.New("BACKPLANE_ADMIN_TOKEN is required")
	errUsage = errors.New("usage: bind file.yaml…")
	errSave  = errors.New("binding rejected")
)

func run() error {
	token := os.Getenv("BACKPLANE_ADMIN_TOKEN")
	if token == "" {
		return errToken
	}

	files := os.Args[1:]
	if len(files) == 0 {
		return errUsage
	}

	base := os.Getenv("BACKPLANE_DEMO_URL")
	if base == "" {
		base = "http://localhost:8081"
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	conn, err := demo.Connect(ctx, base, token)
	if err != nil {
		return fmt.Errorf("bind: %w", err)
	}
	defer conn.Close()

	call := demo.Unary(conn)

	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // the files are the command's arguments
		if err != nil {
			return fmt.Errorf("bind: %w", err)
		}

		b, err := bindings.BindingYAML(string(raw))
		if err != nil {
			return fmt.Errorf("bind %s: %w", file, err)
		}

		var saved consolev1.SaveBindingResponse
		if err := call(ctx, "/backplane.console.v1.BindingService/SaveBinding",
			&consolev1.SaveBindingRequest{Definition: b.PB(), Comment: "bind " + file}, &saved); err != nil {
			return fmt.Errorf("bind %s: %w", file, err)
		}

		if len(saved.GetViolations()) > 0 {
			return fmt.Errorf("%w: %s: %v", errSave, file, saved.GetViolations())
		}

		fmt.Fprintf(os.Stdout, "%s: %s v%d\n", file, b.PB().GetHook(), saved.GetVersion().GetVersion())
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
