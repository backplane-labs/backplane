package conformance_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
)

// TestConsoleRedrive: backplane's console redrives a reactor's dead
// letters to that reactor alone. A service built on the SDK subscribes to
// its own event with two reactors; one dead-letters an event; the
// console's RedriveDeadLetters publishes it back on the reactor's redrive
// subject, the reactor's handler gets it (same ce-id), the other reactor
// does not, and the dead letter is gone. The console's test event reaches
// both reactors.
//
//nolint:paralleltest // one NATS, services named per run
func TestConsoleRedrive(t *testing.T) {
	url := natsURLOf(t)
	name := uniqueName("redrive")
	jet := jetStreamOf(t, url, name)

	var (
		lock     sync.Mutex
		fixed    bool
		sink     []string
		audit    []string
		attempts = map[string]int{}
	)

	runNATSService(t, url, jet, name, quickStop, func(root deps.Scope) {
		event.Declare[pingV1](root, "Ping")
		event.React(root, name+".Ping", func(ctx context.Context, v pingV1) error {
			lock.Lock()
			defer lock.Unlock()

			if v.Mode == "fail" && !fixed {
				return event.Terminal(errors.New("not yet"))
			}

			d, _ := event.DeliveryOf(ctx)
			attempts[v.ID] = d.Attempt
			sink = append(sink, v.ID)

			return nil
		}, event.Consumer("sink"))
		event.React(root, name+".Ping", func(_ context.Context, v pingV1) error {
			lock.Lock()
			defer lock.Unlock()

			audit = append(audit, v.ID)

			return nil
		}, event.Consumer("audit"))
	}, "bp_"+name, name+"__sink", name+"__audit")

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{name: {
		Name: name,
		Manifests: map[string]*backplanev1.Manifest{"1.0.0": {
			Service: name, Version: "1.0.0", Events: []*backplanev1.Event{{Name: "Ping"}},
			Subscriptions: []*backplanev1.Subscription{
				{Event: name + ".Ping", Consumer: "sink"}, {Event: name + ".Ping", Consumer: "audit"},
			},
		}},
	}})

	o := ops.Detached(hub, ops.WithJetStream(func() (jetstream.JetStream, error) { return jet.JetStream, nil }))
	api := o.Events()

	for _, p := range []string{`{"id":"f","mode":"fail"}`, `{"id":"o","mode":"ok"}`} {
		if _, err := api.PublishTestEvent(t.Context(), &consolev1.PublishTestEventRequest{Event: name + ".Ping", Payload: p}); err != nil {
			t.Fatal(err)
		}
	}

	var dead *consolev1.ListDeadLettersResponse

	waitFor(t, "one dead letter", func() bool {
		var err error

		dead, err = api.ListDeadLetters(t.Context(), &consolev1.ListDeadLettersRequest{Subscriber: name, Consumer: "sink"})

		return err == nil && len(dead.GetDeadLetters()) == 1
	})

	if d := dead.GetDeadLetters()[0]; !strings.Contains(d.GetError(), "not yet") || d.GetConsumer() != "sink" ||
		!d.GetMessage().GetTest() {
		t.Fatalf("dead letter: %v", d)
	}

	lock.Lock()
	fixed = true
	lock.Unlock()

	res, err := api.RedriveDeadLetters(t.Context(), &consolev1.RedriveDeadLettersRequest{Subscriber: name, Consumer: "sink", All: true})
	if err != nil || res.GetRedriven() != 1 || len(res.GetFailed()) != 0 {
		t.Fatalf("redrive: %v %v", res, err)
	}

	waitFor(t, "redriven event handled", func() bool {
		lock.Lock()
		defer lock.Unlock()

		return strings.Join(sink, ",") == "o,f"
	})

	time.Sleep(time.Second) // the other reactor would have had it by now

	lock.Lock()
	defer lock.Unlock()

	// The audit reactor is concurrent: handler completion order is not
	// delivery order. Assert the exact set (including duplicate count).
	slices.Sort(audit)

	if strings.Join(audit, ",") != "f,o" || attempts["f"] != 1 {
		t.Fatalf("audit saw %v; redriven attempt %d", audit, attempts["f"])
	}

	if left, err := api.ListDeadLetters(t.Context(), &consolev1.ListDeadLettersRequest{Subscriber: name}); err != nil ||
		len(left.GetDeadLetters()) != 0 {
		t.Fatalf("left: %v %v", left, err)
	}
}
