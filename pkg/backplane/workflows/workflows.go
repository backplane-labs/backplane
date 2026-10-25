// Package workflows gives a service its own Temporal workflows (design §9):
// the SDK owns the connection and the worker on the service's task queue
// (= its name); the author registers on it and uses the client. Between
// services go through hooks, never through another service's queue.
//
//	workflows.Register(root, func(r worker.Registry) {
//	    r.RegisterWorkflow(orders.Ship)
//	    r.RegisterActivity(orders.Charge)
//	})
//
//	c, err := workflows.Client(scope)
//	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: workflows.Queue(scope)}, orders.Ship, in)
//
// Declare registers one workflow and records it in the manifest with its
// input and output schemas, so the console can start it with a form:
//
//	workflows.Declare(root, "Ship", orders.Ship, workflows.Describe("ships an order"))
//
// Schedules are managed by the platform administrative API, never by SDK startup.
//
// The worker is tuned by configuration, not code: BACKPLANE_TEMPORAL_WORKER_*
// (config.Worker).
package workflows

import (
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
)

var errClient = errors.New("workflows: unexpected Temporal client")

// Register adds fn's workflows and activities to the service's worker.
// Declare it before Run, like any declaration.
func Register(scope deps.Scope, fn func(r worker.Registry)) {
	e, _ := decl.Env(scope, "workflows")
	if e.Manifest.Sealed() {
		panic("backplane: workflows registered after Run")
	}

	e.RegisterWorker(func(registry any) {
		if r, ok := registry.(worker.Registry); ok {
			fn(r)
		}
	})
}

// Client is the service's Temporal client. It fails with an error wrapping
// hook.ErrUnavailable while Temporal is not configured or not connected.
func Client(scope deps.Scope) (client.Client, error) {
	e, _ := decl.Env(scope, "workflows client")

	c, err := e.WorkflowClient()
	if err != nil {
		return nil, fmt.Errorf("workflows: %w", err)
	}

	tc, ok := c.(client.Client)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errClient, c)
	}

	return tc, nil
}

// Codec is the codec of the installation's workflow payloads
// (BACKPLANE_TEMPORAL_ENCRYPTION_*): history read through the raw service
// API is decoded with it. Without encryption it is temporal.Plain, which
// changes nothing.
func Codec(scope deps.Scope) temporal.Codec {
	e, _ := decl.Env(scope, "workflows codec")

	if c, ok := e.PayloadCodec().(temporal.Codec); ok {
		return c
	}

	return temporal.Plain
}

// Queue is the service's task queue.
func Queue(scope deps.Scope) string {
	e, _ := decl.Env(scope, "workflows queue")

	return e.Service
}
