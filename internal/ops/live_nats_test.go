package ops_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/wire"
)

func unique(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)

	return prefix + "-" + hex.EncodeToString(b)
}

// liveJetStream connects to BACKPLANE_TEST_NATS; the test is skipped
// without it. The streams of services are deleted at cleanup.
func liveJetStream(t *testing.T, services ...string) jetstream.JetStream {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_NATS")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_NATS not set (make up)")
	}

	conn, err := nats.Connect("nats://" + addr)
	if err != nil {
		t.Fatal(err)
	}

	jet, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, s := range services {
			_ = jet.DeleteStream(context.Background(), wire.StreamName(s))
			_ = jet.DeleteStream(context.Background(), wire.DLQStreamName(s))
		}

		conn.Close()
	})

	return jet
}

func mustStream(t *testing.T, jet jetstream.JetStream, name, subjects string) {
	t.Helper()

	if _, err := jet.CreateStream(t.Context(), jetstream.StreamConfig{
		Name: name, Subjects: []string{subjects}, Metadata: map[string]string{wire.MetaKind: "test"},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestEventsLive: against NATS, the console sees a service's stream, its
// messages and consumers, publishes test events, lists dead letters,
// redrives them to their reactor alone and purges them.
//
//nolint:paralleltest // one scenario over one NATS
func TestEventsLive(t *testing.T) {
	src, sub := unique("opss"), unique("opsw")
	jet := liveJetStream(t, src, sub)

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{
		src: service(&backplanev1.Manifest{Service: src, Version: "1.0.0", Events: []*backplanev1.Event{{Name: "Greeted"}}}),
		sub: service(&backplanev1.Manifest{Service: sub, Version: "1.0.0", Subscriptions: []*backplanev1.Subscription{
			{Event: src + ".Greeted", Consumer: "sink"}, {Event: src + ".Greeted", Consumer: "old"},
		}}),
	})

	srv := newServers(ops.Detached(hub, ops.WithJetStream(func() (jetstream.JetStream, error) { return jet, nil })))
	ctx := t.Context()

	mustStream(t, jet, wire.StreamName(src), wire.StreamSubjects(src))
	mustStream(t, jet, wire.DLQStreamName(sub), wire.DLQStreamSubjects(sub))

	// The reactor's consumer as the SDK creates it; one of an older SDK;
	// a rule's.
	sink, err := jet.CreateConsumer(ctx, wire.StreamName(src), jetstream.ConsumerConfig{
		Durable: wire.Durable(sub, "sink"), AckPolicy: jetstream.AckExplicitPolicy,
		FilterSubjects: []string{wire.Subject(src, "Greeted"), wire.RedriveSubject(src, sub, "sink")},
		Metadata:       map[string]string{wire.MetaService: sub, wire.MetaConsumer: "sink", wire.MetaEvent: src + ".Greeted"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, cfg := range []jetstream.ConsumerConfig{
		{Durable: wire.Durable(sub, "old"), FilterSubject: wire.Subject(src, "Greeted")},
		{Durable: "backplane__rule_1", FilterSubject: wire.Subject(src, "Greeted")},
	} {
		if _, err := jet.CreateConsumer(ctx, wire.StreamName(src), cfg); err != nil {
			t.Fatal(err)
		}
	}

	before := time.Now().Add(-time.Second)

	for _, key := range []string{"a", "b", "c"} {
		res, err := srv.events.PublishTestEvent(ctx, &consolev1.PublishTestEventRequest{
			Event: src + ".Greeted", Payload: `{"key":"` + key + `"}`, Key: key,
		})
		if err != nil || res.GetSeq() == 0 || res.GetDuplicate() {
			t.Fatalf("publish %s: %v %v", key, res, err)
		}
	}

	again, err := srv.events.PublishTestEvent(ctx, &consolev1.PublishTestEventRequest{Event: src + ".Greeted", Id: "dup"})
	if err != nil {
		t.Fatal(err)
	}

	if dup, err := srv.events.PublishTestEvent(ctx, &consolev1.PublishTestEventRequest{Event: src + ".Greeted", Id: "dup"}); err != nil ||
		!dup.GetDuplicate() || dup.GetSeq() != again.GetSeq() {
		t.Fatalf("duplicate: %v %v", dup, err)
	}

	// Stats: 4 messages, the reactor lagging by all of them.
	list, err := srv.events.ListEvents(ctx, &consolev1.ListEventsRequest{Service: src})
	if err != nil || list.GetNatsError() != "" || len(list.GetEvents()) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}

	greeted := list.GetEvents()[0]
	if greeted.GetMessages() != 4 || greeted.GetLastSeq() != 4 || list.GetStreams()[src].GetMessages() != 4 {
		t.Fatalf("event stats: %v", greeted)
	}

	kinds := map[string]*consolev1.Subscriber{}
	for _, s := range greeted.GetSubscribers() {
		kinds[s.GetDurable()] = s
	}

	if s := kinds[wire.Durable(sub, "sink")]; s == nil || !s.GetRedrive() || s.GetState().GetNumPending() != 4 {
		t.Fatalf("sink: %v", s)
	}

	if s := kinds[wire.Durable(sub, "old")]; s == nil || s.GetRedrive() || s.GetState() == nil {
		t.Fatalf("old: %v", s)
	}

	if s := kinds["backplane__rule_1"]; s == nil || s.GetKind() != consolev1.SubscriberKind_SUBSCRIBER_KIND_RULE ||
		s.GetEvent() != src+".Greeted" {
		t.Fatalf("rule: %v", s)
	}

	// Peek: the newest, a page forward, from a time.
	tailRes, err := srv.events.PeekMessages(ctx, &consolev1.PeekMessagesRequest{Service: src, Event: "Greeted", Limit: 2})
	if err != nil || len(tailRes.GetMessages()) != 2 || tailRes.GetMessages()[0].GetSeq() != 3 ||
		tailRes.GetMessages()[1].GetSeq() != 4 {
		t.Fatalf("tail: %v %v", tailRes, err)
	}

	page, err := srv.events.PeekMessages(ctx, &consolev1.PeekMessagesRequest{Service: src, StartSeq: 1, Limit: 2})
	if err != nil || len(page.GetMessages()) != 2 || page.GetNextSeq() != 3 ||
		page.GetMessages()[0].GetCloudEvent().GetSubject() != "a" || !page.GetMessages()[0].GetTest() ||
		page.GetMessages()[0].GetData() != `{"key":"a"}` {
		t.Fatalf("page: %v %v", page, err)
	}

	byTime, err := srv.events.PeekMessages(ctx, &consolev1.PeekMessagesRequest{
		Service: src, StartTime: timestamppb.New(before), Limit: 1,
	})
	if err != nil || len(byTime.GetMessages()) != 1 || byTime.GetMessages()[0].GetSeq() != 1 {
		t.Fatalf("by time: %v %v", byTime, err)
	}

	// Dead letters of the reactor.
	for _, id := range []string{"d1", "d2"} {
		h := nats.Header{}
		h.Set(wire.HeaderID, id)
		h.Set(wire.HeaderType, src+".Greeted")
		h.Set(wire.HeaderError, "boom")
		h.Set(wire.HeaderConsumer, "sink")
		h.Set(wire.HeaderDelivered, "5")

		if _, err := jet.PublishMsg(ctx, &nats.Msg{Subject: wire.DLQSubject(sub, "sink"), Header: h, Data: []byte(`{"id":"` + id + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}

	dead, err := srv.events.ListDeadLetters(ctx, &consolev1.ListDeadLettersRequest{Subscriber: sub, Consumer: "sink"})
	if err != nil || len(dead.GetDeadLetters()) != 2 || len(dead.GetCounts()) != 1 || dead.GetCounts()[0].GetCount() != 2 {
		t.Fatalf("dead letters: %v %v", dead, err)
	}

	first := dead.GetDeadLetters()[0]
	if first.GetError() != "boom" || first.GetDelivered() != 5 || first.GetEventSubject() != wire.Subject(src, "Greeted") {
		t.Fatalf("dead letter: %v", first)
	}

	// Drain the reactor's backlog, then redrive one dead letter: only the
	// reactor's consumer gets it, on its redrive subject.
	drain(t, sink, 4)

	redriven, err := srv.events.RedriveDeadLetters(ctx, &consolev1.RedriveDeadLettersRequest{
		Subscriber: sub, Consumer: "sink", Seqs: []uint64{first.GetMessage().GetSeq()},
	})
	if err != nil || redriven.GetRedriven() != 1 || len(redriven.GetFailed()) != 0 {
		t.Fatalf("redrive: %v %v", redriven, err)
	}

	got := drain(t, sink, 1)
	if got[0].Subject() != wire.RedriveSubject(src, sub, "sink") || got[0].Headers().Get(wire.HeaderID) != "d1" ||
		got[0].Headers().Get(wire.HeaderError) != "" {
		t.Fatalf("redriven message: %s %v", got[0].Subject(), got[0].Headers())
	}

	if s := kindsAfter(t, srv, src)[wire.Durable(sub, "old")]; s.GetState().GetNumPending() != 4 {
		t.Fatalf("another reactor saw the redrive: %v", s.GetState())
	}

	// Refused: an older consumer, an undeclared reactor.
	if _, err := srv.events.RedriveDeadLetters(ctx, &consolev1.RedriveDeadLettersRequest{Subscriber: sub, Consumer: "old", All: true}); code(t, err) != codes.FailedPrecondition {
		t.Fatalf("old consumer: %v", err)
	}

	if _, err := srv.events.RedriveDeadLetters(ctx, &consolev1.RedriveDeadLettersRequest{Subscriber: sub, Consumer: "nope", All: true}); code(t, err) != codes.NotFound {
		t.Fatalf("undeclared: %v", err)
	}

	stream, err := srv.events.GetStream(ctx, &consolev1.GetStreamRequest{Service: sub})
	if err != nil || stream.GetDeadLetters().GetMessages() != 1 || len(stream.GetDeadLetterCounts()) != 1 {
		t.Fatalf("stream: %v %v", stream, err)
	}

	purged, err := srv.events.PurgeDeadLetters(ctx, &consolev1.PurgeDeadLettersRequest{Subscriber: sub, Consumer: "sink"})
	if err != nil || purged.GetPurged() != 1 {
		t.Fatalf("purge: %v %v", purged, err)
	}

	if left, err := srv.events.ListDeadLetters(ctx, &consolev1.ListDeadLettersRequest{Subscriber: sub}); err != nil ||
		len(left.GetDeadLetters()) != 0 {
		t.Fatalf("after purge: %v %v", left, err)
	}
}

// drain fetches and acks n messages of c.
func drain(t *testing.T, c jetstream.Consumer, n int) []jetstream.Msg {
	t.Helper()

	var out []jetstream.Msg

	deadline := time.Now().Add(10 * time.Second)
	for len(out) < n && time.Now().Before(deadline) {
		batch, err := c.Fetch(n-len(out), jetstream.FetchMaxWait(time.Second))
		if err != nil {
			t.Fatal(err)
		}

		for m := range batch.Messages() {
			_ = m.Ack()
			out = append(out, m)
		}
	}

	if len(out) != n {
		t.Fatalf("fetched %d of %d", len(out), n)
	}

	return out
}

// kindsAfter are the subscribers of src's events by durable name.
func kindsAfter(t *testing.T, srv servers, src string) map[string]*consolev1.Subscriber {
	t.Helper()

	list, err := srv.events.ListEvents(t.Context(), &consolev1.ListEventsRequest{Service: src})
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]*consolev1.Subscriber{}

	for _, ev := range list.GetEvents() {
		for _, s := range ev.GetSubscribers() {
			out[s.GetDurable()] = s
		}
	}

	return out
}
