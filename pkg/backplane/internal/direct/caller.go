package direct

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
	"github.com/backplane-labs/backplane/pkg/backplane/meta"
)

// NoBindingType is the failure type of a required hook without a binding.
const NoBindingType = "backplane.NoBinding"

// Discovery finds the platform addresses (host:port) of backplane's
// replicas; none means backplane is not known to be running.
type Discovery interface {
	Backplanes(ctx context.Context) ([]string, error)
}

// DiscoveryFunc adapts a function.
type DiscoveryFunc func(ctx context.Context) ([]string, error)

// Backplanes implements Discovery.
func (f DiscoveryFunc) Backplanes(ctx context.Context) ([]string, error) { return f(ctx) }

// Caller carries hook calls to backplane's HookService: env.Caller for the
// direct transport. A call goes to one replica; UNAVAILABLE from it tries
// another once.
type Caller struct {
	service  string
	instance string
	secret   string
	timeout  time.Duration
	find     Discovery
	dial     func(addr string) (*grpc.ClientConn, error)

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

// CallerParams builds a Caller.
type CallerParams struct {
	Service  string
	Instance string
	// Secret is the installation's internal secret, presented to backplane.
	Secret string
	// Timeout of a call nothing else bounds; 0 is env.DefaultHookTimeout.
	Timeout   time.Duration
	Discovery Discovery
	// Dial replaces the gRPC dial (tests).
	Dial func(addr string) (*grpc.ClientConn, error)
}

// NewCaller creates the caller.
func NewCaller(p CallerParams) *Caller {
	c := &Caller{
		service: p.Service, instance: p.Instance, secret: p.Secret, timeout: p.Timeout, find: p.Discovery,
		dial: p.Dial, conns: map[string]*grpc.ClientConn{},
	}
	if c.dial == nil {
		c.dial = func(addr string) (*grpc.ClientConn, error) {
			return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		}
	}

	return c
}

var _ env.Caller = (*Caller)(nil)

var errHookName = errors.New("hook name")

// Call implements env.Caller: HookService.Call on a replica of backplane,
// within ctx's deadline (the configured timeout when it has none). The
// error is env.ErrNoBinding, env.ErrUnavailable (no replica, none
// reachable), context.DeadlineExceeded, or the run's failure with its
// message.
func (c *Caller) Call(ctx context.Context, hook string, in []byte) ([]byte, error) {
	if _, ok := cutService(hook, c.service); !ok {
		return nil, fmt.Errorf("%w: %q is not a hook of %s", errHookName, hook, c.service)
	}

	timeout := c.timeout
	if timeout <= 0 {
		timeout = env.DefaultHookTimeout
	}

	if d, set := ctx.Deadline(); set {
		timeout = time.Until(d)
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("timed out: %w", context.DeadlineExceeded)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addrs, err := c.find.Backplanes(ctx)
	if err != nil {
		return nil, fmt.Errorf("backplane unreachable: %w: %w", env.ErrUnavailable, err)
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("no backplane replica is serving: %w", env.ErrUnavailable)
	}

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	call := &backplanev1.HookCall{
		Hook: hook, Instance: c.instance, Payload: in, Trace: carrier, Deadline: durationpb.New(timeout),
		Metadata: meta.From(ctx),
	}
	ctx = metadata.AppendToOutgoingContext(ctx, guard.Header, c.secret, KeyHeader, env.CallKeyOf(ctx))

	rand.Shuffle(len(addrs), func(i, j int) { addrs[i], addrs[j] = addrs[j], addrs[i] })

	var last error

	for i, addr := range addrs {
		res, err := c.callOne(ctx, addr, call)
		if err == nil {
			return res.GetResult().GetPayload(), nil
		}

		last = err

		// A replica that cannot be reached is not the answer; one that
		// answered is, whatever it said.
		if status.Code(err) != codes.Unavailable || i == len(addrs)-1 || ctx.Err() != nil {
			break
		}
	}

	return nil, Local(ctx, last)
}

// KeyHeader carries the call's idempotency key (hook.Key) to backplane.
const KeyHeader = "bp-call-key"

func (c *Caller) callOne(
	ctx context.Context, addr string, call *backplanev1.HookCall,
) (*consolev1.RaiseResponse, error) {
	conn, err := c.conn(addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "dial %s: %v", addr, err)
	}

	//nolint:wrapcheck // mapped by Local
	return consolev1.NewHookServiceClient(conn).Raise(ctx, &consolev1.RaiseRequest{Call: call})
}

func (c *Caller) conn(addr string) (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cc, ok := c.conns[addr]; ok {
		return cc, nil
	}

	cc, err := c.dial(addr)
	if err != nil {
		return nil, err
	}

	c.conns[addr] = cc

	return cc, nil
}

// Close closes the connections.
func (c *Caller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for addr, cc := range c.conns {
		_ = cc.Close()

		delete(c.conns, addr)
	}
}

// Local maps a HookService error to what the caller sees: env.ErrNoBinding,
// context.DeadlineExceeded, env.ErrUnavailable, or the run's failure with
// its message.
func Local(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w", context.Cause(ctx))
	}

	st, _ := status.FromError(err)
	typ, _ := FailureOf(err)

	switch {
	case typ == NoBindingType:
		return env.ErrNoBinding
	case st.Code() == codes.DeadlineExceeded:
		return fmt.Errorf("%w", context.DeadlineExceeded)
	case st.Code() == codes.Unavailable, st.Code() == codes.PermissionDenied, st.Code() == codes.Unimplemented:
		return fmt.Errorf("%s: %w", st.Message(), env.ErrUnavailable)
	default:
		return remote{msg: st.Message(), err: err}
	}
}

// remote is a failure reported by backplane.
type remote struct {
	msg string
	err error
}

func (r remote) Error() string { return r.msg }

func (r remote) Unwrap() error { return r.err }
