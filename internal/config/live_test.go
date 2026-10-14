package config_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/config"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	sdkconfig "github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/standard"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

const liveLimit = 20 * time.Second

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	for deadline := time.Now().Add(liveLimit); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if ok() {
			return
		}
	}

	t.Fatalf("%s: timed out", what)
}

func freePort(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	return port
}

// liveEnv is platform-in-a-box: skipped without BACKPLANE_TEST_CONSUL and
// BACKPLANE_TEST_PG.
func liveEnv(t *testing.T) (*api.Client, string, string) {
	t.Helper()

	addr, dsn := os.Getenv("BACKPLANE_TEST_CONSUL"), os.Getenv("BACKPLANE_TEST_PG")
	if addr == "" || dsn == "" {
		t.Skip("BACKPLANE_TEST_CONSUL and BACKPLANE_TEST_PG not set")
	}

	client, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	return client, addr, dsn
}

// backplaneSide is the server's tree as in production: the store, the
// registry over Consul and the Manager.
func backplaneSide(t *testing.T, client *api.Client, dsn string) (*config.Manager, *registry.Registry, *store.Store) {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: sdkconfig.Secret(dsn)}))
	reg := registry.New(h.Root(), client)
	m := config.New(h.Root(), config.Settings{ReconcileInterval: 300 * time.Millisecond}, client, st, reg, config.WaitTime(5*time.Second))

	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	<-reg.Synced()

	return m, reg, st.Get()
}

type sdkState struct{ cfg testConfig }

// runService runs a service with testConfig on the SDK against Consul, not
// registered in the catalog: manifest and instance state in KV, Live
// fields from config/<name>/.
func runService(t *testing.T, addr, name string) testConfig {
	t.Helper()

	env := strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_"

	t.Setenv("BACKPLANE_CONSUL_ADDR", addr)
	t.Setenv("BACKPLANE_CONSUL_REGISTER", "false")
	t.Setenv("BACKPLANE_INTERNAL_PORT", freePort(t))
	t.Setenv("BACKPLANE_PUBLIC_PORT", freePort(t))
	t.Setenv("BACKPLANE_SHUTDOWN_DRAIN", "0s")
	t.Setenv(env+"PASSWORD", "long-secret-password")

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*sdkState, error) {
		return &sdkState{cfg: root.Config()}, nil
	},
		standard.Drivers(), backplane.Name(name), backplane.Instance(name+"-1"), backplane.Advertise("127.0.0.1"),
		backplane.Logger(xlog.NewJSON(xlog.WithWriter(io.Discard))), backplane.ConfigOptions(sdkconfig.WithoutFile()))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		select {
		case <-done:
		case <-time.After(liveLimit):
			t.Error("service did not stop")
		}
	})

	return svc.State().cfg
}

func randomName(t *testing.T) string {
	t.Helper()

	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return "cfgtest-" + hex.EncodeToString(b)
}

// state is the service's only instance as the registry sees it.
func state(reg *registry.Registry, name string) *backplanev1.InstanceState {
	svc := reg.Current().Services[name]
	if len(svc.Instances) != 1 {
		return nil
	}

	return svc.Instances[0].State
}

// The cycle of §5: a save validated on the live instance lands in KV with
// its _revision, the SDK applies it and reports the revision; the
// reconciler repairs a wiped prefix and overwrites a manual edit; a
// rollback is a new revision.
//
//nolint:paralleltest // t.Setenv: the SDK reads the process environment
func TestLiveCycle(t *testing.T) {
	client, addr, dsn := liveEnv(t)
	name := randomName(t)

	// Consul last, once the reconciler has stopped: a pass in flight would
	// write config/<name>/ back.
	t.Cleanup(func() {
		_, _ = client.KV().DeleteTree(config.Prefix(name), nil)
		_, _ = client.KV().DeleteTree(registry.Prefix+name+"/", nil)
	})

	m, reg, st := backplaneSide(t, client, dsn)

	// The rows while the store is open: no pass syncs the service after.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = st.Pool.Exec(ctx, "DELETE FROM backplane.config_current WHERE service = $1", name)
		_, _ = st.Pool.Exec(ctx, "DELETE FROM backplane.config_revision WHERE service = $1", name)
	})

	cfg := runService(t, addr, name)

	eventually(t, "instance state in the registry", func() bool {
		svc := reg.Current().Services[name]

		return svc.Latest() != nil && state(reg, name) != nil && len(state(reg, name).GetConfig()) > 0
	})

	liveRejects(t, m, name)
	liveSaves(t, m, reg, cfg, name)
	liveRepairs(t, client, cfg, name)
	liveRollback(t, m, reg, client, cfg, name)
	liveAPI(t, m, name)
}

// liveRejects: invalid overrides are not saved; violations name the path
// and the instance.
func liveRejects(t *testing.T, m *config.Manager, name string) {
	t.Helper()

	ctx := t.Context()

	saved, err := m.Save(ctx, name, map[string]string{"limits.max": `200`, "static": `"y"`}, "bad")
	if err != nil || len(saved.Violations) == 0 || saved.Revision.Revision != 0 {
		t.Fatalf("invalid save: %+v %v", saved, err)
	}

	saved, err = m.Save(ctx, name, map[string]string{"limits.min": `50`}, "rule")
	if err != nil || !has(saved.Violations, name+"-1", "limits", "RULE_VIOLATED") {
		t.Fatalf("rule: %+v %v", saved, err)
	}

	if _, ok, _ := m.Current(ctx, name); ok {
		t.Fatal("a rejected override was saved")
	}
}

// liveSaves: revisions 1 and 2 reach the instance, which reports them.
func liveSaves(t *testing.T, m *config.Manager, reg *registry.Registry, cfg testConfig, name string) {
	t.Helper()

	ctx := t.Context()

	saved, err := m.Save(ctx, name, map[string]string{"suffix": `"?"`}, "first")
	if err != nil || len(saved.Violations) > 0 || saved.DeliveryErr != nil || saved.Revision.Revision != 1 ||
		saved.Revision.Author != config.DefaultAuthor {
		t.Fatalf("save: %+v %v", saved, err)
	}

	eventually(t, "revision 1 applied", func() bool {
		st := state(reg, name)

		return cfg.Suffix.Get() == "?" && st.GetConfigRevision() == 1 &&
			st.GetSources()["suffix"] == backplanev1.ConfigSource_CONFIG_SOURCE_KV
	})

	// Several kinds at once.
	saved, err = m.Save(ctx, name, map[string]string{
		"suffix": `"!!"`, "limits.max": `20`, "timeout": `"2s"`, "tags": `["a","b"]`,
	}, "second")
	if err != nil || len(saved.Violations) > 0 || saved.Revision.Revision != 2 {
		t.Fatalf("save 2: %+v %v", saved, err)
	}

	eventually(t, "revision 2 applied", func() bool {
		return cfg.Suffix.Get() == "!!" && cfg.Limits.Max.Get() == 20 && cfg.Timeout.Get() == 2*time.Second &&
			len(cfg.Tags.Get()) == 2 && state(reg, name).GetConfigRevision() == 2
	})
}

func kvValue(client *api.Client, name, key string) string {
	p, _, err := client.KV().Get(config.Prefix(name)+key, nil)
	if err != nil || p == nil {
		return ""
	}

	return string(p.Value)
}

// liveRepairs: the reconciler restores a wiped prefix and overwrites
// manual edits, and so does the instance.
func liveRepairs(t *testing.T, client *api.Client, cfg testConfig, name string) {
	t.Helper()

	if _, err := client.KV().DeleteTree(config.Prefix(name), nil); err != nil {
		t.Fatal(err)
	}

	eventually(t, "wiped kv restored", func() bool {
		return kvValue(client, name, config.RevisionKey) == "2" && kvValue(client, name, "suffix") == "!!" &&
			kvValue(client, name, "limits/max") == "20"
	})

	// A changed value and a key nobody saved.
	for key, value := range map[string]string{"suffix": "hacked", "limits/min": "3"} {
		if _, err := client.KV().Put(&api.KVPair{Key: config.Prefix(name) + key, Value: []byte(value)}, nil); err != nil {
			t.Fatal(err)
		}
	}

	eventually(t, "manual edit overwritten", func() bool {
		return kvValue(client, name, "suffix") == "!!" && kvValue(client, name, "limits/min") == "" &&
			cfg.Suffix.Get() == "!!" && cfg.Limits.Min.Get() == 1
	})
}

// liveRollback: rolling back to 1 is revision 3; paths 1 did not set fall
// back to the instance's own layers.
func liveRollback(
	t *testing.T, m *config.Manager, reg *registry.Registry, client *api.Client, cfg testConfig, name string,
) {
	t.Helper()

	saved, err := m.Rollback(t.Context(), name, 1, "back")
	if err != nil || saved.Revision.Revision != 3 || saved.Revision.RollbackOf != 1 ||
		string(saved.Revision.Values["suffix"]) != `"?"` || len(saved.Revision.Values) != 1 {
		t.Fatalf("rollback: %+v %v", saved, err)
	}

	eventually(t, "rollback applied", func() bool {
		return cfg.Suffix.Get() == "?" && cfg.Limits.Max.Get() == 10 && cfg.Timeout.Get() == 5*time.Second &&
			state(reg, name).GetConfigRevision() == 3 && kvValue(client, name, "limits/max") == ""
	})

	if _, err := m.Rollback(t.Context(), name, 99, ""); err == nil {
		t.Fatal("rollback to a missing revision")
	}
}

// liveAPI reads the result through the console API.
func liveAPI(t *testing.T, m *config.Manager, name string) {
	t.Helper()

	srv := m.API()
	ctx := t.Context()

	got, err := srv.GetConfig(ctx, &consolev1.GetConfigRequest{Service: name})
	if err != nil {
		t.Fatal(err)
	}

	c := got.GetConfig()
	if c.GetCurrent().GetRevision() != 3 || c.GetCurrent().GetRollbackOf() != 1 || c.GetSchema() == nil ||
		len(c.GetLive()) == 0 || len(c.GetInstances()) != 1 {
		t.Fatalf("config: %v", c)
	}

	in := c.GetInstances()[0]
	if in.GetAppliedRevision() != 3 {
		t.Fatalf("instance: %v", in)
	}

	for _, value := range in.GetLive() {
		switch value.GetPath() {
		case "suffix":
			if value.GetValue() != `"?"` || value.GetSource() != backplanev1.ConfigSource_CONFIG_SOURCE_KV {
				t.Fatalf("suffix: %v", value)
			}
		case "limits.max":
			if value.GetValue() != "10" || value.GetSource() != backplanev1.ConfigSource_CONFIG_SOURCE_DEFAULT {
				t.Fatalf("limits.max: %v", value)
			}
		}
	}

	page, err := srv.ListRevisions(ctx, &consolev1.ListRevisionsRequest{Service: name, PageSize: 2})
	if err != nil || len(page.GetRevisions()) != 2 || page.GetRevisions()[0].GetRevision() != 3 || page.GetNextBefore() != 2 {
		t.Fatalf("page 1: %v %v", page, err)
	}

	page, err = srv.ListRevisions(ctx, &consolev1.ListRevisionsRequest{Service: name, PageSize: 2, Before: 2})
	if err != nil || len(page.GetRevisions()) != 1 || page.GetRevisions()[0].GetRevision() != 1 || page.GetNextBefore() != 0 {
		t.Fatalf("page 2: %v %v", page, err)
	}

	v, err := srv.ValidateOverride(ctx, &consolev1.ValidateOverrideRequest{Service: name, Values: map[string]string{"limits.max": `101`}})
	if err != nil || len(v.GetViolations()) != 1 || v.GetViolations()[0].GetInstance() != name+"-1" {
		t.Fatalf("validate: %v %v", v, err)
	}

	res, err := srv.SaveRevision(ctx, &consolev1.SaveRevisionRequest{Service: name, Values: map[string]string{"nope": `1`}})
	if err != nil || res.GetRevision() != nil || len(res.GetViolations()) != 1 {
		t.Fatalf("save rejected: %v %v", res, err)
	}
}

// replica is one more backplane over the same store, reading src.
func replica(t *testing.T, client *api.Client, dsn string, src registry.Source) (*config.Manager, *store.Store) {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: sdkconfig.Secret(dsn)}))
	m := config.New(h.Root(), config.Settings{ReconcileInterval: 200 * time.Millisecond}, client, st, src, config.WaitTime(5*time.Second))

	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return m, st.Get()
}

// Two replicas: a save on one reaches the other's WatchConfig through KV,
// and their reconcilers agree — KV is written once, not fought over.
func TestLiveReplicas(t *testing.T) {
	t.Parallel()

	client, _, dsn := liveEnv(t)
	name := randomName(t)

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{name: {
		Name: name, Manifests: map[string]*backplanev1.Manifest{"1.0.0": testManifest(t, name, "1.0.0")},
	}})

	// Consul last, once both reconcilers have stopped (see TestLiveCycle).
	t.Cleanup(func() { _, _ = client.KV().DeleteTree(config.Prefix(name), nil) })

	one, st := replica(t, client, dsn, hub)
	two, _ := replica(t, client, dsn, hub)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = st.Pool.Exec(ctx, "DELETE FROM backplane.config_current WHERE service = $1", name)
		_, _ = st.Pool.Exec(ctx, "DELETE FROM backplane.config_revision WHERE service = $1", name)
	})

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := grpc.NewServer()
	two.Register(srv)

	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(srv.Stop)

	cc, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = cc.Close() })

	stream, err := consolev1.NewConfigServiceClient(cc).WatchConfig(t.Context(), &consolev1.WatchConfigRequest{Service: name})
	if err != nil {
		t.Fatal(err)
	}

	first, err := stream.Recv()
	if err != nil || first.GetConfig().GetCurrent() != nil || len(first.GetConfig().GetLive()) == 0 {
		t.Fatalf("first: %v %v", first, err)
	}

	// No live instance: validated on the schema defaults.
	saved, err := one.Save(t.Context(), name, map[string]string{"limits.max": `0`}, "")
	if err != nil || len(saved.Violations) != 1 || saved.Violations[0].Instance != "" {
		t.Fatalf("defaults: %+v %v", saved, err)
	}

	saved, err = one.Save(t.Context(), name, map[string]string{"suffix": `"?"`}, "from one")
	if err != nil || saved.Revision.Revision != 1 {
		t.Fatalf("save: %+v %v", saved, err)
	}

	next, err := stream.Recv()
	if err != nil || next.GetConfig().GetCurrent().GetRevision() != 1 ||
		next.GetConfig().GetCurrent().GetValues()["suffix"] != `"?"` {
		t.Fatalf("watch on the other replica: %v %v", next, err)
	}

	rev, _, err := client.KV().Get(config.Prefix(name)+config.RevisionKey, nil)
	if err != nil || rev == nil || string(rev.Value) != "1" {
		t.Fatalf("_revision: %v %v", rev, err)
	}

	time.Sleep(time.Second) // several passes of both reconcilers

	again, _, err := client.KV().Get(config.Prefix(name)+config.RevisionKey, nil)
	if err != nil || again == nil || again.ModifyIndex != rev.ModifyIndex {
		t.Fatalf("replicas rewrote a synced kv: %v -> %v", rev, again)
	}

	if one.Err() != nil || two.Err() != nil {
		t.Fatalf("reconcile errors: %v %v", one.Err(), two.Err())
	}
}
