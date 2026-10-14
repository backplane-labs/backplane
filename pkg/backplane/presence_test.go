package backplane_test

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
)

// TestPhasesWhileDependencyRetries: the instance state is in Consul
// (phase starting, with the applied config revision) while a required
// dependency retries, before any catalog registration; serving registers;
// stopping removes both.
func TestPhasesWhileDependencyRetries(t *testing.T) {
	const name = "svc-phases"

	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	stateKey := "backplane/services/" + name + "/instances/" + name + "-1"

	t.Cleanup(func() {
		_, _ = c.KV().DeleteTree("backplane/services/"+name+"/", nil)
		_, _ = c.KV().DeleteTree("config/"+name+"/", nil)
		_ = c.Agent().ServiceDeregister(name + "-1")
	})

	if _, err := c.KV().Put(&api.KVPair{Key: "config/" + name + "/_revision", Value: []byte("3")}, nil); err != nil {
		t.Fatal(err)
	}

	setup(t, addr)
	t.Setenv("BACKPLANE_CONSUL_TAGS", `["phase-test"]`)
	t.Setenv("BACKPLANE_CONSUL_CHECK_INTERVAL", "4s")

	var connUp atomic.Bool

	conn := deps.Func(func(context.Context) (string, error) {
		if !connUp.Load() {
			return "", errDown
		}

		return "conn", nil
	})

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		return &testState{
			Conn: deps.NewDependency(root, conn, deps.Name("conn"), deps.Backoff(10*time.Millisecond, 20*time.Millisecond)),
		}, nil
	}, baseOptions(name)...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	state := func() *backplanev1.InstanceState {
		kv, _, err := c.KV().Get(stateKey, nil)
		if err != nil || kv == nil {
			return nil
		}

		var st backplanev1.InstanceState
		if proto.Unmarshal(kv.Value, &st) != nil {
			return nil
		}

		return &st
	}
	inCatalog := func() bool {
		services, _ := c.Agent().Services()

		return services[name+"-1"] != nil
	}

	eventually(t, "state while starting", func() bool {
		st := state()

		return st.GetPhase() == backplanev1.InstancePhase_INSTANCE_PHASE_STARTING
	})

	st := state()
	if st.GetConfigRevision() != 3 || st.GetSdkVersion() == "" || len(st.GetTransports()) == 0 ||
		st.GetTransports()[0].GetName() != "consul" {
		t.Fatalf("starting state: %v", st)
	}

	time.Sleep(200 * time.Millisecond) // the dependency keeps retrying

	if inCatalog() || state().GetPhase() != backplanev1.InstancePhase_INSTANCE_PHASE_STARTING {
		t.Fatal("registered or serving before the dependency")
	}

	connUp.Store(true)

	eventually(t, "serving", func() bool {
		return state().GetPhase() == backplanev1.InstancePhase_INSTANCE_PHASE_SERVING
	})
	eventually(t, "registered", inCatalog)

	if svc, _, err := c.Agent().Service(name+"-1", nil); err != nil || len(svc.Tags) != 1 || svc.Tags[0] != "phase-test" {
		t.Fatalf("registration: %+v %v", svc, err)
	}

	var checks map[string]struct{ Interval string }
	if _, err := c.Raw().Query("/v1/agent/checks", &checks, nil); err != nil || checks["service:"+name+"-1"].Interval != "4s" {
		t.Fatalf("check: %+v %v", checks, err)
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}

	if state() != nil || inCatalog() {
		t.Fatal("state or registration outlived the instance")
	}
}

// TestPreviousSecretAccepted: during a rotation both secrets open the
// platform port.
func TestPreviousSecretAccepted(t *testing.T) {
	p := setup(t, "")
	t.Setenv("BACKPLANE_INTERNAL_SECRET_PREVIOUS", "old-secret")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-rotate")...)
	if err != nil {
		t.Fatal(err)
	}

	svc.UI(fstest.MapFS{"plugin.json": {Data: []byte(`{"sdk_major":1}`)}})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	for presented, want := range map[string]int{secret: http.StatusOK, "old-secret": http.StatusOK, "other": http.StatusForbidden} {
		if code, body := get(p.platformURL()+"/_backplane/ui/plugin.json", guard.HTTPHeader, presented); code != want {
			t.Errorf("secret %q: %d %s", presented, code, body)
		}
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}
