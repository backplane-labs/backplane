// Package mtls is mutual TLS between services: a Source (the service's live
// certificate, the trusted roots and who may call it — the Consul driver's
// follows Consul's CA and intentions) turned into the TLS of a public port
// and of clients, and the caller of a request: its service, taken from the
// SPIFFE URI of the certificate it presented.
//
// Certificates are read on every handshake, so a renewed certificate or a
// rotated root applies to the next connection without restarting anything;
// connections already open keep the certificates they were made with.
package mtls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/soheilhy/cmux"
)

// Source is a service's identity.
type Source interface {
	// Certificate is the service's current certificate; nil until there is one.
	Certificate() *tls.Certificate
	// Roots are the CA certificates peers' chains must lead to; nil until known.
	Roots() *x509.CertPool
	// Authorize decides whether caller (a service name) may call this
	// service, and why.
	Authorize(caller string) (bool, string)
}

// Errors.
var (
	// ErrNoIdentity: the source has no certificate or roots yet.
	ErrNoIdentity = errors.New("mtls: no certificate or roots yet")
	// ErrPeer: the peer's certificate is not a service's of this mesh.
	ErrPeer = errors.New("mtls: peer")
)

// Peer is the service on the other end.
type Peer struct {
	// Service is the peer's service name.
	Service string
	// URI is its SPIFFE identity (spiffe://<trust domain>/ns/<ns>/dc/<dc>/svc/<service>).
	URI string
}

// ServiceOf is the service a certificate speaks for: the last path element
// after /svc/ of its spiffe URI.
func ServiceOf(cert *x509.Certificate) (Peer, bool) {
	for _, uri := range cert.URIs {
		if uri.Scheme != "spiffe" {
			continue
		}

		if i := strings.LastIndex(uri.Path, "/svc/"); i >= 0 && len(uri.Path) > i+len("/svc/") {
			return Peer{Service: uri.Path[i+len("/svc/"):], URI: uri.String()}, true
		}
	}

	return Peer{}, false
}

// verify checks a presented chain against the source's current roots and
// names its service. It runs from VerifyConnection, so on every handshake,
// resumed sessions included.
func verify(src Source, certs []*x509.Certificate) (Peer, error) {
	roots := src.Roots()
	if roots == nil {
		return Peer{}, ErrNoIdentity
	}

	if len(certs) == 0 {
		return Peer{}, fmt.Errorf("%w: no certificate presented", ErrPeer)
	}

	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}

	if _, err := certs[0].Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return Peer{}, fmt.Errorf("%w: %w", ErrPeer, err)
	}

	p, ok := ServiceOf(certs[0])
	if !ok {
		return Peer{}, fmt.Errorf("%w: no service identity in the certificate", ErrPeer)
	}

	return p, nil
}

// Failing is a source that cannot have an identity, and says why.
type Failing interface{ Err() error }

func certificate(src Source) (*tls.Certificate, error) {
	if f, ok := src.(Failing); ok && f.Err() != nil {
		return nil, fmt.Errorf("mtls: %w", f.Err())
	}

	if c := src.Certificate(); c != nil {
		return c, nil
	}

	return nil, ErrNoIdentity
}

// ServerConfig is the TLS of a port only services of the mesh reach: the
// source's certificate, a client certificate required and verified against
// the current roots. It offers h2 (gRPC) and http/1.1.
func ServerConfig(src Source) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return certificate(src) },
		ClientAuth:     tls.RequireAnyClientCert,
		VerifyConnection: func(cs tls.ConnectionState) error {
			_, err := verify(src, cs.PeerCertificates)

			return err
		},
	}
}

// ClientConfig is the TLS of a call to target: the source's certificate, the
// peer verified against the current roots and as target. nextProtos: h2
// for gRPC, http/1.1 for HTTP.
func ClientConfig(src Source, target string, nextProtos ...string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: nextProtos,
		// Verified below, against the roots of the moment and the target's
		// identity rather than a host name, on every handshake.
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection verifies
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return certificate(src)
		},
		VerifyConnection: func(cs tls.ConnectionState) error {
			p, err := verify(src, cs.PeerCertificates)
			if err == nil && p.Service != target {
				err = fmt.Errorf("%w: the server is %q, not %q", ErrPeer, p.Service, target)
			}

			return err
		},
	}
}

// PeerOf is the verified peer of a server connection: a *tls.Conn, possibly
// inside cmux's connection.
func PeerOf(conn net.Conn) (Peer, bool) {
	if mc, ok := conn.(*cmux.MuxConn); ok {
		conn = mc.Conn
	}

	tc, ok := conn.(*tls.Conn)
	if !ok {
		return Peer{}, false
	}

	state := tc.ConnectionState()
	if !state.HandshakeComplete || len(state.PeerCertificates) == 0 {
		return Peer{}, false
	}

	return ServiceOf(state.PeerCertificates[0])
}

type peerKey struct{}

// WithPeer is ctx carrying p.
func WithPeer(ctx context.Context, p Peer) context.Context {
	return context.WithValue(ctx, peerKey{}, p)
}

// PeerFrom is the peer ctx carries.
func PeerFrom(ctx context.Context) (Peer, bool) {
	p, ok := ctx.Value(peerKey{}).(Peer)

	return p, ok
}
