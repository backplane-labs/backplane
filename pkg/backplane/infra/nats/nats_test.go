package nats_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/nats/natstest"
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

	if err := (nats.Config{URL: "nats://n:4222", InboxPrefix: "_INBOX_billing.v1"}).Validate(); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		cfg  nats.Config
		want error
	}{
		"no url":            {nats.Config{}, nats.ErrNoURL},
		"creds":             {nats.Config{URL: "nats://n:4222", Creds: "garbage"}, nil},
		"tls pem":           {nats.Config{URL: "nats://n:4222", TLS: config.TLS{Enabled: true, CA: "junk"}}, config.ErrTLSCA},
		"inbox wildcard":    {nats.Config{URL: "nats://n:4222", InboxPrefix: "_INBOX_billing.>"}, config.ErrInboxPrefix},
		"inbox dot":         {nats.Config{URL: "nats://n:4222", InboxPrefix: "_INBOX_billing."}, config.ErrInboxPrefix},
		"inbox empty token": {nats.Config{URL: "nats://n:4222", InboxPrefix: "_INBOX..billing"}, config.ErrInboxPrefix},
		"inbox space":       {nats.Config{URL: "nats://n:4222", InboxPrefix: "_INBOX billing"}, config.ErrInboxPrefix},
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

// With an inbox prefix, a request's reply and a JetStream API call's
// answer come on that prefix, none on _INBOX (BACKPLANE_TEST_NATS).
func TestInboxPrefix(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_NATS")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_NATS not set")
	}

	url := config.Secret("nats://" + addr)
	prefix := "_INBOX_test" + strconv.FormatInt(time.Now().UnixNano(), 36)

	watcher, err := nats.Connect(nats.Config{URL: url}, nats.Options{})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(watcher.Close)

	// own: JetStream's answers on the prefix. replies: where this
	// connection's requests ask to be answered, seen by a responder of its
	// own subject, so other clients of the shared server do not interfere.
	own, replies := make(chan string, 16), make(chan string, 16)
	service := prefix + "-service"

	if _, err := watcher.Subscribe(prefix+".>", func(m *natsgo.Msg) { own <- m.Subject }); err != nil {
		t.Fatal(err)
	}

	if _, err := watcher.Subscribe(service, func(m *natsgo.Msg) { replies <- m.Reply; _ = m.Respond(nil) }); err != nil {
		t.Fatal(err)
	}

	if err := watcher.Flush(); err != nil {
		t.Fatal(err)
	}

	conn, err := nats.Connect(nats.Config{URL: url, InboxPrefix: prefix}, nats.Options{})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(conn.Close)

	for !conn.IsConnected() {
		time.Sleep(10 * time.Millisecond)
	}

	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := js.AccountInfo(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case subject := <-own:
		if !strings.HasPrefix(subject, prefix+".") {
			t.Fatalf("answer on %s", subject)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no JetStream answer on the prefix")
	}

	if _, err := conn.Request(service, nil, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	if reply := <-replies; !strings.HasPrefix(reply, prefix+".") {
		t.Fatalf("a request asked for an answer on %s, not on the prefix", reply)
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

// Chaos: a JetStream cluster loses its meta leader under a live
// connection (two of three nodes down, the client's node up) and gets it
// back. With WithJetStream the probe fails readiness meanwhile, bounded by
// its deadline, never liveness, and recovers by itself; without it only
// the connection counts.
func TestJetStreamReadiness(t *testing.T) {
	t.Parallel()

	cluster := natstest.StartCluster(t, 3)
	cfg := nats.Config{URL: config.Secret(cluster.URL(0))}
	h := backplanetest.New(t)

	conn, err := nats.New(cfg).Provide(t.Context(), h.Root())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(conn.Close)

	withJS, plain := nats.New(cfg, nats.WithJetStream()), nats.New(cfg)

	ready := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		return withJS.Probe(ctx, conn)
	}

	waitFor := func(what string, ok func(error) bool) {
		t.Helper()

		var err error

		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if err = ready(); ok(err) {
				return
			}
		}

		t.Fatalf("%s: %v", what, err)
	}

	waitFor("cluster formed", func(err error) bool { return err == nil })

	cluster.Stop(1)
	cluster.Stop(2)

	waitFor("meta leader lost", func(err error) bool { return errors.Is(err, nats.ErrJetStream) })

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	began := time.Now()

	err = withJS.Probe(ctx, conn)
	if !errors.Is(err, nats.ErrJetStream) || errors.Is(err, deps.ErrTerminal) || !conn.IsConnected() {
		t.Fatalf("jetstream down: %v, connected %v", err, conn.IsConnected())
	}

	if took := time.Since(began); took > time.Second {
		t.Fatalf("the probe took %v past its 300ms deadline", took)
	}

	if err := plain.Probe(t.Context(), conn); err != nil {
		t.Fatalf("without WithJetStream the connection alone counts: %v", err)
	}

	cluster.Restart(t, 1)
	cluster.Restart(t, 2)

	waitFor("meta leader back", func(err error) bool { return err == nil })
}
