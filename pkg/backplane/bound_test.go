package backplane_test

import (
	"context"
	"os"
	"testing"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
)

// A running service follows its bound hooks as backplane publishes them.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestBoundHooks(t *testing.T) {
	const name = "svc-bound"

	consul := os.Getenv("BACKPLANE_TEST_CONSUL")
	if consul == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	setup(t, consul)

	client, err := api.NewClient(&api.Config{Address: consul})
	if err != nil {
		t.Fatal(err)
	}

	key := "backplane/bindings/" + name
	put := func(v string) {
		t.Helper()

		if _, err := client.KV().Put(&api.KVPair{Key: key, Value: []byte(v)}, nil); err != nil {
			t.Fatal(err)
		}
	}

	put(`["Greet"]`)
	t.Cleanup(func() { _, _ = client.KV().Delete(key, nil) })

	var greet, other hook.Ref[struct{}, struct{}]

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		greet = hook.Declare[struct{}, struct{}](root, "Greet")
		other = hook.Declare[struct{}, struct{}](root, "Other")

		return &testState{Cfg: root.Config()}, nil
	}, baseOptions(name)...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "bound set known", func() bool { return greet.Bound() && !other.Bound() })

	changes := greet.BindingChanges(ctx)

	put(`["Other"]`)
	<-changes
	eventually(t, "bound set changed", func() bool { return !greet.Bound() && other.Bound() })

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}
