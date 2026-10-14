package console

import (
	"context"
	"errors"
	"io"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gopherex/ws-proto/wsrpc"

	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// Metadata the relay sets on a forwarded call (§11.1, §17).
const (
	// SecretHeader carries the installation's internal secret.
	SecretHeader = "bp-internal-secret" //nolint:gosec // a header name, not a credential
	// SessionHeader names the console session the call comes from.
	SessionHeader = "bp-console-session"

	// maxRelayMessage bounds one message either way: ws-proto's read limit.
	maxRelayMessage = 16 << 20
)

var errRawCodec = errors.New("console: relay codec: want *[]byte")

// relayDesc streams both ways: it carries unary and every streaming kind
// alike, the service's own server knows which one the method is.
//
//nolint:gochecknoglobals // a constant descriptor
var relayDesc = &grpc.StreamDesc{StreamName: "relay", ServerStreams: true, ClientStreams: true}

// relay forwards every method /ws has no handler for to the internal API
// of the service that declares it: frames as they are, both ways, over a
// pooled plaintext gRPC connection to a live instance's platform port.
//
// A relay is shared by pointer: it holds the pool.
type relay struct {
	src    registry.Source
	secret config.Secret

	mu    sync.Mutex
	pool  map[string]*grpc.ClientConn // by platform address
	ended bool
}

func newRelay(src registry.Source, secret config.Secret) *relay {
	return &relay{src: src, secret: secret, pool: map[string]*grpc.ClientConn{}}
}

// handle is the unknown-method handler of /ws.
func (rl *relay) handle(ctx context.Context, down *wsrpc.Stream) error {
	addr, err := rl.target(down.Method())
	if err != nil {
		return err
	}

	cc, err := rl.conn(addr)
	if err != nil {
		return err
	}

	out := forwarded(down.Header())
	if secret := rl.secret.Reveal(); secret != "" {
		out.Set(SecretHeader, secret)
	}

	if id, ok := SessionID(ctx); ok {
		out.Set(SessionHeader, id.String())
	}

	ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(ctx, out))
	defer cancel()

	upstream, err := cc.NewStream(ctx, relayDesc, down.Method(), grpc.ForceCodecV2(rawCodec{}))
	if err != nil {
		return err //nolint:wrapcheck // the upstream status as is
	}

	go pumpUp(down, upstream, cancel)

	if hdr, err := upstream.Header(); err == nil {
		if h := response(hdr); len(h) > 0 {
			_ = down.SendHeader(h)
		}
	}

	for {
		var frame []byte

		err := upstream.RecvMsg(&frame)
		if err != nil {
			down.SetTrailer(response(upstream.Trailer()))

			if errors.Is(err, io.EOF) {
				return nil
			}

			return err //nolint:wrapcheck // the upstream status as is
		}

		if err := down.SendRaw(frame); err != nil {
			return err //nolint:wrapcheck // the downstream status as is
		}
	}
}

// pumpUp forwards the browser's frames until it half-closes; a broken
// downstream cancels the call.
func pumpUp(down *wsrpc.Stream, upstream grpc.ClientStream, cancel context.CancelFunc) {
	for {
		b, err := down.RecvRaw()
		if errors.Is(err, io.EOF) {
			_ = upstream.CloseSend()

			return
		}

		if err != nil {
			cancel()

			return
		}

		// io.EOF: the call ended; RecvMsg reports how.
		if err := upstream.SendMsg(&b); err != nil {
			return
		}
	}
}

// target is the platform address of an instance serving method's service:
// PERMISSION_DENIED unless a service's latest manifest declares it as its
// internal API, UNAVAILABLE without a serving instance.
func (rl *relay) target(method string) (string, error) {
	name, ok := serviceOf(method)
	if !ok {
		return "", status.Errorf(codes.PermissionDenied, "%s: not a method", method)
	}

	cat := rl.src.Current()

	var owners []string

	for _, svcName := range slices.Sorted(maps.Keys(cat.Services)) {
		if slices.Contains(cat.Services[svcName].Latest().GetInternalServices(), name) {
			owners = append(owners, svcName)
		}
	}

	if len(owners) == 0 {
		return "", status.Errorf(codes.PermissionDenied, "%s is not the internal API of any service", method)
	}

	if len(owners) > 1 {
		return "", status.Errorf(codes.FailedPrecondition,
			"%s is declared by multiple services: %s", name, strings.Join(owners, ", "))
	}

	svc := cat.Services[owners[0]]

	preferred, fallback := serving(svc, internalService(name))
	if len(preferred) == 0 {
		preferred = fallback
	}

	if len(preferred) == 0 {
		return "", status.Errorf(codes.Unavailable, "%s: no serving instance of %s", method, owners[0])
	}

	return platformAddr(preferred[rand.IntN(len(preferred))]), nil //nolint:gosec // load spreading
}

// serviceOf is the full service name of "/pkg.Service/Method".
func serviceOf(method string) (string, bool) {
	name, _, ok := strings.Cut(strings.TrimPrefix(method, "/"), "/")

	return name, ok && name != "" && strings.HasPrefix(method, "/")
}

// conn is the pooled connection to addr.
func (rl *relay) conn(addr string) (*grpc.ClientConn, error) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.ended {
		return nil, status.Errorf(codes.Unavailable, "console is stopping")
	}

	if cc, ok := rl.pool[addr]; ok {
		return cc, nil
	}

	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxRelayMessage), grpc.MaxCallSendMsgSize(maxRelayMessage)))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "dial %s: %v", addr, err)
	}

	rl.pool[addr] = cc

	return cc, nil
}

// prune closes the connections to platform addresses no instance has any
// more.
func (rl *relay) prune(cat registry.Catalog) {
	live := map[string]bool{}

	for _, svc := range cat.Services {
		for _, in := range svc.Instances {
			if in.State.GetPlatformPort() != 0 {
				live[platformAddr(in)] = true
			}
		}
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	for addr, cc := range rl.pool {
		if !live[addr] {
			_ = cc.Close()

			delete(rl.pool, addr)
		}
	}
}

// close ends the pool; later calls are UNAVAILABLE.
func (rl *relay) close() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	for _, cc := range rl.pool {
		_ = cc.Close()
	}

	rl.pool, rl.ended = map[string]*grpc.ClientConn{}, true
}

// forwarded is the browser's metadata minus what it must not set: the
// authorization, backplane's own bp-* and transport-reserved keys.
func forwarded(in map[string]string) metadata.MD {
	out := metadata.MD{}

	for k, v := range in {
		k = strings.ToLower(k)
		if reserved(k) || k == "authorization" || strings.HasPrefix(k, "bp-") || k == "cookie" {
			continue
		}

		out.Append(k, v)
	}

	return out
}

// response is the upstream's header or trailer metadata for the browser.
func response(md metadata.MD) map[string]string {
	out := wsrpc.FlattenMD(md)
	for k := range out {
		if reserved(k) {
			delete(out, k)
		}
	}

	return out
}

// reserved keys belong to the transports (HTTP/2, gRPC, ws-proto).
func reserved(k string) bool {
	switch {
	case strings.HasPrefix(k, ":"), strings.HasPrefix(k, "grpc-"), strings.HasPrefix(k, "ws-"):
		return true
	}

	switch k {
	case "content-type", "te", "host", "connection", "user-agent":
		return true
	}

	return false
}

// rawCodec moves messages as bytes: the relay never decodes them. Its
// empty name keeps the content type plain "application/grpc" (no
// "+<codec>" subtype): the frames are protobuf, and the SDK's platform
// port routes gRPC by exactly that content type.
type rawCodec struct{}

func (rawCodec) Marshal(v any) (mem.BufferSlice, error) {
	b, ok := v.(*[]byte)
	if !ok {
		return nil, errRawCodec
	}

	return mem.BufferSlice{mem.SliceBuffer(*b)}, nil
}

func (rawCodec) Unmarshal(data mem.BufferSlice, v any) error {
	b, ok := v.(*[]byte)
	if !ok {
		return errRawCodec
	}

	*b = data.Materialize()

	return nil
}

func (rawCodec) Name() string { return "" }
