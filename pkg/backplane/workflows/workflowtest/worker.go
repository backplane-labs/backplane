package workflowtest

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
)

// ErrEndpointTaken: the Nexus endpoint named after the service exists and
// routes somewhere else than the service's task queue in the namespace.
var ErrEndpointTaken = errors.New("nexus endpoint taken")

// endpointCleanup bounds the endpoint's deletion at the end of the test.
const endpointCleanup = 10 * time.Second

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
// production. The worker is a component of the harness tree: it starts
// with Start and stops with the tree. Call it after declaring and before
// Start.
//
// When the service declares hooks, a Nexus endpoint named after the
// service routes workflows.CallHook to its task queue, answered by
// backplanetest.Answer of the harness whose worker takes the call. The
// endpoint belongs to the test, not to one harness: a Worker joins one
// that already routes to the same queue, and the endpoint is deleted when
// the test ends. Replicas of one service (harnesses of the same name in
// one test) therefore share the queue and the endpoint as in production:
// one stops, the others take its workflows and hook calls. Answer the
// hooks on every replica.
//
// Endpoints are cluster-wide: separate tests that run at once on one
// cluster need distinct service names (backplanetest.Name), or they share
// one queue. An endpoint of the name that routes elsewhere fails the start
// with ErrEndpointTaken.
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
		endpoint atomic.Pointer[nexuspb.Endpoint]
	)

	// Registered before the harness's Start, so it runs after the tree
	// stopped.
	tb.Cleanup(func() {
		if ep := endpoint.Load(); ep != nil {
			if err := deleteEndpoint(context.WithoutCancel(tb.Context()), c, ep); err != nil {
				tb.Errorf("workflowtest: %v", err)
			}
		}
	})

	n.OnStart(func(ctx context.Context) error {
		next := worker.New(c, e.Service, worker.Options{})

		hooked, err := register(next, e)
		if err != nil {
			return err
		}

		if hooked {
			ep, err := ensureEndpoint(ctx, c, e.Service, o.namespace)
			if err != nil {
				return err
			}

			endpoint.Store(ep)
		}

		if err := next.Start(); err != nil {
			return fmt.Errorf("worker %s: %w", e.Service, err)
		}

		w = next

		return nil
	})

	n.OnStop(func(context.Context) error {
		if w != nil {
			w.Stop()
		}

		return nil
	})
}

// ensureEndpoint is the Nexus endpoint name routing to the queue of the
// same name in namespace: created, or the existing one when it routes
// there.
func ensureEndpoint(ctx context.Context, c client.Client, name, namespace string) (*nexuspb.Endpoint, error) {
	target := &nexuspb.EndpointTarget_Worker{Namespace: namespace, TaskQueue: name}

	res, err := c.OperatorService().CreateNexusEndpoint(ctx, &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{
			Name:   name,
			Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{Worker: target}},
		},
	})

	var exists *serviceerror.AlreadyExists

	switch {
	case err == nil:
		return res.GetEndpoint(), nil
	case !errors.As(err, &exists):
		return nil, fmt.Errorf("nexus endpoint %s: %w", name, err)
	}

	list, err := c.OperatorService().ListNexusEndpoints(ctx, &operatorservice.ListNexusEndpointsRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("nexus endpoint %s: %w", name, err)
	}

	for _, ep := range list.GetEndpoints() {
		got := ep.GetSpec().GetTarget().GetWorker()
		if got.GetNamespace() == target.GetNamespace() && got.GetTaskQueue() == target.GetTaskQueue() {
			return ep, nil
		}
	}

	return nil, fmt.Errorf("nexus endpoint %s: %w", name, ErrEndpointTaken)
}

// deleteEndpoint deletes the endpoint; one deleted already is gone, as
// wanted.
func deleteEndpoint(ctx context.Context, c client.Client, endpoint *nexuspb.Endpoint) error {
	ctx, cancel := context.WithTimeout(ctx, endpointCleanup)
	defer cancel()

	_, err := c.OperatorService().DeleteNexusEndpoint(ctx, &operatorservice.DeleteNexusEndpointRequest{
		Id: endpoint.GetId(), Version: endpoint.GetVersion(),
	})

	var gone *serviceerror.NotFound
	if err != nil && !errors.As(err, &gone) {
		return fmt.Errorf("nexus endpoint %s: delete: %w", endpoint.GetSpec().GetName(), err)
	}

	return nil
}
