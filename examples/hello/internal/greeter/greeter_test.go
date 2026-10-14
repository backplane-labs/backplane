package greeter_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/examples/hello/internal/hellotest"
	"github.com/backplane-labs/backplane/examples/hello/internal/store"
	hellov1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

func TestGreetPublishes(t *testing.T) {
	t.Parallel()

	tree := hellotest.New(t, map[string]string{"SUFFIX": "?"})
	tree.H.Start()

	text, err := tree.Greeter.Text(t.Context(), "ann")
	if err != nil || text != "Hello, ann?" {
		t.Fatalf("text %q, %v", text, err)
	}

	// Live: applied in place, the next greeting sees it.
	backplanetest.SetLive(&tree.GreetCfg.Excited, true)

	if text, _ = tree.Greeter.Text(t.Context(), "ann"); text != "Hello, ann?!!" {
		t.Fatalf("excited: %q", text)
	}

	got := backplanetest.EventsWithMeta(tree.H, tree.Greeter.Greeted())
	if len(got) != 2 {
		t.Fatalf("events: %+v", got)
	}

	first, second := got[0], got[1]
	if first.Value != (greeter.Greeted{Name: "ann", Count: 1, Text: "Hello, ann?"}) || first.Key != "ann" ||
		!strings.HasSuffix(first.ID, "-ann-1") || first.Headers["excited"] != "false" {
		t.Errorf("first: %+v", first)
	}

	if second.Value.Count != 2 || !strings.HasSuffix(second.ID, "-ann-2") || second.Headers["excited"] != "true" {
		t.Errorf("second: %+v", second)
	}
}

// A greeting does not depend on the event transport.
func TestGreetWithoutTransport(t *testing.T) {
	t.Parallel()

	tree := hellotest.New(t, nil)
	tree.H.Start()

	backplanetest.FailPublish(tree.H, errors.New("stream full"))

	if text, err := tree.Greeter.Text(t.Context(), "bob"); err != nil || text != "Hello, bob!" {
		t.Fatalf("with failing publish: %q %v", text, err)
	}

	backplanetest.FailPublish(tree.H, nil)
	backplanetest.Unavailable(tree.H)

	res, err := tree.Greeter.Greet(t.Context(), &hellov1.GreetRequest{Name: "grpc"})
	if err != nil || res.GetGreeting() != "Hello, grpc!" {
		t.Fatalf("grpc without NATS: %v %v", res, err)
	}

	if got := backplanetest.Events(tree.H, tree.Greeter.Greeted()); len(got) != 0 {
		t.Fatalf("recorded: %+v", got)
	}
}

func TestReadiness(t *testing.T) {
	t.Parallel()

	tree := hellotest.New(t, nil)
	tree.H.Start()

	if err := backplanetest.Ready(tree.H); err != nil {
		t.Fatalf("ready: %v", err)
	}

	if err := errors.Join(tree.Greeter.Ready(t.Context()), tree.Greeter.Alive(t.Context())); err != nil {
		t.Fatalf("greeter probes: %v", err)
	}

	// The store's outage takes the service out of readiness.
	backplanetest.SetLive(&tree.StoreCfg.Down, true)

	if err := backplanetest.Ready(tree.H); !errors.Is(err, store.ErrDown) {
		t.Fatalf("store down: %v", err)
	}

	backplanetest.SetLive(&tree.StoreCfg.Down, false)

	if err := backplanetest.Ready(tree.H); err != nil {
		t.Fatalf("store back: %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, value string
		want        error
	}{
		{"SUFFIX", "123456789", greeter.ErrSuffix},
		{"SALUTE", " ", greeter.ErrSalute},
	} {
		_, err := backplanetest.LoadConfig[greeter.Config](t.Context(), map[string]string{tc.name: tc.value})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s=%q: %v, want %v", tc.name, tc.value, err, tc.want)
		}
	}

	if cfg := backplanetest.Config[greeter.Config](t, nil); cfg.Salute != "Hello" || cfg.Suffix.Get() != "!" {
		t.Errorf("defaults: %q %q", cfg.Salute, cfg.Suffix.Get())
	}
}
