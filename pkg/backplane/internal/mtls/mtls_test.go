package mtls_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
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
)

// ca issues SPIFFE service certificates, as Consul's CA does.
type ca struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	cert   *x509.Certificate
	serial atomic.Int64
}

func newCA(t *testing.T) *ca {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, IsCA: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, _ := x509.ParseCertificate(der)
	c := &ca{t: t, key: key, cert: cert}
	c.serial.Store(1)

	return c
}

func (c *ca) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)

	return p
}

func (c *ca) issue(service string) *tls.Certificate {
	c.t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		c.t.Fatal(err)
	}

	uri, _ := url.Parse("spiffe://11111111-2222.consul/ns/default/dc/dc1/svc/" + service)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(c.serial.Add(1)), URIs: []*url.URL{uri},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		c.t.Fatal(err)
	}

	leaf, _ := x509.ParseCertificate(der)

	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// source is a mutable identity: a certificate, roots and denied callers.
type source struct {
	mu     sync.Mutex
	cert   *tls.Certificate
	roots  *x509.CertPool
	denied map[string]bool
}

func (s *source) Certificate() *tls.Certificate { s.mu.Lock(); defer s.mu.Unlock(); return s.cert }
func (s *source) Roots() *x509.CertPool         { s.mu.Lock(); defer s.mu.Unlock(); return s.roots }

func (s *source) Authorize(caller string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.denied[caller] {
		return false, "denied by the test"
	}

	return true, "allowed"
}

func (s *source) set(cert *tls.Certificate) { s.mu.Lock(); s.cert = cert; s.mu.Unlock() }

// server is service "a" behind an mTLS listener: gRPC health and an HTTP
// route echoing its caller.
func server(t *testing.T, src *source) string {
	t.Helper()

	g := grpc.NewServer(mtls.GRPCServerOptions(src)...)
	hv1.RegisterHealthServer(g, health.NewServer())

	h := mtls.Middleware(src)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := mtls.PeerFrom(r.Context())
		_, _ = io.WriteString(w, p.Service)
	}))

	l := listener.New("a", "127.0.0.1:0", g, h, testlog.Discard(), listener.Limits{},
		listener.TLS(mtls.ServerConfig(src)), listener.ConnContext(mtls.ConnContext))
	n := node.New("a", testlog.Discard(), nil)
	n.OnStart(func(ctx context.Context) error { return l.Start(ctx, n) })
	n.OnStop(l.Stop)

	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = n.Stop(context.Background()) })

	return l.Addr()
}

func callGRPC(t *testing.T, addr string, creds credentials.TransportCredentials) error {
	t.Helper()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err = hv1.NewHealthClient(conn).Check(ctx, &hv1.HealthCheckRequest{})

	return err
}

func callHTTP(t *testing.T, addr string, cfg *tls.Config) (int, string, error) {
	t.Helper()

	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}, Timeout: 5 * time.Second}

	res, err := c.Get("https://" + addr + "/")
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	return res.StatusCode, strings.TrimSpace(string(body)), nil
}

func TestMutualTLS(t *testing.T) {
	t.Parallel()

	authority := newCA(t)
	a := &source{cert: authority.issue("a"), roots: authority.pool(), denied: map[string]bool{}}
	b := &source{cert: authority.issue("b"), roots: authority.pool()}
	addr := server(t, a)

	// b calls a, over gRPC and HTTP; a sees b.
	if err := callGRPC(t, addr, credentials.NewTLS(mtls.ClientConfig(b, "a", "h2"))); err != nil {
		t.Fatalf("grpc b -> a: %v", err)
	}

	if code, body, err := callHTTP(t, addr, mtls.ClientConfig(b, "a", "http/1.1")); err != nil || code != http.StatusOK || body != "b" {
		t.Fatalf("http b -> a: %d %q %v", code, body, err)
	}

	// a refuses b: PermissionDenied, 403.
	a.mu.Lock()
	a.denied["b"] = true
	a.mu.Unlock()

	if err := callGRPC(t, addr, credentials.NewTLS(mtls.ClientConfig(b, "a", "h2"))); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("denied grpc: %v", err)
	}

	if code, _, err := callHTTP(t, addr, mtls.ClientConfig(b, "a", "http/1.1")); err != nil || code != http.StatusForbidden {
		t.Fatalf("denied http: %d %v", code, err)
	}

	a.mu.Lock()
	a.denied["b"] = false
	a.mu.Unlock()

	// b expecting another service refuses a.
	if err := callGRPC(t, addr, credentials.NewTLS(mtls.ClientConfig(b, "c", "h2"))); err == nil ||
		!strings.Contains(err.Error(), "the server is") {
		t.Fatalf("wrong server accepted: %v", err)
	}

	// A certificate of another CA does not get in, nor does no certificate.
	stranger := newCA(t)
	mallory := &source{cert: stranger.issue("b"), roots: authority.pool()}

	if _, _, err := callHTTP(t, addr, mtls.ClientConfig(mallory, "a", "http/1.1")); err == nil {
		t.Fatal("a certificate of another CA got in")
	}

	if err := callGRPC(t, addr, credentials.NewTLS(mtls.ClientConfig(&source{roots: authority.pool()}, "a", "h2"))); err == nil {
		t.Fatal("a client without a certificate got in")
	}

	// A server of another CA is refused by the client.
	if err := callGRPC(t, server(t, &source{cert: stranger.issue("a"), roots: stranger.pool()}),
		credentials.NewTLS(mtls.ClientConfig(b, "a", "h2"))); err == nil {
		t.Fatal("a server of another CA was trusted")
	}

	// A renewed certificate applies to the next connection.
	renewed := authority.issue("a")
	a.set(renewed)

	conn, err := tls.Dial("tcp", addr, mtls.ClientConfig(b, "a", "http/1.1"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if got := conn.ConnectionState().PeerCertificates[0].SerialNumber; got.Cmp(renewed.Leaf.SerialNumber) != 0 {
		t.Fatalf("server presented serial %v, want the renewed %v", got, renewed.Leaf.SerialNumber)
	}
}

func TestServiceOf(t *testing.T) {
	t.Parallel()

	c := newCA(t).issue("sync-admin")

	p, ok := mtls.ServiceOf(c.Leaf)
	if !ok || p.Service != "sync-admin" || !strings.HasPrefix(p.URI, "spiffe://") {
		t.Fatalf("%+v %v", p, ok)
	}

	if _, named := mtls.ServiceOf(&x509.Certificate{}); named {
		t.Fatal("a certificate without a URI named a service")
	}
}
