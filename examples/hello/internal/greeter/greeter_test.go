package greeter_test

import (
	"errors"
	"testing"

	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/examples/hello/internal/hellotest"
	"github.com/backplane-labs/backplane/examples/hello/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

func TestGreetingUsesLiveConfig(t *testing.T) {
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
