package workflowtest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
)

// ErrEndpointTaken: the Nexus endpoint named after the service exists
// already — another harness of the same service runs on the cluster, or a
// killed test left it behind.
var ErrEndpointTaken = errors.New("nexus endpoint taken")

// WorkerOption tunes Worker.
type WorkerOption func(o *workerOptions)

type workerOptions struct{ namespace string }

// Namespace is the namespace c is connected to ("default" by default):
// the Nexus endpoint of the hooks targets it.
func Namespace(ns string) WorkerOption {
	return func(o *workerOptions) { o.namespace = ns }
}

// Worker runs the service's worker on a real Temporal server through c:
// the service's task queue (its name) serves what New registers — every
// activity, the workflows of workflows.Declare, workflows.Activity and
// workflows.Register — so workflows the service starts run as in
// production. When the service declares hooks, a Nexus endpoint named
// after the service routes workflows.CallHook to the same worker, answered
// by backplanetest.Answer as in New. The worker is a component of the
// harness tree: it starts with Start and stops with the tree, deleting the
// endpoint. Call it after declaring and before Start.
//
// The queue and the endpoint are named after the service, and endpoints
// are cluster-wide: harnesses running at once on one cluster need
// distinct service names (backplanetest.Name); a taken endpoint fails the
// start with ErrEndpointTaken.
//
//	workflowtest.Worker(t, h, c)
//	h.Start()
//	run, _ := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: h.Service()}, "Ship", in)
func Worker(tb testing.TB, h *backplanetest.Harness, c client.Client, opts ...WorkerOption) {
	tb.Helper()

	o := workerOptions{namespace: "default"}
	for _, opt := range opts {
		opt(&o)
	}

	e, _ := decl.Env(h.Root(), "workflow worker")
	n := deps.NewComponent(h.Root(), "temporal worker")

	var (
		w        worker.Worker
		endpoint *nexuspb.Endpoint
	)

	n.OnStart(func(ctx context.Context) error {
		next := worker.New(c, e.Service, worker.Options{})

		hooked, err := register(next, e)
		if err != nil {
			return err
		}

		if hooked {
			if endpoint, err = createEndpoint(ctx, c, e.Service, o.namespace); err != nil {
				return err
			}
		}

		if err := next.Start(); err != nil {
			return fmt.Errorf("worker %s: %w", e.Service, err)
		}

		w = next

		return nil
	})

	n.OnStop(func(ctx context.Context) error {
		if w != nil {
			w.Stop()
		}

		if endpoint == nil {
			return nil
		}

		if _, err := c.OperatorService().DeleteNexusEndpoint(ctx, &operatorservice.DeleteNexusEndpointRequest{
			Id: endpoint.GetId(), Version: endpoint.GetVersion(),
		}); err != nil {
			return fmt.Errorf("nexus endpoint %s: delete: %w", e.Service, err)
		}

		return nil
	})
}

// createEndpoint creates the Nexus endpoint name targeting the queue of
// the same name in namespace.
func createEndpoint(ctx context.Context, c client.Client, name, namespace string) (*nexuspb.Endpoint, error) {
	res, err := c.OperatorService().CreateNexusEndpoint(ctx, &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{
			Name: name,
			Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{
				Worker: &nexuspb.EndpointTarget_Worker{Namespace: namespace, TaskQueue: name},
			}},
		},
	})

	var exists *serviceerror.AlreadyExists

	switch {
	case errors.As(err, &exists):
		return nil, fmt.Errorf("nexus endpoint %s: %w", name, ErrEndpointTaken)
	case err != nil:
		return nil, fmt.Errorf("nexus endpoint %s: %w", name, err)
	}

	return res.GetEndpoint(), nil
}
