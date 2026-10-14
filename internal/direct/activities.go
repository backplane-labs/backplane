package direct

import (
	"context"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/registry"
)

// internalSecretHeader is the SDK guard's gRPC metadata key.
const internalSecretHeader = "bp-internal-secret" //nolint:gosec // a header name, not a secret

// maxMessage bounds a call's payloads either way.
const maxMessage = 16 << 20

// Caller runs one activity call: the Activities transport, a fake in tests.
type Caller interface {
	Call(ctx context.Context, call *backplanev1.ActivityCall, timeout time.Duration) (*backplanev1.ActivityResult, error)
}

// Activities calls the services' activities over their platform ports:
// ActivityService.Run on a serving instance whose manifest declares the
// activity, found in the registry. An instance that cannot be reached is
// not the answer: the next one is tried, once per instance.
type Activities struct {
	src    registry.Source
	secret string
	dial   func(addr string) (*grpc.ClientConn, error)

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

// NewActivities creates the transport over the registry, presenting the
// installation's internal secret.
func NewActivities(src registry.Source, secret string) *Activities {
	return &Activities{
		src: src, secret: secret, conns: map[string]*grpc.ClientConn{},
		dial: func(addr string) (*grpc.ClientConn, error) {
			return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMessage), grpc.MaxCallSendMsgSize(maxMessage)))
		},
	}
}

var _ Caller = (*Activities)(nil)

// Call implements Caller.
func (a *Activities) Call(
	ctx context.Context, call *backplanev1.ActivityCall, timeout time.Duration,
) (*backplanev1.ActivityResult, error) {
	service, name := bindings.SplitName(call.GetActivity())

	addrs := a.targets(service, name)
	if len(addrs) == 0 {
		return nil, errNoInstance(service, call.GetActivity())
	}

	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	ctx = metadata.AppendToOutgoingContext(ctx, internalSecretHeader, a.secret)

	var last error

	for i, addr := range addrs {
		res, err := a.callOne(ctx, addr, call)
		if err == nil {
			return res, nil
		}

		last = err

		if status.Code(err) != codes.Unavailable || i == len(addrs)-1 || ctx.Err() != nil {
			break
		}
	}

	return nil, last
}

func (a *Activities) callOne(
	ctx context.Context, addr string, call *backplanev1.ActivityCall,
) (*backplanev1.ActivityResult, error) {
	conn, err := a.conn(addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "dial %s: %v", addr, err)
	}

	res, err := backplanev1.NewActivityServiceClient(conn).Run(ctx, &backplanev1.RunRequest{Call: call})
	if err != nil {
		return nil, err //nolint:wrapcheck // read by callFailure
	}

	return res.GetResult(), nil
}

// targets are the platform addresses of the instances that run the
// activity, registered and healthy ones first, in random order within.
func (a *Activities) targets(service, activity string) []string {
	svc, ok := a.src.Current().Services[service]
	if !ok {
		return nil
	}

	var preferred, fallback []string

	for _, in := range svc.Instances {
		st := in.State
		if st.GetPhase() != backplanev1.InstancePhase_INSTANCE_PHASE_SERVING || st.GetPlatformPort() == 0 ||
			st.GetAddress() == "" || !declares(svc.Manifest(st.GetVersion()), activity) {
			continue
		}

		addr := net.JoinHostPort(st.GetAddress(), strconv.FormatUint(uint64(st.GetPlatformPort()), 10))
		if in.Registered && in.Healthy {
			preferred = append(preferred, addr)
		} else {
			fallback = append(fallback, addr)
		}
	}

	rand.Shuffle(len(preferred), func(i, j int) { preferred[i], preferred[j] = preferred[j], preferred[i] })
	rand.Shuffle(len(fallback), func(i, j int) { fallback[i], fallback[j] = fallback[j], fallback[i] })

	return append(preferred, fallback...)
}

func declares(m *backplanev1.Manifest, activity string) bool {
	for _, act := range m.GetActivities() {
		if act.GetName() == activity && act.GetKind() != backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW {
			return true
		}
	}

	return false
}

func (a *Activities) conn(addr string) (*grpc.ClientConn, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if cc, ok := a.conns[addr]; ok {
		return cc, nil
	}

	cc, err := a.dial(addr)
	if err != nil {
		return nil, err
	}

	a.conns[addr] = cc

	return cc, nil
}

// Prune closes the connections to addresses no instance has any more.
func (a *Activities) Prune() {
	live := map[string]bool{}

	for _, svc := range a.src.Current().Services {
		for _, in := range svc.Instances {
			if st := in.State; st != nil && st.GetPlatformPort() != 0 {
				live[net.JoinHostPort(st.GetAddress(), strconv.FormatUint(uint64(st.GetPlatformPort()), 10))] = true
			}
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for addr, cc := range a.conns {
		if !live[addr] {
			_ = cc.Close()

			delete(a.conns, addr)
		}
	}
}

// Close closes every connection.
func (a *Activities) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()

	for addr, cc := range a.conns {
		_ = cc.Close()

		delete(a.conns, addr)
	}
}
