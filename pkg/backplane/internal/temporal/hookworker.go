package temporal

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// StartHookWorker runs the hooks worker: CallHookWorkflow alone on the
// hooks queue <service>.hooks. Hook calls from outside workflow code run
// there, so they work from the start of the author's tree to its stop —
// the main worker (StartWorker) starts after the tree and stops before it.
//
// The hooks queue is its own, not the service's: a worker polling a queue
// fails the workflow tasks of the types it lacks, so a queue is served only
// by workers registering the same types. The hooks worker runs on every
// replica that has hooks, whatever worker.enabled says: it is what makes
// Call work there.
//
// Like StartWorker it does not wait for Temporal: the worker starts on g
// as soon as the connection is up, retrying a failed start. Nothing starts
// when the service declared no hooks.
func (c *Client) StartHookWorker(_ context.Context, g node.Group) error {
	m, err := c.p.Env.Manifest.Build()
	if err != nil || len(m.GetHooks()) == 0 {
		return nil //nolint:nilerr // a broken manifest fails Run, not this node
	}

	g.Go(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-c.ready:
		}

		_ = backoff.Retry(ctx, c.retry, func(context.Context) error { return c.startHookWorker() },
			func(err error, in time.Duration) {
				c.log.Warn("temporal hooks worker start failed, retrying", xlog.Err(err), xlog.Duration("retry_in", in))
			})

		return nil
	})

	return nil
}

func (c *Client) startHookWorker() error {
	c.mu.Lock()
	conn, done := c.client, c.closed || c.unhook
	c.mu.Unlock()

	if done || conn == nil {
		return nil
	}

	queue := HooksQueue(c.p.Service)
	w := worker.New(conn, queue, worker.Options{
		WorkerStopTimeout: stopGrace,
		OnFatalError: func(err error) {
			c.log.Error("temporal hooks worker stopped on a fatal error", xlog.Err(err))
		},
	})
	w.RegisterWorkflowWithOptions(callHook, workflow.RegisterOptions{Name: CallHookWorkflow})

	if err := w.Start(); err != nil {
		w.Stop()

		return fmt.Errorf("temporal: start hooks worker: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.unhook {
		go w.Stop()

		return nil
	}

	c.hooks = w
	c.log.Info("temporal hooks worker started", xlog.String("task_queue", queue))

	return nil
}

// StopHookWorker stops the hooks worker within ctx. A CallHook run in
// flight is not lost: Temporal hands its next task to another replica's
// hooks worker, or to this service's next start.
func (c *Client) StopHookWorker(ctx context.Context) error {
	c.mu.Lock()
	w := c.hooks
	c.hooks, c.unhook = nil, true
	c.mu.Unlock()

	return c.stop(ctx, w, "hooks worker")
}
