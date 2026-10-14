package consul_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/consul"
)

// selfSigned returns a certificate and its key as PEM.
func selfSigned(t *testing.T) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "consul"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}))
}

func TestConsulEnvFallbacks(t *testing.T) {
	t.Setenv("CONSUL_HTTP_TOKEN", "env-token")
	t.Setenv("CONSUL_CACERT", "/etc/consul/ca.pem")
	t.Setenv("CONSUL_HTTP_SSL", "true")

	cfg, err := consul.ConsulAPIConfig(config.Consul{Addr: "consul:8501"})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Address != "consul:8501" || cfg.Token != "env-token" || cfg.Scheme != "https" ||
		cfg.TLSConfig.CAFile != "/etc/consul/ca.pem" {
		t.Fatalf("env fallbacks: %+v", cfg)
	}

	cfg, err = consul.ConsulAPIConfig(config.Consul{Addr: "consul:8501", Token: "block", Datacenter: "eu1"})
	if err != nil || cfg.Token != "block" || cfg.Datacenter != "eu1" {
		t.Fatalf("block must win: token %q dc %q %v", cfg.Token, cfg.Datacenter, err)
	}
}

func TestConsulTLSFromPEM(t *testing.T) {
	t.Setenv("CONSUL_CACERT", "/nonexistent/ca.pem") // the block's TLS replaces the environment's
	t.Setenv("CONSUL_HTTP_SSL", "")

	cert, key := selfSigned(t)
	block := config.Consul{Addr: "consul:8501", TLS: config.TLS{
		Enabled: true, CA: cert, Cert: cert, Key: config.Secret(key), ServerName: "consul.internal",
	}}

	cfg, err := consul.ConsulAPIConfig(block)
	if err != nil || cfg.Scheme != "https" || cfg.TLSConfig.CAFile != "" {
		t.Fatalf("tls mapping: %+v %v", cfg.TLSConfig, err)
	}

	tlsCfg, err := consul.ConsulClientTLS(block)
	if err != nil {
		t.Fatal(err)
	}

	if tlsCfg.ServerName != "consul.internal" || tlsCfg.RootCAs == nil || len(tlsCfg.Certificates) != 1 ||
		tlsCfg.InsecureSkipVerify {
		t.Fatalf("transport tls: server %q roots %v certs %d", tlsCfg.ServerName, tlsCfg.RootCAs != nil,
			len(tlsCfg.Certificates))
	}

	// TLS off: nothing of the block's TLS applies.
	off, err := consul.ConsulAPIConfig(config.Consul{Addr: "consul:8500", TLS: config.TLS{CA: cert}})
	if err != nil || off.Scheme != "http" || off.Transport.TLSClientConfig != nil {
		t.Fatalf("tls disabled: %+v %v", off.TLSConfig, err)
	}
}

func TestTLSClientConfig(t *testing.T) {
	t.Parallel()

	cert, key := selfSigned(t)

	if cfg, err := (config.TLS{CA: cert}).ClientConfig(); cfg != nil || err != nil {
		t.Fatalf("disabled: %v %v", cfg, err)
	}

	if cfg, err := (config.TLS{Enabled: true}).ClientConfig(); err != nil || cfg.RootCAs != nil {
		t.Fatalf("system pool: %v %v", cfg, err)
	}

	for name, c := range map[string]struct {
		tls  config.TLS
		want error
	}{
		"bad ca":     {config.TLS{Enabled: true, CA: "not a pem"}, config.ErrTLSCA},
		"cert alone": {config.TLS{Enabled: true, Cert: cert}, config.ErrTLSPair},
		"key alone":  {config.TLS{Enabled: true, Key: config.Secret(key)}, config.ErrTLSPair},
	} {
		if _, err := c.tls.ClientConfig(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestConsulTLSBadPEMFailsOpen(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "127.0.0.1:1")
	t.Setenv("BACKPLANE_CONSUL_TLS_ENABLED", "true")
	t.Setenv("BACKPLANE_CONSUL_TLS_CERT", "not a pem")
	t.Setenv("BACKPLANE_CONSUL_TLS_KEY", "not a pem")

	if _, err := config.Open[struct {
		config.Backplane `json:"backplane"`
	}](t.Context(), consul.Config(), config.Service("hello"), config.WithoutFile()); err == nil {
		t.Fatal("unusable client certificate accepted")
	}
}
