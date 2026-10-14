package broker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	infranats "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

// A bad TLS configuration fails Connect before dialing.
func TestConnectRejectsBadTLS(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{
		Conn:    infranats.Config{URL: "nats://127.0.0.1:1", TLS: config.TLS{Enabled: true, CA: "junk"}},
		Service: "svc", Log: testlog.Discard(),
	})
	if err := b.Connect(t.Context(), newGroup(t)); !errors.Is(err, config.ErrTLSCA) {
		t.Fatalf("want ErrTLSCA, got %v", err)
	}
}

func TestPublishTimeoutParam(t *testing.T) {
	t.Parallel()

	if d := broker.New(broker.Params{Service: "svc"}).PublishTimeout(); d != 5*time.Second {
		t.Fatalf("default %v", d)
	}

	if d := broker.New(broker.Params{Service: "svc", PublishTimeout: time.Second}).PublishTimeout(); d != time.Second {
		t.Fatalf("set %v", d)
	}
}

func TestNotConnected(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{Service: "svc"})
	if b.Connected() {
		t.Fatal("connected before Connect")
	}

	if _, err := b.JetStream(); !errors.Is(err, env.ErrUnavailable) {
		t.Fatalf("jetstream before Connect: %v", err)
	}
}

func TestHeadersExtensionsAndIncoming(t *testing.T) {
	t.Parallel()

	published := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	h := broker.BuildHeaders(context.Background(), broker.Event{
		ID: "id-1", Service: "iam", Instance: "iam-1", Version: "1.2.3", Type: "iam.UserRegistered",
		Key: "user-1", Time: published, Extensions: map[string]string{"tenant": "t1", "id": "spoofed"},
	})

	if h.Get("ce-tenant") != "t1" || h.Get("ce-id") != "id-1" || h.Get("Nats-Msg-Id") != "id-1" {
		t.Fatalf("headers %v", h)
	}

	// Header names may arrive canonicalized.
	canonical := nats.Header{}
	for k, v := range h {
		canonical[k] = v
	}

	canonical["Ce-Region"] = []string{"eu"}

	in := broker.Incoming(canonical, "mailer:iam.UserRegistered", 3)
	if in.ID != "id-1" || in.Source != "iam" || in.Type != "iam.UserRegistered" || in.Subject != "user-1" ||
		!in.Time.Equal(published) || in.Attempt != 3 || in.Consumer != "mailer:iam.UserRegistered" {
		t.Fatalf("incoming %+v", in)
	}

	want := map[string]string{"tenant": "t1", "region": "eu", "instance": "iam-1", "version": "1.2.3"}
	if len(in.Extensions) != len(want) {
		t.Fatalf("extensions %v", in.Extensions)
	}

	for k, v := range want {
		if in.Extensions[k] != v {
			t.Fatalf("extensions %v, want %v", in.Extensions, want)
		}
	}
}

func TestReactorSettingsOrderedInactive(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{Service: "mail", Log: testlog.Discard()})
	h := func(context.Context, []byte) ([]byte, error) { return nil, nil }

	got, err := b.ReactorSettings(env.Reactor{Event: "iam.UserRegistered", Consumer: "c", Handler: h, Delivery: env.Delivery{
		Ordered: true, InactiveThreshold: time.Hour,
	}})
	if err != nil {
		t.Fatal(err)
	}

	if got.Concurrency != 1 || got.Consumer.MaxAckPending != 1 || got.Consumer.InactiveThreshold != time.Hour {
		t.Fatalf("ordered: %+v", got)
	}
}
