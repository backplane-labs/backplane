package broker_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	infranats "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

func TestMain(m *testing.M) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	os.Exit(m.Run())
}

func TestToken(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"iam":                     "iam",
		"mail-sender":             "mail-sender",
		"UserRegistered":          "UserRegistered",
		"a.b":                     "a_2Eb",
		"x_y":                     "x_5Fy",
		"greeter/templates:iam.X": "greeter_2Ftemplates_3Aiam_2EX",
		"a b*>":                   "a_20b_2A_3E",
		"":                        "",
	} {
		if got := broker.Token(in); got != want {
			t.Errorf("Token(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	for got, want := range map[string]string{
		broker.StreamName("mail-sender"):                                  "bp_mail-sender",
		broker.StreamSubjects("mail-sender"):                              "bp.mail-sender.>",
		broker.Subject("iam", "UserRegistered"):                           "bp.iam.UserRegistered",
		broker.DLQStreamName("mailer"):                                    "bp_dlq_mailer",
		broker.DLQStreamSubjects("mailer"):                                "bp.dlq.mailer.>",
		broker.DLQSubject("mailer", "iam.UserRegistered"):                 "bp.dlq.mailer.iam_2EUserRegistered",
		broker.Durable("mailer", "iam.UserRegistered"):                    "mailer__iam_2EUserRegistered",
		broker.Durable("mailer", "send:iam.UserRegistered"):               "mailer__send_3Aiam_2EUserRegistered",
		broker.RedriveSubject("iam", "mailer", "send:iam.UserRegistered"): "bp.iam._redrive.mailer.send_3Aiam_2EUserRegistered",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestDurableUnique(t *testing.T) {
	t.Parallel()

	if broker.Durable("a", "b__c") == broker.Durable("a__b", "c") {
		t.Fatal("durable names collide")
	}

	long := strings.Repeat("x", 300)
	a, b := broker.Durable("svc", long+"1"), broker.Durable("svc", long+"2")

	if len(a) > 200 || a == b {
		t.Fatalf("long names: %d %q %q", len(a), a, b)
	}
}

func TestSplitEvent(t *testing.T) {
	t.Parallel()

	service, name, err := broker.SplitEvent("iam.User.Registered")
	if err != nil || service != "iam" || name != "User.Registered" {
		t.Fatalf("%q %q %v", service, name, err)
	}

	for _, bad := range []string{"", "iam", ".X", "iam."} {
		if _, _, err := broker.SplitEvent(bad); !errors.Is(err, broker.ErrEventName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestHeaders(t *testing.T) {
	t.Parallel()

	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(t.Context(), spanCtx)
	when := time.Date(2026, 9, 28, 10, 0, 0, 5, time.FixedZone("x", 3600))

	h := broker.BuildHeaders(ctx, broker.Event{
		ID: "id-1", Service: "iam", Instance: "iam-1", Version: "1.2.3",
		Type: "iam.UserRegistered", Key: "user-7", Time: when,
	})

	for k, want := range map[string]string{
		"ce-specversion":      "1.0",
		"ce-id":               "id-1",
		"ce-source":           "iam",
		"ce-type":             "iam.UserRegistered",
		"ce-time":             "2026-09-28T09:00:00.000000005Z",
		"ce-subject":          "user-7",
		"ce-datacontenttype":  "application/json",
		"content-type":        "application/json",
		"ce-instance":         "iam-1",
		"ce-version":          "1.2.3",
		jetstream.MsgIDHeader: "id-1",
		"traceparent":         "00-01020300000000000000000000000000-0405060000000000-01",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	bare := broker.BuildHeaders(t.Context(), broker.Event{ID: "id-2", Service: "iam", Type: "iam.X", Time: when})
	for _, k := range []string{"ce-subject", "ce-instance", "ce-version", "traceparent"} {
		if _, ok := bare[k]; ok {
			t.Errorf("%s set without a value", k)
		}
	}
}

func TestDeadHeaders(t *testing.T) {
	t.Parallel()

	orig := nats.Header{}
	orig.Set("ce-id", "id-1")
	orig.Set(jetstream.MsgIDHeader, "id-1")
	orig.Set("Nats-Expected-Stream", "x")

	h := broker.DeadHeaders(orig, "dead-1", "send:iam.X", strings.Repeat("e", 10000), 5)
	if h.Get("ce-id") != "id-1" || h.Get(jetstream.MsgIDHeader) != "dead-1" || h.Get("Nats-Expected-Stream") != "" {
		t.Fatalf("headers %v", h)
	}

	if h.Get("bp-consumer") != "send:iam.X" || h.Get("bp-delivered") != "5" || len(h.Get("bp-error")) != 4096 {
		t.Fatalf("dead-letter headers %v", h)
	}

	if orig.Get(jetstream.MsgIDHeader) != "id-1" {
		t.Fatal("original headers changed")
	}
}

func TestNakDelay(t *testing.T) {
	t.Parallel()

	p := backoff.Policy{Min: time.Second, Max: 5 * time.Second}
	for n, want := range map[uint64]time.Duration{0: time.Second, 1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 5 * time.Second, 60: 5 * time.Second} {
		if got := broker.NakDelay(p, n); got != want {
			t.Errorf("delivery %d: %v, want %v", n, got, want)
		}
	}
}

func TestReservedService(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{Conn: infranats.Config{URL: "nats://127.0.0.1:1"}, Service: "dlq", Log: testlog.Discard()})
	if err := b.Connect(t.Context(), newGroup(t)); !errors.Is(err, broker.ErrReserved) {
		t.Fatalf("want ErrReserved, got %v", err)
	}
}

func TestPublishBeforeConnect(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{Conn: infranats.Config{URL: "nats://127.0.0.1:1"}, Service: "svc"})
	if err := b.PublishRaw(t.Context(), "svc.X", "", []byte("{}")); !errors.Is(err, env.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}

	if err := b.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := b.StopReactors(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// An unreachable NATS neither blocks the start nor Publish.
func TestUnreachableDoesNotBlock(t *testing.T) {
	t.Parallel()

	e := emitter(t, "svc")
	b := broker.New(broker.Params{Conn: infranats.Config{URL: "nats://127.0.0.1:1"}, Service: "svc", Log: testlog.Discard(), Env: e})
	b.Fast(3)

	start := time.Now()

	if err := b.Connect(t.Context(), newGroup(t)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = b.Close(context.Background()) })

	err := b.PublishRaw(t.Context(), "svc.Greeted", "", []byte("{}"))
	if !errors.Is(err, env.ErrUnavailable) || !errors.Is(err, broker.ErrNotConnected) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}

	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v", took)
	}
}

func TestReactorSettings(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{Service: "mail", Log: testlog.Discard()})
	h := func(context.Context, []byte) ([]byte, error) { return nil, nil }

	def, err := b.ReactorSettings(env.Reactor{Event: "iam.UserRegistered", Consumer: "c", Handler: h})
	if err != nil {
		t.Fatal(err)
	}

	if def.Consumer.MaxDeliver != 5+broker.StopDeliveries || def.Consumer.MaxAckPending != 0 || def.Consumer.AckWait != 45*time.Second || def.Concurrency != 4 ||
		def.Timeout != 30*time.Second || def.Nak != (backoff.Policy{Min: time.Second, Max: time.Minute}) ||
		def.StartAll || def.Consumer.DeliverPolicy != jetstream.DeliverNewPolicy {
		t.Fatalf("defaults: %+v", def)
	}

	// The consumer takes its event and the dead letters backplane redrives
	// to it alone.
	if f := def.Consumer.FilterSubjects; def.Consumer.FilterSubject != "" || len(f) != 2 ||
		f[0] != "bp.iam.UserRegistered" || f[1] != "bp.iam._redrive.mail.c" {
		t.Fatalf("filters: %q %q", def.Consumer.FilterSubject, f)
	}

	got, err := b.ReactorSettings(env.Reactor{Event: "iam.UserRegistered", Consumer: "c", Handler: h, Delivery: env.Delivery{
		MaxDeliver: 2, Concurrency: 1, Timeout: time.Second,
		Redelivery: backoff.Policy{Min: time.Millisecond, Max: 10 * time.Millisecond}, StartAll: true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	if got.Consumer.MaxDeliver != 2+broker.StopDeliveries || got.Consumer.AckWait != 16*time.Second || got.Concurrency != 1 ||
		got.Timeout != time.Second || got.Nak != (backoff.Policy{Min: time.Millisecond, Max: 10 * time.Millisecond}) ||
		!got.StartAll {
		t.Fatalf("declared: %+v", got)
	}
}

func TestOwnStreams(t *testing.T) {
	t.Parallel()

	events, dead := broker.New(broker.Params{Service: "mail"}).OwnStreams()
	if events.MaxAge != 7*24*time.Hour || events.MaxBytes != -1 || events.Replicas != 1 ||
		events.Duplicates != 2*time.Minute || dead.MaxAge != 30*24*time.Hour || dead.Replicas != 1 ||
		dead.Duplicates != 2*time.Minute {
		t.Fatalf("defaults: %+v %+v", events, dead)
	}

	events, dead = broker.New(broker.Params{Service: "mail", Streams: broker.Streams{
		MaxAge: time.Hour, MaxBytes: 1 << 30, Replicas: 3, Duplicates: 30 * time.Second, DeadMaxAge: time.Minute,
	}}).OwnStreams()
	if events.MaxAge != time.Hour || events.MaxBytes != 1<<30 || events.Replicas != 3 ||
		events.Duplicates != 30*time.Second || dead.MaxAge != time.Minute || dead.Replicas != 3 ||
		dead.Duplicates != time.Minute {
		t.Fatalf("set: %+v %+v", events, dead)
	}

	_, dead = broker.New(broker.Params{Service: "mail", Streams: broker.Streams{Replicas: 1}}).OwnStreams()
	if dead.MaxAge != 0 || dead.Duplicates != 2*time.Minute {
		t.Fatalf("unlimited dead letters: %+v", dead)
	}
}
