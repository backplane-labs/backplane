package consul_test

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/consul"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/mtls"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

// The mesh follows Consul: its certificate names the service, intentions
// apply as they change, and a rotated CA root brings a new certificate and
// roots without a restart.
//
//nolint:paralleltest // rotates the CA of the shared Consul
func TestMeshFollowsConsul(t *testing.T) {
	c := client(t)

	const service = "mesh-follow"

	t.Cleanup(func() { _, _ = c.ConfigEntries().Delete(api.ServiceIntentions, service, nil) })

	m := consul.NewMesh(consul.MeshParams{Client: c, Service: service, DefaultAllow: false, Log: testlog.Discard()})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- m.Run(ctx) }()

	t.Cleanup(func() { cancel(); <-done })

	eventually(t, "ready", func() bool { return m.Ready() == nil })

	peer, named := mtls.ServiceOf(m.Certificate().Leaf)
	if !named || peer.Service != service {
		t.Fatalf("certificate speaks for %+v", peer)
	}

	if _, err := m.Certificate().Leaf.Verify(verifyOptions(m)); err != nil {
		t.Fatalf("the certificate does not lead to the roots: %v", err)
	}

	// The default denies; intentions decide, exact source before a wildcard.
	if ok, _ := m.Authorize("caller"); ok {
		t.Fatal("default deny allowed")
	}

	if _, _, err := c.ConfigEntries().Set(&api.ServiceIntentionsConfigEntry{
		Kind: api.ServiceIntentions, Name: service,
		Sources: []*api.SourceIntention{
			{Name: "blocked", Action: api.IntentionActionDeny},
			{Name: "*", Action: api.IntentionActionAllow},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}

	eventually(t, "intentions", func() bool { ok, _ := m.Authorize("caller"); return ok })

	if ok, reason := m.Authorize("blocked"); ok {
		t.Fatalf("an exact deny lost to the wildcard: %s", reason)
	}

	// Rotate the CA's root: a new certificate and roots arrive by themselves.
	before := m.Certificate().Leaf.SerialNumber

	rotate(t, c)

	// Consul spreads the re-signing after a rotation over seconds.
	for deadline := time.Now().Add(time.Minute); m.Certificate().Leaf.SerialNumber.Cmp(before) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("no new certificate a minute after the rotation")
		}

		time.Sleep(50 * time.Millisecond)
	}

	if _, err := m.Certificate().Leaf.Verify(verifyOptions(m)); err != nil {
		t.Fatalf("the new certificate does not lead to the roots: %v", err)
	}
}

// rotate changes the CA's key so Consul makes a new root.
func rotate(t *testing.T, c *api.Client) {
	t.Helper()

	cfg, _, err := c.Connect().CAGetConfig(nil)
	if err != nil {
		t.Fatal(err)
	}

	bits := 384
	if v, ok := cfg.Config["PrivateKeyBits"].(float64); ok && v == 384 {
		bits = 256
	}

	cfg.Config["PrivateKeyType"], cfg.Config["PrivateKeyBits"] = "ec", bits

	if _, err := c.Connect().CASetConfig(&api.CAConfig{Provider: cfg.Provider, Config: cfg.Config}, nil); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)
}

func verifyOptions(m *consul.Mesh) x509.VerifyOptions {
	inter := x509.NewCertPool()

	for _, raw := range m.Certificate().Certificate[1:] {
		if c, err := x509.ParseCertificate(raw); err == nil {
			inter.AddCert(c)
		}
	}

	return x509.VerifyOptions{Roots: m.Roots(), Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}
}
