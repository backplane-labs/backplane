package consul

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/mtls"
)

// Mesh is a service's identity from Consul's CA and intentions, followed by
// blocking queries as configuration is: the service's leaf certificate
// (renewed by the agent before it expires and when the CA's root rotates),
// the CA's roots, and the intentions naming the service as destination.
// Who may call is decided here from the intentions, as Consul orders them
// (exact source before a wildcard): the agent's own authorize endpoint
// caches its answer.
type Mesh struct {
	client   *api.Client
	service  string
	allow    bool // no intention names the caller
	log      *xlog.Logger
	retryMin time.Duration

	leaf  atomic.Pointer[tls.Certificate]
	roots atomic.Pointer[x509.CertPool]
	rules atomic.Pointer[[]rule]

	mu      sync.Mutex
	failing map[string]error // per watch: the error last logged
}

// rule is one intention: a source (a service or "*", in a namespace and
// partition, "" or "*" for any) and its action.
type rule struct {
	source, namespace, partition string
	allow                        bool
}

// names reports whether r names a caller of this service's namespace and
// partition (the only callers the handshake lets in).
func (r rule) names(caller string, own mtls.Peer) bool {
	matches := func(v, own string) bool { return v == "" || v == "*" || v == own }

	return (r.source == caller || r.source == "*") && matches(r.namespace, own.Namespace) &&
		matches(r.partition, own.Partition)
}

// MeshParams configure NewMesh.
type MeshParams struct {
	Client  *api.Client
	Service string
	// DefaultAllow: a caller no intention names is allowed.
	DefaultAllow bool
	Log          *xlog.Logger
}

// NewMesh creates the identity; Run follows it.
func NewMesh(p MeshParams) *Mesh {
	return &Mesh{
		client: p.Client, service: p.Service, allow: p.DefaultAllow, log: p.Log, retryMin: time.Second,
		failing: map[string]error{},
	}
}

// Certificate is the service's current leaf certificate.
func (m *Mesh) Certificate() *tls.Certificate { return m.leaf.Load() }

// Roots are the CA's current roots.
func (m *Mesh) Roots() *x509.CertPool { return m.roots.Load() }

// ErrRoot: a root of the CA does not parse.
var ErrRoot = errors.New("consul mesh: root does not parse")

// ErrNotReady: the leaf certificate or the roots are not fetched yet.
var ErrNotReady = errors.New("consul mesh: no certificate or roots yet")

// Ready is nil once the certificate, the roots and the intentions are known.
func (m *Mesh) Ready() error {
	if m.leaf.Load() == nil || m.roots.Load() == nil || m.rules.Load() == nil {
		return ErrNotReady
	}

	return nil
}

// Authorize decides from the intentions: the first naming caller or "*";
// none: the default.
func (m *Mesh) Authorize(caller string) (bool, string) {
	rules, cert := m.rules.Load(), m.leaf.Load()
	if rules == nil || cert == nil || cert.Leaf == nil {
		return false, "intentions or certificate not fetched yet"
	}

	own, ok := mtls.ServiceOf(cert.Leaf)
	if !ok {
		return false, "this service's certificate names no service"
	}

	for _, r := range *rules {
		if r.names(caller, own) {
			verdict := "deny"
			if r.allow {
				verdict = "allow"
			}

			return r.allow, fmt.Sprintf("intention %s => %s: %s", r.source, m.service, verdict)
		}
	}

	if m.allow {
		return true, "no intention: default allow"
	}

	return false, "no intention: default deny"
}

// Run follows the leaf, the roots and the intentions until ctx ends.
func (m *Mesh) Run(ctx context.Context) error {
	var watches sync.WaitGroup

	for name, fetch := range map[string]func(*api.QueryOptions) (uint64, error){
		"leaf": m.fetchLeaf, "roots": m.fetchRoots, "intentions": m.fetchIntentions,
	} {
		watches.Go(func() { m.follow(ctx, name, fetch) })
	}

	watches.Wait()

	return nil
}

// follow runs fetch as a blocking query from the last index; failures
// retry with backoff and are logged once each.
func (m *Mesh) follow(ctx context.Context, name string, fetch func(*api.QueryOptions) (uint64, error)) {
	const (
		maxDelay = 30 * time.Second
		wait     = 5 * time.Minute
		growth   = 2
	)

	var index uint64

	delay := m.retryMin

	for ctx.Err() == nil {
		next, err := fetch((&api.QueryOptions{WaitIndex: index, WaitTime: wait}).WithContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			m.report(name, err)

			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}

			delay = min(growth*delay, maxDelay)

			continue
		}

		m.report(name, nil)

		delay = m.retryMin

		// An index going back (a restarted agent) restarts from zero.
		if next < index {
			next = 0
		}

		index = next
	}
}

func (m *Mesh) report(name string, err error) {
	m.mu.Lock()
	last := m.failing[name]

	if err == nil {
		delete(m.failing, name)
	} else {
		m.failing[name] = err
	}
	m.mu.Unlock()

	switch {
	case err != nil && (last == nil || last.Error() != err.Error()):
		m.log.Warn("consul mesh: "+name+" unavailable, retrying", xlog.Err(err))
	case err == nil && last != nil:
		m.log.Info("consul mesh: " + name + " recovered")
	}
}

func (m *Mesh) fetchLeaf(q *api.QueryOptions) (uint64, error) {
	leaf, meta, err := m.client.Agent().ConnectCALeaf(m.service, q)
	if err != nil {
		return 0, fmt.Errorf("leaf certificate: %w", err)
	}

	cert, err := tls.X509KeyPair([]byte(leaf.CertPEM), []byte(leaf.PrivateKeyPEM))
	if err != nil {
		return 0, fmt.Errorf("leaf certificate: %w", err)
	}

	if prev := m.leaf.Swap(&cert); prev != nil && prev.Leaf != nil && cert.Leaf != nil &&
		prev.Leaf.SerialNumber.Cmp(cert.Leaf.SerialNumber) != 0 {
		m.log.Info("consul mesh: certificate renewed", xlog.String("serial", leaf.SerialNumber),
			xlog.String("valid_before", leaf.ValidBefore.Format(time.RFC3339)))
	}

	return meta.LastIndex, nil
}

func (m *Mesh) fetchRoots(q *api.QueryOptions) (uint64, error) {
	list, meta, err := m.client.Agent().ConnectCARoots(q)
	if err != nil {
		return 0, fmt.Errorf("roots: %w", err)
	}

	pool := x509.NewCertPool()
	for _, r := range list.Roots {
		if !pool.AppendCertsFromPEM([]byte(r.RootCertPEM)) {
			return 0, fmt.Errorf("%w: %s", ErrRoot, r.ID)
		}
	}

	m.roots.Store(pool)

	return meta.LastIndex, nil
}

func (m *Mesh) fetchIntentions(q *api.QueryOptions) (uint64, error) {
	matched, meta, err := m.client.Connect().IntentionMatch(
		&api.IntentionMatch{By: api.IntentionMatchDestination, Names: []string{m.service}}, q)
	if err != nil {
		return 0, fmt.Errorf("intentions: %w", err)
	}

	rules := make([]rule, 0, len(matched[m.service]))
	for _, in := range matched[m.service] {
		// A peer's or another partition's source is no caller of this mesh;
		// an intention with L7 permissions only is a deny for L4, as Consul
		// treats it.
		if in.SourcePeer != "" {
			continue
		}

		rules = append(rules, rule{
			source: in.SourceName, namespace: in.SourceNS, partition: in.SourcePartition,
			allow: in.Action == api.IntentionActionAllow,
		})
	}

	m.rules.Store(&rules)

	return meta.LastIndex, nil
}
