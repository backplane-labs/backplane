package nats_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nats-io/nkeys"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatal(err)
	}

	seed, err := kp.Seed()
	if err != nil {
		t.Fatal(err)
	}

	creds := "-----BEGIN NATS USER JWT-----\neyJhbGciOiJlZDI1NTE5In0.e30.sig\n------END NATS USER JWT------\n\n" +
		"-----BEGIN USER NKEY SEED-----\n" + string(seed) + "\n------END USER NKEY SEED------\n"

	if err := (nats.Config{URL: "nats://n:4222", Creds: config.Secret(creds)}).Validate(); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		cfg  nats.Config
		want error
	}{
		"no url":  {nats.Config{}, nats.ErrNoURL},
		"creds":   {nats.Config{URL: "nats://n:4222", Creds: "garbage"}, nil},
		"tls pem": {nats.Config{URL: "nats://n:4222", TLS: config.TLS{Enabled: true, CA: "junk"}}, config.ErrTLSCA},
	} {
		err := c.cfg.Validate()
		if err == nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestProvider connects to BACKPLANE_TEST_NATS (make up: localhost:4222);
// skipped without.
func TestProvider(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_NATS")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_NATS not set")
	}

	h := backplanetest.New(t)
	conn := deps.NewDependency(h.Root(), nats.New(nats.Config{URL: config.Secret("nats://" + addr)}), deps.Name("realtime"))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	if !conn.Get().IsConnected() {
		t.Fatal("not connected")
	}

	if err := conn.Get().Publish("infra.nats.test", []byte("ping")); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledConnectionRedactsCredentials(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := nats.New(nats.Config{URL: "nats://review:synthetic-password@127.0.0.1:1"}).Provide(ctx, h.Root())
	if err == nil {
		t.Fatal("expected canceled connection")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}

	if strings.Contains(err.Error(), "synthetic-password") {
		t.Fatal("connection error contains credentials")
	}
}
