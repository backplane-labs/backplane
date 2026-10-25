// Package meshdial dials the gRPC channels of the mesh: a service's
// api.MTLS port, a platform port. It is what Root.DialService and
// Root.DialPlatform build on, and what backplane and the SDK dial with.
//
// The channel resolves its host itself rather than through gRPC's DNS
// resolver, whose re-resolution waits 30 s after the last lookup (a
// process-wide setting): when a peer restarts and its address moves (a
// container's IP reassigned, possibly to another service, whose identity
// the handshake refuses), the channel looks the name up again within
// MinResolveInterval, retries a failing lookup within RetryMax and
// reconnects from ConnectBase up to ConnectMax — seconds, not minutes. Nothing
// process-wide is changed.
//
// Calls are not retried: a call made while the channel is reconnecting
// fails with Unavailable unless it waits for readiness, bounded by its
// deadline:
//
//	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
//	defer cancel()
//	_, err := client.Get(ctx, req, grpc.WaitForReady(true))
package meshdial

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/resolver"
)

// Timings of a channel.
const (
	// MinResolveInterval is the least time between two lookups of a host:
	// how soon a channel whose peer refused it looks again.
	MinResolveInterval = 500 * time.Millisecond
	// RetryMax bounds the wait before a failed lookup is retried (it starts
	// at MinResolveInterval and grows).
	RetryMax = 5 * time.Second
	// lookupTimeout bounds one lookup.
	lookupTimeout = 5 * time.Second
)

// How a channel reconnects: from ConnectBase up to ConnectMax, each
// attempt bounded by ConnectTimeout.
const (
	ConnectBase    = 250 * time.Millisecond
	ConnectMax     = 5 * time.Second
	ConnectTimeout = 5 * time.Second
	connectGrowth  = 1.6
	connectJitter  = 0.2
)

func connectParams() grpc.ConnectParams {
	return grpc.ConnectParams{
		Backoff: backoff.Config{
			BaseDelay: ConnectBase, Multiplier: connectGrowth, Jitter: connectJitter, MaxDelay: ConnectMax,
		},
		MinConnectTimeout: ConnectTimeout,
	}
}

// Lookup resolves a host name to its IP addresses.
type Lookup func(ctx context.Context, host string) ([]string, error)

// Dial is a channel to addr — host:port, the host a name or an IP — with
// creds and opts (applied last). An addr with a scheme (dns:///, unix:)
// goes to gRPC's own resolver unchanged, with the same reconnection.
// Nothing connects until the first call or Connect.
func Dial(addr string, creds credentials.TransportCredentials, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return Dialer{}.Dial(addr, creds, opts...)
}

// Dialer is Dial with its host names resolved by Lookup (nil: the
// system's resolver): tests, a custom discovery.
type Dialer struct{ Lookup Lookup }

// Dial is Dial of the dialer.
func (d Dialer) Dial(addr string, creds credentials.TransportCredentials, opts ...grpc.DialOption) (
	*grpc.ClientConn, error,
) {
	lookup := d.Lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}

	base := []grpc.DialOption{grpc.WithTransportCredentials(creds), grpc.WithConnectParams(connectParams())}

	target := addr
	if !strings.Contains(addr, ":///") && !strings.HasPrefix(addr, "unix:") {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, fmt.Errorf("meshdial: %q is not host:port: %w", addr, err)
		}

		target = scheme + ":///" + addr

		base = append(base, grpc.WithResolvers(builder{lookup: lookup}))
	}

	conn, err := grpc.NewClient(target, append(base, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("meshdial: %s: %w", addr, err)
	}

	return conn, nil
}

const scheme = "backplane-mesh"

// builder builds the resolver of one channel (grpc.WithResolvers): nothing
// is registered process-wide.
type builder struct{ lookup Lookup }

func (builder) Scheme() string { return scheme }

//nolint:ireturn // gRPC's resolver
func (b builder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (
	resolver.Resolver, error,
) {
	host, port, err := net.SplitHostPort(target.Endpoint())
	if err != nil {
		return nil, fmt.Errorf("meshdial: %w", err)
	}

	// An IP needs no lookup.
	if net.ParseIP(host) != nil {
		if err := cc.UpdateState(resolver.State{Addresses: []resolver.Address{{Addr: target.Endpoint()}}}); err != nil {
			return nil, fmt.Errorf("meshdial: %w", err)
		}

		return static{}, nil
	}

	// Canceled by Close.
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // kept in the resolver
	r := &dnsResolver{
		host: host, port: port, cc: cc, lookup: b.lookup,
		now: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}),
	}

	go r.run(ctx)

	return r, nil
}

type static struct{}

func (static) ResolveNow(resolver.ResolveNowOptions) {}
func (static) Close()                                {}

// dnsResolver looks its host up at start and whenever the channel asks
// (a connection lost or refused), at most every MinResolveInterval; a
// failing lookup is retried with a growing wait.
type dnsResolver struct {
	host, port string
	cc         resolver.ClientConn
	lookup     Lookup

	now    chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

var errNoAddress = errors.New("meshdial: no address")

func (r *dnsResolver) ResolveNow(resolver.ResolveNowOptions) {
	select {
	case r.now <- struct{}{}:
	default:
	}
}

func (r *dnsResolver) Close() {
	r.once.Do(func() {
		r.cancel()
		<-r.done
	})
}

func (r *dnsResolver) run(ctx context.Context) {
	defer close(r.done)

	retry := MinResolveInterval

	for {
		last := time.Now()

		var wake <-chan time.Time // nil: until the channel asks

		if err := r.resolve(ctx); err != nil {
			r.cc.ReportError(err)

			wake = time.After(retry)
			retry = min(retry*2, RetryMax) //nolint:mnd // doubling
		} else {
			retry = MinResolveInterval
		}

		select {
		case <-ctx.Done():
			return
		case <-r.now:
		case <-wake:
		}

		// Throttled: at most one lookup per MinResolveInterval.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(last.Add(MinResolveInterval))):
		}
	}
}

func (r *dnsResolver) resolve(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	ips, err := r.lookup(ctx, r.host)
	if err != nil {
		return fmt.Errorf("meshdial: lookup %s: %w", r.host, err)
	}

	if len(ips) == 0 {
		return fmt.Errorf("%w: %s", errNoAddress, r.host)
	}

	addrs := make([]resolver.Address, 0, len(ips))
	for _, ip := range ips {
		addrs = append(addrs, resolver.Address{Addr: net.JoinHostPort(ip, r.port)})
	}

	// An error here is the balancer's: it asks again by itself.
	_ = r.cc.UpdateState(resolver.State{Addresses: addrs})

	return nil
}
