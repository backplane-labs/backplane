package nats_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// refusing is a NATS server that refuses every client with the same
// authorization error, as a restarted server does before its accounts
// load; it counts the connections.
func refusing(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = lis.Close() })

	var accepted atomic.Int32

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}

			accepted.Add(1)

			go func() {
				defer conn.Close()

				_, _ = conn.Write([]byte(`INFO {"server_id":"refusing","version":"2.10.0","max_payload":1048576,"auth_required":true}` + "\r\n"))
				_, _ = bufio.NewReader(conn).ReadString('\n') // CONNECT
				_, _ = conn.Write([]byte("-ERR 'Authorization Violation'\r\n"))
			}()
		}
	}()

	return "nats://" + lis.Addr().String(), &accepted
}

// The same authorization error again and again never closes the
// connection: it keeps retrying, not ready but alive.
func TestAuthErrorsKeepRetrying(t *testing.T) {
	t.Parallel()

	url, accepted := refusing(t)

	conn, err := nats.Connect(nats.Config{URL: config.Secret(url)}, nats.Options{
		ReconnectWait: 5 * time.Millisecond, ReconnectJitter: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(conn.Close)

	for deadline := time.Now().Add(5 * time.Second); accepted.Load() < 5; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d attempts, closed %v", accepted.Load(), conn.IsClosed())
		}
	}

	if conn.IsClosed() {
		t.Fatal("repeated authorization errors closed the connection")
	}

	p := nats.New(nats.Config{URL: config.Secret(url)})
	if err := p.Probe(t.Context(), conn); !errors.Is(err, nats.ErrDisconnected) || errors.Is(err, deps.ErrTerminal) {
		t.Fatalf("reconnecting: %v", err)
	}

	conn.Close()

	if err := p.Probe(t.Context(), conn); !errors.Is(err, deps.ErrTerminal) {
		t.Fatalf("closed: %v", err)
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
