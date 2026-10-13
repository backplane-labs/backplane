package consul_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/configrt"
	"github.com/gopherex/backplane/pkg/backplane/internal/consul"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

var errTest = errors.New("validate: limit: must be positive")

// readState decodes the instance state the fake holds; nil when absent.
func (f *fakeConsul) readState(key string) *backplanev1.InstanceState {
	var raw []byte

	f.with(func(f *fakeConsul) { raw = f.kv[key].value })

	if raw == nil {
		return nil
	}

	var st backplanev1.InstanceState
	if err := proto.Unmarshal(raw, &st); err != nil {
		return nil
	}

	return &st
}

func (f *fakeConsul) registered() []api.AgentServiceRegistration {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.registrations)
}

// The state is published while starting; the catalog registration waits
// for Register and leaves at Deregister, before the state goes.
func TestPhasesAndRegistration(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)

	p, err := consul.New(consul.Params{
		Client: c, Log: testlog.Discard(), Identity: testID, Register: true,
		Manifest: &backplanev1.Manifest{Service: "svc", Version: "1.0.0", SdkVersion: "v9.9.9"},
		Tags:     []string{"blue", "canary"},
		Check:    consul.Check{Interval: 3 * time.Second, Timeout: time.Second, DeregisterAfter: 2 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}

	p.UseFastTiming()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	phase := func() backplanev1.InstancePhase { return f.readState(key).GetPhase() }

	eventually(t, "starting", func() bool { return phase() == backplanev1.InstancePhase_INSTANCE_PHASE_STARTING })

	if st := f.readState(key); st.GetSdkVersion() != "v9.9.9" || st.GetTransports()[0].GetName() != "consul" {
		t.Fatalf("state: %v", st)
	}

	time.Sleep(100 * time.Millisecond) // several renew ticks

	if regs := f.registered(); len(regs) != 0 {
		t.Fatalf("registered before serving: %v", regs)
	}

	if err := p.Register(ctx); err != nil {
		t.Fatal(err)
	}

	eventually(t, "serving", func() bool { return phase() == backplanev1.InstancePhase_INSTANCE_PHASE_SERVING })
	eventually(t, "registered", func() bool { return len(f.registered()) == 1 })

	reg := f.registered()[0]
	if !slices.Equal(reg.Tags, []string{"blue", "canary"}) || reg.Check.Interval != "3s" ||
		reg.Check.Timeout != "1s" || reg.Check.DeregisterCriticalServiceAfter != "2m0s" {
		t.Fatalf("registration: tags %v check %+v", reg.Tags, reg.Check)
	}

	if err := p.Deregister(ctx); err != nil {
		t.Fatal(err)
	}

	if st := f.readState(key); st.GetPhase() != backplanev1.InstancePhase_INSTANCE_PHASE_STOPPING {
		t.Fatalf("after Deregister: %v", st.GetPhase())
	}

	f.with(func(f *fakeConsul) {
		if f.deregisters != 1 {
			t.Errorf("deregisters: %d", f.deregisters)
		}
	})

	time.Sleep(100 * time.Millisecond)

	if regs := f.registered(); len(regs) != 1 {
		t.Fatalf("registered again after Deregister: %d", len(regs))
	}

	cancel()

	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if f.readState(key) != nil {
		t.Fatal("state outlived Stop")
	}
}

// A failing registration is retried on the renew ticks without dropping
// the session.
func TestRegistrationRetried(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)

	p, err := consul.New(consul.Params{
		Client: c, Log: testlog.Discard(), Identity: testID, Register: true, Manifest: &backplanev1.Manifest{},
	})
	if err != nil {
		t.Fatal(err)
	}

	p.UseFastTiming()
	p.SetRenewGiveUp(5 * time.Second) // only the registration may fail here

	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(func() {
		cancel()

		_ = p.Stop(context.Background())
	})

	if err := p.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	eventually(t, "established", established(f, p, key))

	session := p.Session()

	f.with(func(f *fakeConsul) { f.failRegisters = 3 })

	if err := p.Register(ctx); err != nil {
		t.Fatal(err)
	}

	eventually(t, "registered after failures", func() bool { return len(f.registered()) == 1 })

	if p.Session() != session {
		t.Fatal("a failing registration re-established the session")
	}
}

type nodesAndTransports struct {
	mu    sync.Mutex
	ready bool
}

func (n *nodesAndTransports) nodes() []*backplanev1.NodeStatus {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.ready {
		return []*backplanev1.NodeStatus{{Path: "store", Ready: true}}
	}

	return []*backplanev1.NodeStatus{{Path: "store", Error: "dial: refused"}}
}

func (n *nodesAndTransports) set(ready bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.ready = ready
}

// Nodes and transports are polled on every renew tick; the commit and the
// revisions come through.
func TestStateCarriesNodesTransportsRevisions(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	id := testID
	id.Commit = "abc123"
	key := stateKey(id)
	statuses := &nodesAndTransports{}
	cfg := &fakeConfig{values: []byte("{}")}
	cfg.eff = configrt.Effective{Revision: 7, RejectedRevision: 8, Err: errTest}

	start(t, consul.Params{
		Client: c, Identity: id, Config: cfg, Nodes: statuses.nodes,
		Transports: func() []*backplanev1.TransportStatus {
			return []*backplanev1.TransportStatus{
				{Name: "nats", Connected: true}, {Name: "consul", Connected: false}, {Name: "otlp", Error: "no endpoint"},
			}
		},
	})

	eventually(t, "published", func() bool { return f.readState(key) != nil })

	st := f.readState(key)
	if st.GetCommit() != "abc123" || st.GetConfigRevision() != 7 || st.GetConfigRejectedRevision() != 8 ||
		st.GetConfigError() != errTest.Error() {
		t.Fatalf("state: %v", st)
	}

	names := make([]string, 0, len(st.GetTransports()))
	for _, tr := range st.GetTransports() {
		names = append(names, tr.GetName())
	}

	if !slices.Equal(names, []string{"consul", "nats", "otlp"}) {
		t.Fatalf("transports: %v", names)
	}

	if n := st.GetNodes(); len(n) != 1 || n[0].GetReady() || n[0].GetError() == "" {
		t.Fatalf("nodes: %v", n)
	}

	statuses.set(true)

	eventually(t, "node change polled", func() bool {
		n := f.readState(key).GetNodes()

		return len(n) == 1 && n[0].GetReady()
	})
}

// A released version's manifest is never overwritten with a different one;
// a dev version's is.
type resultGroup struct {
	ctx    context.Context
	result chan error
}

func (g resultGroup) Go(fn func(context.Context) error) {
	go func() { g.result <- fn(g.ctx) }()
}

func TestManifestConflictStopsPresence(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		version string
		race    bool
	}{
		{"1.0.0", false}, {"0.0.0+abcdef", false}, {"1.0.0", true}, {"0.0.0+abcdef", true},
	} {
		t.Run(fmt.Sprintf("%s/race=%v", scenario.version, scenario.race), func(t *testing.T) {
			version := scenario.version

			t.Parallel()
			f, c := newFake(t)
			id := testID
			id.Version = version
			key := "backplane/services/svc/manifests/" + version
			old, _ := proto.Marshal(&backplanev1.Manifest{Service: "svc", Version: version, SdkVersion: "old"})

			f.with(func(f *fakeConsul) {
				if scenario.race {
					f.manifestOnCAS = old
				} else {
					f.kv[key] = fakeKV{value: old}
				}
			})

			p, err := consul.New(consul.Params{
				Client: c, Identity: id, Log: testlog.Discard(), Register: true,
				Manifest: &backplanev1.Manifest{Service: "svc", Version: version, SdkVersion: "new"},
			})
			if err != nil {
				t.Fatal(err)
			}

			p.UseFastTiming()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			result := make(chan error, 1)

			if err := p.Register(ctx); err != nil {
				t.Fatal(err)
			}

			if err := p.Start(ctx, resultGroup{ctx, result}); err != nil {
				t.Fatal(err)
			}

			select {
			case err := <-result:
				if !errors.Is(err, consul.ErrManifestConflict) {
					t.Fatalf("conflict: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("conflict did not stop startup")
			}

			if f.readState(stateKey(id)) != nil || len(f.registered()) != 0 {
				t.Fatal("conflicting instance published state or registered")
			}

			var m backplanev1.Manifest

			f.with(func(f *fakeConsul) { _ = proto.Unmarshal(f.kv[key].value, &m) })

			if m.GetSdkVersion() != "old" {
				t.Fatal("conflict overwrote existing manifest")
			}

			if err := p.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManifestCreatedOnce(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := "backplane/services/svc/manifests/1.0.0"
	logs := &logBuf{}
	p := start(t, consul.Params{Client: c, Identity: testID, Log: xlog.NewJSON(xlog.WithWriter(logs))})

	eventually(t, "established", established(f, p, stateKey(testID)))

	var m backplanev1.Manifest

	f.with(func(f *fakeConsul) { _ = proto.Unmarshal(f.kv[key].value, &m) })

	if m.GetService() != "svc" || logs.has("different manifest") {
		t.Fatalf("manifest %v", &m)
	}
}

// Runs against a real Consul: the knobs reach the agent, phases and the
// manifest guard behave as with the fake.
//
//nolint:paralleltest // shares one Consul with other packages' tests
func TestLiveKnobsPhasesManifest(t *testing.T) {
	c := client(t)
	id := consul.Identity{
		Service: "presence-knobs", Version: "2.0.0", Instance: "presence-knobs-1", Address: "127.0.0.1", PlatformPort: 1,
		Commit: "deadbeef",
	}
	key := stateKey(id)
	manifestKey := "backplane/services/presence-knobs/manifests/2.0.0"

	t.Cleanup(func() {
		_, _ = c.KV().DeleteTree("backplane/services/presence-knobs/", nil)
		_ = c.Agent().ServiceDeregister(id.Instance)
	})

	old, _ := proto.Marshal(&backplanev1.Manifest{Service: id.Service, Version: id.Version, SdkVersion: "old"})
	if _, err := c.KV().Put(&api.KVPair{Key: manifestKey, Value: old}, nil); err != nil {
		t.Fatal(err)
	}

	conflict, err := consul.New(consul.Params{
		Client: c, Log: testlog.Discard(), Identity: id, Register: true,
		Manifest: &backplanev1.Manifest{Service: id.Service, Version: id.Version, SdkVersion: "new"},
	})
	if err != nil {
		t.Fatal(err)
	}

	conflictCtx, stopConflict := context.WithCancel(t.Context())
	defer stopConflict()

	result := make(chan error, 1)
	if err := conflict.Start(conflictCtx, resultGroup{conflictCtx, result}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		if !errors.Is(err, consul.ErrManifestConflict) {
			t.Fatalf("live conflict: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live conflict did not stop startup")
	}

	if kv, _, err := c.KV().Get(key, nil); err != nil || kv != nil {
		t.Fatalf("conflicting instance published state: %v %v", kv, err)
	}

	if err := conflict.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	p, err := consul.New(consul.Params{
		Client: c, Log: testlog.Discard(), Identity: id, Register: true,
		Manifest:   &backplanev1.Manifest{Service: id.Service, Version: id.Version, SdkVersion: "old"},
		Tags:       []string{"knob"},
		Check:      consul.Check{Interval: 7 * time.Second, Timeout: 2 * time.Second, DeregisterAfter: 3 * time.Minute},
		SessionTTL: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	phase := func() backplanev1.InstancePhase {
		kv, _, err := c.KV().Get(key, nil)
		if err != nil || kv == nil {
			return backplanev1.InstancePhase_INSTANCE_PHASE_UNSPECIFIED
		}

		var st backplanev1.InstanceState

		_ = proto.Unmarshal(kv.Value, &st)

		return st.GetPhase()
	}

	eventually(t, "starting", func() bool { return phase() == backplanev1.InstancePhase_INSTANCE_PHASE_STARTING })

	if kv, _, _ := c.KV().Get(key, nil); kv != nil {
		info, _, err := c.Session().Info(kv.Session, nil)
		if err != nil || info == nil || info.TTL != "15s" {
			t.Fatalf("session ttl: %+v %v", info, err)
		}
	}

	if services, _ := c.Agent().Services(); services[id.Instance] != nil {
		t.Fatal("in catalog before serving")
	}

	if err := p.Register(ctx); err != nil {
		t.Fatal(err)
	}

	eventually(t, "serving", func() bool { return phase() == backplanev1.InstancePhase_INSTANCE_PHASE_SERVING })
	eventually(t, "in catalog", func() bool { s, _ := c.Agent().Services(); return s[id.Instance] != nil })

	svc, _, err := c.Agent().Service(id.Instance, nil)
	if err != nil || !slices.Equal(svc.Tags, []string{"knob"}) {
		t.Fatalf("service: %+v %v", svc, err)
	}

	// The agent reports interval and timeout outside the api types.
	var checks map[string]struct{ ServiceID, Interval, Timeout string }
	if _, err := c.Raw().Query("/v1/agent/checks", &checks, nil); err != nil {
		t.Fatal(err)
	}

	if ch := checks["service:"+id.Instance]; ch.Interval != "7s" || ch.Timeout != "2s" {
		t.Fatalf("check: %+v", checks)
	}

	var m backplanev1.Manifest

	kv, _, _ := c.KV().Get(manifestKey, nil)
	if err := proto.Unmarshal(kv.Value, &m); err != nil || m.GetSdkVersion() != "old" {
		t.Fatalf("released manifest overwritten: %v %v", &m, err)
	}

	if err := p.Deregister(ctx); err != nil {
		t.Fatal(err)
	}

	if phase() != backplanev1.InstancePhase_INSTANCE_PHASE_STOPPING {
		t.Fatalf("phase after Deregister: %v", phase())
	}

	if s, _ := c.Agent().Services(); s[id.Instance] != nil {
		t.Fatal("still in catalog after Deregister")
	}

	cancel()

	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	waitKey(t, c, key, false)
}
