package meshdial_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/listener"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/mtls"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
	"github.com/backplane-labs/backplane/pkg/backplane/meshdial"
)

// identity is a service of a test mesh.
type identity struct {
	cert  *tls.Certificate
	roots *x509.CertPool
}

func (i identity) Certificate() *tls.Certificate { return i.cert }
func (i identity) Roots() *x509.CertPool         { return i.roots }
func (identity) Authorize(string) (bool, string) { return true, "allowed" }

// mesh issues service certificates from one CA.
func mesh(t *testing.T) func(service string) identity {
	t.Helper()

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, IsCA: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	authority, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(authority)

	var serial atomic.Int64

	serial.Store(1)

	return func(service string) identity {
		leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		uri, _ := url.Parse("spiffe://11111111-2222.consul/ns/default/dc/dc1/svc/" + service)
		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(serial.Add(1)), URIs: []*url.URL{uri},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage:    x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		}

		leafDER, err := x509.CreateCertificate(rand.Reader, leaf, authority, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}

		parsed, _ := x509.ParseCertificate(leafDER)

		return identity{
			cert: &tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey, Leaf: parsed}, roots: roots,
		}
	}
}

// serve runs service id's gRPC health behind an mTLS listener at addr; calls
// counts the calls it served. The returned func stops it.
func serve(t *testing.T, id identity, addr string, calls *atomic.Int64) func() {
	t.Helper()

	opts := append(mtls.GRPCServerOptions(id), grpc.ChainUnaryInterceptor(func(
		ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler,
	) (any, error) {
		calls.Add(1)

		return h(ctx, req)
	}))
	g := grpc.NewServer(opts...)
	hv1.RegisterHealthServer(g, health.NewServer())

	l := listener.New("svc", addr, g, nil, testlog.Discard(), listener.Limits{},
		listener.TLS(mtls.ServerConfig(id)), listener.ConnContext(mtls.ConnContext))
	n := node.New("svc", testlog.Discard(), nil)
	n.OnStart(func(ctx context.Context) error { return l.Start(ctx, n) })
	n.OnStop(l.Stop)

	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	var once sync.Once

	stop := func() { once.Do(func() { _ = n.Stop(context.Background()) }) }
	t.Cleanup(stop)

	return stop
}

// dns answers one name with the IP of the moment.
type dns struct{ ip atomic.Value }

func (d *dns) set(ip string) { d.ip.Store(ip) }

func (d *dns) lookup(_ context.Context, host string) ([]string, error) {
	if host != "platform" {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	ip, _ := d.ip.Load().(string)

	return []string{ip}, nil
}

func freePort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer l.Close()

	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func check(ctx context.Context, conn *grpc.ClientConn, wait time.Duration, opts ...grpc.CallOption) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	_, err := hv1.NewHealthClient(conn).Check(ctx, &hv1.HealthCheckRequest{}, opts...)

	return err
}

// A peer restarts on another IP and its old one goes to another service:
// the channel never takes the impostor, looks the name up again within
// seconds and recovers; a call waiting for readiness succeeds within its
// deadline.
func TestAddressChurn(t *testing.T) {
	t.Parallel()

	issue := mesh(t)
	platform, syncID, gateway := issue("platform"), issue("sync"), issue("gateway")
	port := freePort(t)
	a, b := net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("127.0.0.2", port)

	var platformCalls, syncCalls atomic.Int64

	d := &dns{}
	d.set("127.0.0.1")

	stopA := serve(t, platform, a, &platformCalls)

	conn, err := meshdial.Dialer{Lookup: d.lookup}.Dial(net.JoinHostPort("platform", port),
		credentials.NewTLS(mtls.ClientConfig(gateway, "platform", "h2")))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := check(t.Context(), conn, 5*time.Second); err != nil {
		t.Fatalf("platform at A: %v", err)
	}

	// platform goes; sync takes its address; the name still points there.
	stopA()
	serve(t, syncID, a, &syncCalls)

	if err := check(t.Context(), conn, 5*time.Second); status.Code(err) != codes.Unavailable {
		t.Fatalf("sync at A, fail-fast call: %v", err)
	}

	// platform comes back at B.
	serve(t, platform, b, &platformCalls)
	d.set("127.0.0.2")

	start := time.Now()

	if err := check(t.Context(), conn, 10*time.Second, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("platform at B: %v", err)
	}

	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("recovered in %v, budget 3s", took)
	}

	if syncCalls.Load() != 0 || platformCalls.Load() != 2 {
		t.Fatalf("calls: sync %d, platform %d", syncCalls.Load(), platformCalls.Load())
	}

	// The name keeps pointing at sync: a call waiting for readiness ends at
	// its deadline, sync never accepted.
	d.set("127.0.0.1")

	wrong, err := meshdial.Dialer{Lookup: d.lookup}.Dial(net.JoinHostPort("platform", port),
		credentials.NewTLS(mtls.ClientConfig(gateway, "platform", "h2")))
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()

	start = time.Now()

	if err := check(t.Context(), wrong, time.Second, grpc.WaitForReady(true)); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("permanently wrong identity: %v", err)
	}

	if took := time.Since(start); took > 1500*time.Millisecond || syncCalls.Load() != 0 {
		t.Fatalf("wrong identity: ended after %v, sync calls %d", took, syncCalls.Load())
	}
}

// A name that does not resolve yet (the peer not started) is looked up
// again until it does.
func TestLookupRetry(t *testing.T) {
	t.Parallel()

	issue := mesh(t)
	platform, gateway := issue("platform"), issue("gateway")
	port := freePort(t)

	var calls atomic.Int64

	var missing atomic.Bool

	missing.Store(true)

	lookup := func(_ context.Context, host string) ([]string, error) {
		if missing.Load() {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}

		return []string{"127.0.0.1"}, nil
	}

	conn, err := meshdial.Dialer{Lookup: lookup}.Dial(net.JoinHostPort("platform", port),
		credentials.NewTLS(mtls.ClientConfig(gateway, "platform", "h2")))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := check(t.Context(), conn, time.Second); status.Code(err) != codes.Unavailable {
		t.Fatalf("not resolving: %v", err)
	}

	serve(t, platform, net.JoinHostPort("127.0.0.1", port), &calls)
	missing.Store(false)

	if err := check(t.Context(), conn, 10*time.Second, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("after it resolves: %v", err)
	}
}

func TestDialRejectsBadAddress(t *testing.T) {
	t.Parallel()

	if _, err := meshdial.Dial("platform", credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})); err == nil {
		t.Fatal("dialed an address without a port")
	}

	if _, err := meshdial.Dial("dns:///platform:1", credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})); err != nil {
		t.Fatalf("dns scheme: %v", err)
	}
}
