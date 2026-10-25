package broker_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
)

// seen records the event types a raw reactor got, of the given sources.
type seen struct {
	mu    sync.Mutex
	types []string
}

func (s *seen) handler(sources ...string) env.Handler {
	return func(ctx context.Context, _ []byte) ([]byte, error) {
		in, _ := env.IncomingOf(ctx)
		if slices.Contains(sources, in.Source) {
			s.mu.Lock()
			s.types = append(s.types, in.Type)
			s.mu.Unlock()
		}

		return nil, nil
	}
}

func (s *seen) has(types ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range types {
		if !slices.Contains(s.types, t) {
			return false
		}
	}

	return true
}

func (s *seen) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.types)
}

// emitting declares events of service.
func emitting(service string, names ...string) *env.Env {
	e := env.New(service, manifest.New(service, "1.0.0"))
	for _, n := range names {
		e.Manifest.Event(&backplanev1.Event{Name: n})
	}

	return e
}

// A one-service pattern takes every event of that service, none of another.
func TestRawReactorOfService(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, other, sub := unique("src"), unique("other"), unique("sub")
	jet := admin(t, url, src, other, sub)

	var s seen

	e := env.New(sub, manifest.New(sub, "1.0.0"))
	e.Reactor(env.Reactor{Event: src + ".*", Consumer: "journal", Handler: s.handler(src, other)})

	pub := open(t, url, src, emitting(src, "Greeted", "Left"))
	pubOther := open(t, url, other, emitting(other, "Greeted"))
	recv := open(t, url, sub, e)
	startReactors(t, jet, recv, src, broker.Durable(sub, "journal"))

	for _, ev := range []struct {
		b    *broker.Broker
		name string
	}{{pub, src + ".Greeted"}, {pub, src + ".Left"}, {pubOther, other + ".Greeted"}} {
		if err := ev.b.PublishRaw(t.Context(), ev.name, "", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}

	eventually(t, "both events of src", func() bool { return s.has(src+".Greeted", src+".Left") })

	time.Sleep(200 * time.Millisecond)

	if got := s.got(); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

// "*" takes the events of every service, including one whose stream
// appears after the reactor started.
func TestRawReactorOfAll(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	early, late, sub := unique("early"), unique("late"), unique("sub")
	jet := admin(t, url, early, late, sub)

	var s seen

	e := env.New(sub, manifest.New(sub, "1.0.0"))
	e.Reactor(env.Reactor{Event: "*", Consumer: "journal", Handler: s.handler(early, late)})

	pubEarly := open(t, url, early, emitting(early, "Greeted"))
	if err := pubEarly.PublishRaw(t.Context(), early+".Greeted", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	recv := open(t, url, sub, e)
	startReactors(t, jet, recv, early, broker.Durable(sub, "journal"))

	if err := pubEarly.PublishRaw(t.Context(), early+".Greeted", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	pubLate := open(t, url, late, emitting(late, "Joined"))
	if err := pubLate.PublishRaw(t.Context(), late+".Joined", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	eventually(t, "consumer on the late stream", func() bool {
		_, err := jet.Consumer(t.Context(), broker.StreamName(late), broker.Durable(sub, "journal"))

		return err == nil
	})

	// The late stream's consumer starts at new messages, after Joined.
	if err := pubLate.PublishRaw(t.Context(), late+".Joined", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	eventually(t, "events of both services", func() bool { return s.has(early+".Greeted", late+".Joined") })

	_, err := jet.Consumer(t.Context(), broker.DLQStreamName(sub), broker.Durable(sub, "journal"))
	if !errors.Is(err, jetstream.ErrConsumerNotFound) && !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Fatalf("a consumer on the dead letters: %v", err)
	}
}
