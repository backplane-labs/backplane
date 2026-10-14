// Package temporal runs the service's activities and hook calls on
// Temporal (design §7.2, §9).
//
// Names: task queue = service name; activity type = activity name as
// declared; hook = Nexus operation <Name> of Nexus service <service>.Hooks
// on endpoint <service>. A hook raised outside workflow code runs through
// the short workflow backplane.CallHook.v1 on the service's hooks queue
// <service>.hooks, served by a worker of its own, id
// hook/<service>/<Name>/<uuid> or, with a key, hook/<service>/<Name>/<key>.
package temporal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/wire"
	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

const (
	// stopGrace is how long a stopping worker lets activities finish; the
	// stop budget of the service bounds it in practice.
	stopGrace = time.Minute
	scopeName = "github.com/backplane-labs/backplane/temporal"
	// Bounds of the delay between connection and worker start attempts.
	retryFloor = time.Second
	retryCeil  = 30 * time.Second
	// healthEvery is how often a connected client checks the connection.
	healthEvery = 5 * time.Second
)

// Memo and search attributes of a hook call's workflow (§14): who raised
// it. The search attributes are set only when the namespace has them
// (Keyword); the memo always.
const (
	MemoSource        = wire.MemoSource
	AttrService       = wire.AttrService
	AttrHook          = wire.AttrHook
	transportTemporal = "temporal"
)

// Params of New.
type Params struct {
	// Conn is the connection: address, namespace, TLS, API key, dial
	// timeout (one attempt; Connect makes one before going to the
	// background).
	Conn     inftemporal.Config
	Service  string
	Instance string
	Log      *xlog.Logger
	Env      *env.Env
	Worker   Tuning

	// HookTimeout is the deadline of a hook call nothing else bounds; 0 is
	// env.DefaultHookTimeout. New installs it on Env for workflows.CallHook.
	HookTimeout time.Duration
}

// Tuning of the worker; a zero field is Temporal's default.
type Tuning struct {
	MaxConcurrentActivities    int
	MaxConcurrentWorkflowTasks int
	ActivityPollers            int
	WorkflowPollers            int
}

// Client is the service's Temporal connection and workers.
type Client struct {
	p      Params
	log    *xlog.Logger
	retry  backoff.Policy // between connection and worker start attempts
	health time.Duration  // between connection checks

	mu      sync.Mutex
	client  client.Client // nil until connected
	lastErr error         // of the last failed dial
	closed  bool
	worker  worker.Worker // nil until started
	stopped bool          // StopWorker ran: a late start is undone
	hooks   worker.Worker // the hooks worker, nil until started
	unhook  bool          // StopHookWorker ran
	ready   chan struct{} // closed once client is set

	connected atomic.Bool
	endpoint  atomic.Bool  // the service's Nexus endpoint was seen
	attrs     atomic.Int32 // search attributes: 0 not checked, attrsSet, attrsMissing
}

const (
	attrsSet int32 = iota + 1
	attrsMissing
)

// New creates the client; nothing connects until Connect.
func New(p Params) *Client {
	p.Conn = p.Conn.WithDefaults()

	log := p.Log
	if log == nil {
		log = xlog.New(nil)
	}

	if p.Env != nil {
		p.Env.SetHookTimeout(p.HookTimeout)
	}

	return &Client{
		p: p, log: log.With(xlog.String("component", "temporal")),
		retry:  backoff.Policy{Min: retryFloor, Max: retryCeil},
		health: healthEvery, ready: make(chan struct{}),
	}
}

// Connect dials without blocking on an unreachable Temporal: after one short
// attempt it keeps retrying on g and returns. Until connected, Call fails
// fast with env.ErrUnavailable and the workers wait. Once connected, the
// connection is checked on g every few seconds (Connected). A TLS setting
// that cannot work is an error.
func (c *Client) Connect(ctx context.Context, g node.Group) error {
	opts, err := c.options()
	if err != nil {
		return err
	}

	g.Go(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-c.ready:
		}

		c.watch(ctx)

		return nil
	})

	if err := c.dial(ctx, opts); err == nil {
		return nil
	}

	c.log.Warn("temporal unreachable, connecting in background", xlog.String("addr", c.p.Conn.Addr), xlog.Err(c.dialErr()))

	g.Go(func(ctx context.Context) error {
		err := backoff.Retry(ctx, c.retry, func(ctx context.Context) error { return c.dial(ctx, opts) },
			func(err error, in time.Duration) {
				c.log.Debug("temporal unreachable", xlog.Err(err), xlog.Duration("retry_in", in))
			})
		if err == nil {
			c.log.Info("temporal connected", xlog.String("addr", c.p.Conn.Addr))
		}

		return nil
	})

	return nil
}

// dial makes one connection attempt.
func (c *Client) dial(ctx context.Context, opts client.Options) error {
	conn, err := inftemporal.Dial(ctx, c.p.Conn, opts)

	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case err != nil:
		c.lastErr = err

		return err //nolint:wrapcheck // Dial names temporal and the address
	case c.closed:
		conn.Close()

		return nil
	default:
		c.client, c.lastErr = conn, nil
		close(c.ready)
		c.setConnected(ctx, true)

		return nil
	}
}

func (c *Client) options() (client.Options, error) {
	//nolint:wrapcheck // Options names temporal
	return inftemporal.Options(c.p.Conn, c.p.Instance, c.log)
}

func (c *Client) dialErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lastErr
}

// current is the connected client or nil.
func (c *Client) current() client.Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.client
}

// SDK is the connected Temporal client for the author's own workflows;
// ErrUnavailable while not connected.
func (c *Client) SDK() (client.Client, error) {
	if conn := c.current(); conn != nil {
		return conn, nil
	}

	if err := c.dialErr(); err != nil {
		return nil, fmt.Errorf("temporal: %w: %w", env.ErrUnavailable, err)
	}

	return nil, fmt.Errorf("temporal: not connected yet: %w", env.ErrUnavailable)
}

// Close closes the connection; a dial still in flight is dropped.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	if c.client != nil {
		c.client.Close()
	}

	c.setConnected(ctx, false)

	return nil
}

// Call implements env.Caller: a hook call from outside a workflow. It runs
// the workflow backplane.CallHook.v1 on the hooks queue <service>.hooks,
// which raises the Nexus operation, and waits for it within ctx
// (Params.HookTimeout when ctx has no deadline).
//
// With a key (env.WithCallKey) the workflow id is hook/<service>/<Name>/<key>:
// a call while one with the key runs joins it, a call after one with the
// key completed gets its result without running again, a call after one
// with the key failed runs anew. Temporal keeps a completed run for the
// namespace's retention; past it the key is forgotten.
func (c *Client) Call(ctx context.Context, hook string, in []byte) ([]byte, error) {
	conn := c.current()
	if conn == nil {
		if err := c.dialErr(); err != nil {
			return nil, fmt.Errorf("temporal unreachable: %w: %w", env.ErrUnavailable, err)
		}

		return nil, fmt.Errorf("temporal not connected: %w", env.ErrUnavailable)
	}

	name, ok := strings.CutPrefix(hook, c.p.Service+".")
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a hook of %s", errHookName, hook, c.p.Service)
	}

	timeout := c.hookTimeout()
	if d, set := ctx.Deadline(); set {
		timeout = time.Until(d)
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("timed out: %w", context.DeadlineExceeded)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := c.checkEndpoint(ctx, conn); err != nil {
		return nil, err
	}

	call := &backplanev1.HookCall{
		Hook: hook, Instance: c.p.Instance, Payload: in, Trace: Inject(ctx), Deadline: durationpb.New(timeout),
	}

	run, err := conn.ExecuteWorkflow(ctx, c.startOptions(ctx, conn, name, timeout), CallHookWorkflow, call)
	if err != nil {
		return nil, callError(ctx, fmt.Errorf("start: %w", err))
	}

	var res backplanev1.HookResult
	if err := run.Get(ctx, &res); err != nil {
		return nil, callError(ctx, err)
	}

	return res.GetPayload(), nil
}

// hookTimeout is the platform default of a call.
func (c *Client) hookTimeout() time.Duration {
	if c.p.HookTimeout > 0 {
		return c.p.HookTimeout
	}

	return env.DefaultHookTimeout
}

// startOptions of the CallHook workflow of hook name: id, hooks queue,
// timeout, who raised it (memo, search attributes when the namespace has
// them), and the key's dedup policies.
func (c *Client) startOptions(
	ctx context.Context, conn client.Client, name string, timeout time.Duration,
) client.StartWorkflowOptions {
	opts := client.StartWorkflowOptions{
		ID:                       HookWorkflowID(c.p.Service, name, uuid.NewString()),
		TaskQueue:                HooksQueue(c.p.Service),
		WorkflowExecutionTimeout: timeout,
		Memo:                     map[string]any{MemoSource: c.p.Service + "/" + c.p.Instance},
	}

	if key := env.CallKeyOf(ctx); key != "" {
		opts.ID = HookWorkflowID(c.p.Service, name, key)
		opts.WorkflowIDConflictPolicy = enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING
		opts.WorkflowIDReusePolicy = enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY
	}

	if c.searchAttributes(ctx, conn) {
		opts.TypedSearchAttributes = temporal.NewSearchAttributes(
			temporal.NewSearchAttributeKeyKeyword(AttrService).ValueSet(c.p.Service),
			temporal.NewSearchAttributeKeyKeyword(AttrHook).ValueSet(c.p.Service+"."+name),
		)
	}

	return opts
}

// HookWorkflowID is the id of the CallHook workflow of <service>.<name>
// under suffix (a uuid, or the call's key).
func HookWorkflowID(service, name, suffix string) string {
	return "hook/" + service + "/" + name + "/" + suffix
}

// HooksQueue is the task queue of service's CallHook workflows.
func HooksQueue(service string) string { return wire.HooksQueue(service) }

// searchAttributes reports whether the namespace has the Keyword search
// attributes BpService and BpHook; checked once. A server that refuses
// the check (no operator permission) counts as without them.
func (c *Client) searchAttributes(ctx context.Context, conn client.Client) bool {
	switch c.attrs.Load() {
	case attrsSet:
		return true
	case attrsMissing:
		return false
	}

	res, err := conn.OperatorService().ListSearchAttributes(ctx, &operatorservice.ListSearchAttributesRequest{
		Namespace: c.p.Conn.Namespace,
	})
	if err != nil {
		if ctx.Err() != nil {
			return false // not a verdict: checked again next call
		}

		c.log.Debug("search attributes not checked: hook calls go without them", xlog.Err(err))
		c.attrs.Store(attrsMissing)

		return false
	}

	custom := res.GetCustomAttributes()
	has := custom[AttrService] == enumspb.INDEXED_VALUE_TYPE_KEYWORD &&
		custom[AttrHook] == enumspb.INDEXED_VALUE_TYPE_KEYWORD

	if has {
		c.attrs.Store(attrsSet)
	} else {
		c.attrs.Store(attrsMissing)
	}

	return has
}

var errHookName = errors.New("temporal: bad hook name")

// checkEndpoint makes sure backplane registered the service's Nexus
// endpoint before a call that would otherwise wait out its deadline on a
// missing one. Once seen it is not checked again; a server that refuses the
// check (no operator permission) lets the call go.
func (c *Client) checkEndpoint(ctx context.Context, conn client.Client) error {
	if c.endpoint.Load() {
		return nil
	}

	res, err := conn.OperatorService().ListNexusEndpoints(ctx, &operatorservice.ListNexusEndpointsRequest{
		Name: c.p.Service, PageSize: 1,
	})

	switch {
	case err != nil:
		c.log.Debug("nexus endpoint not checked", xlog.Err(err))

		return nil
	case len(res.GetEndpoints()) == 0:
		return fmt.Errorf("no Nexus endpoint %q, backplane has not registered the service: %w",
			c.p.Service, env.ErrUnavailable)
	}

	c.endpoint.Store(true)

	return nil
}

// callError: the caller's deadline or cancellation wins over whatever
// Temporal reported at that moment.
// deadlineSlack absorbs the clock skew between the server's view of the
// deadline and ours.
const deadlineSlack = 50 * time.Millisecond

func deadlineReached(ctx context.Context) bool {
	dl, ok := ctx.Deadline()

	return ok && !time.Now().Before(dl.Add(-deadlineSlack))
}

func callError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w", context.Cause(ctx))
	}

	// The server can report the deadline a moment before ctx notices it,
	// in any wrapping (gRPC status, workflow or Nexus timeout): once the
	// call's deadline is (all but) reached, that is what happened.
	var timeout *temporal.TimeoutError
	if status.Code(err) == codes.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &timeout) || deadlineReached(ctx) {
		return fmt.Errorf("%w", context.DeadlineExceeded)
	}

	return Local(err)
}
