package bindings_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	sdkconfig "github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

// A save on one replica is what Active answers on another well before
// the other's poll: the notification carries it. The saving replica reads
// its own write at once; a hook never saved is ErrNoBinding on both.
func TestActiveFollowsOtherReplica(t *testing.T) {
	t.Parallel()

	a, iam, hub := liveHub(t)
	hook := iam + ".Audit"

	// b polls so rarely that only the notification can tell it.
	other := registry.NewHub()
	other.Publish(hub.services)

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: sdkconfig.Secret(os.Getenv("BACKPLANE_TEST_PG"))}))
	b := bindings.New(h.Root(), st, other, bindings.PollInterval(time.Hour))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()

	for _, m := range []*bindings.Manager{a, b} {
		if _, _, err := m.Active(ctx, hook); !errors.Is(err, bindings.ErrNoBinding) {
			t.Fatalf("before any save: %v", err)
		}
	}

	def := mustBinding(t, "hook: "+hook+"\nsteps: {x: {activity: billing.Charge, input: {v: 1}}}")

	saved, err := a.Save(ctx, def, "", bindings.Base{})
	if err != nil || len(saved.Violations) > 0 {
		t.Fatalf("save: %v %v", saved.Violations, err)
	}

	if _, v, err := a.Active(ctx, hook); err != nil || v != saved.Version.Version {
		t.Fatalf("own write: %d %v", v, err)
	}

	deadline := time.Now().Add(5 * time.Second)

	for {
		_, v, err := b.Active(ctx, hook)
		if err == nil && v == saved.Version.Version {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("other replica: %d %v", v, err)
		}

		time.Sleep(10 * time.Millisecond)
	}

	if _, err := a.Delete(ctx, hook, "", bindings.Base{}); err != nil {
		t.Fatal(err)
	}

	for {
		_, _, err := b.Active(ctx, hook)
		if errors.Is(err, bindings.ErrNoBinding) {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("other replica after delete: %v", err)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
