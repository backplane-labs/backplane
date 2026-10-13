package conformance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/gopherex/ws-proto/wsrpc"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/examples/demo"
)

const replicaVersion = "0.0.0-replicas"

type replicaWorld struct {
	auditSink  *auditCollector
	adminToken string
	*m2
	peers        [2]*m1Proc
	peersEnv     [2][]string
	clients      [2]*wsrpc.ClientConn
	bases        [2]string
	backplaneBin string
	helloBin     string
	helloEnv     []string
	formatter    *m1Proc
	formatterURL string
	temporal     client.Client
}

// TestReplicas runs two real backplanes sharing PostgreSQL, Consul, NATS and
// Temporal. Independent hello and formatter binaries communicate only through
// a saved hook binding, an event rule and a reactor. This test uses direct listeners and
// the named example streams, so run it separately with make test-replicas.
//
//nolint:paralleltest // owns shared example names and service identities
func TestReplicas(t *testing.T) {
	w := newReplicaWorld(t)
	ctx := t.Context()

	id, err := demo.Install(ctx, demo.Unary(w.clients[0]))
	if err != nil {
		t.Fatal(err)
	}

	w.ruleID = id
	// The same seed command updates versions instead of creating duplicate rules.
	again, err := demo.Install(ctx, demo.Unary(w.clients[1]))
	if err != nil || again != id {
		t.Fatalf("repeat setup: %q %v", again, err)
	}

	w.converged(t)
	w.schedules(t)

	if os.Getenv("BACKPLANE_TEST_BROWSER") == "1" {
		w.browserSchedules(t)
	}

	w.awaitHook(t, "Welcome, probe!")
	w.greeting(t, "both", "Welcome, both!", 1)
	first := w.greetedOf(t, "both")
	w.ranOnce(t, first.ceID, m2Wait)
	seq := w.republish(t, first.seq)
	w.settled(t, seq)
	w.runsAre(t, "both replicas deduplicate the same ce-id", "rule/"+id+"/"+first.ceID)

	// Both reconcilers converge on the same revision after direct KV damage.
	w.configure(t, "Greetings")
	w.repair(t, "Greetings")
	w.awaitHook(t, "Greetings, probe!")
	w.greeting(t, "configured", "Greetings, configured!", 2)

	// Kill one replica without cleanup: the other retains Nexus and consumers.
	w.crash(t, 0)
	w.cc = w.clients[1]
	w.repair(t, "Greetings")
	w.awaitHook(t, "Greetings, probe!")
	w.greeting(t, "survivor", "Greetings, survivor!", 3)

	// The returning replica joins without duplicating rule runs or endpoints.
	w.peers[0] = m1Start(t, "backplane-1-restarted", w.backplaneBin, w.peersEnv[0]...)
	m1Until(t, m1Wait, "restarted replica ready", func() (bool, string) { return m1Ready(w.peers[0], w.peersEnv[0]) })
	w.converged(t)
	w.greeting(t, "rejoined", "Greetings, rejoined!", 4)

	// A stopped service's manifest remains, but both replicas retire its
	// endpoint. Restarting the same binary recreates the route and the binding.
	w.hello.stop(t)
	w.endpoint(t, false)

	pair, _, err := w.consul.KV().Get("backplane/services/hello/manifests/9.9.9", nil)
	if err != nil || pair == nil {
		t.Fatalf("manifest unexpectedly removed: %v", err)
	}

	w.hello = m1Start(t, "hello-returned", w.helloBin, w.helloEnv...)
	m1Until(t, m1Wait, "returned hello ready", func() (bool, string) { return m1Ready(w.hello, w.helloEnv) })
	w.endpoint(t, true)
	w.awaitHook(t, "Greetings, probe!")
	w.greeting(t, "returned", "Greetings, returned!", 5)

	runs, err := w.ruleRuns(t, false)
	if err != nil || len(runs) != 5 {
		t.Fatalf("expected five rule executions: %s %v", runIDs(runs), err)
	}

	for name, rs := range runs {
		if len(rs) != 1 {
			t.Errorf("duplicate workflow executions for %s: %v", name, rs)
		}
	}

	w.auditDelivered(t)

	t.Log("two replicas: config repair, cross-service binding, rule deduplication, crash/rejoin and endpoint retirement passed")
}

func newReplicaWorld(t *testing.T) *replicaWorld {
	t.Helper()
	addr, dsn, natsURL, temporalAddr := m2Env(t)

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"hello", "formatter", "backplane"} {
		entries, _, err := c.Health().Service(name, "", false, nil)
		if err != nil || len(entries) != 0 {
			t.Fatalf("test needs no running %s: %d entries, %v", name, len(entries), err)
		}
	}

	scratch := m1Database(t, dsn)
	wipe := func() {
		for _, name := range []string{"hello", "formatter"} {
			_, _ = c.KV().DeleteTree("backplane/services/"+name+"/", nil)
			_, _ = c.KV().DeleteTree("config/"+name+"/", nil)
		}

		for _, id := range []string{"replica-1", "replica-2"} {
			_, _ = c.KV().Delete(m1InstancesPrefix("backplane")+id, nil)
		}

		_, _ = c.KV().Delete("backplane/services/backplane/manifests/"+replicaVersion, nil)
	}
	wipe()
	t.Cleanup(wipe)
	w := &replicaWorld{auditSink: newAuditCollector(t), m2: &m2{consul: c, jet: m2JetStream(t, natsURL)}}

	w.temporal, err = client.DialContext(t.Context(), client.Options{HostPort: temporalAddr})
	if err != nil {
		t.Fatal(err)
	}
	// Registered before processes so cleanup runs after all reconciliers stop.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		res, err := w.temporal.OperatorService().ListNexusEndpoints(ctx, &operatorservice.ListNexusEndpointsRequest{Name: "hello", PageSize: 1})
		if err == nil {
			for _, e := range res.GetEndpoints() {
				_, _ = w.temporal.OperatorService().DeleteNexusEndpoint(ctx, &operatorservice.DeleteNexusEndpointRequest{Id: e.GetId(), Version: e.GetVersion()})
			}
		}

		w.temporal.Close()
		_ = w.jet.DeleteStream(ctx, "bp_formatter")
		_ = w.jet.DeleteStream(ctx, "bp_dlq_formatter")
	})
	_ = w.jet.DeleteStream(t.Context(), "bp_formatter")
	_ = w.jet.DeleteStream(t.Context(), "bp_dlq_formatter")
	dir := t.TempDir()
	w.backplaneBin = m1Build(t, dir, "backplane", "../cmd/backplane", replicaVersion)
	w.helloBin = m1Build(t, dir, "hello", "../examples/hello/cmd/hello", "9.9.9")
	formatterBin := m1Build(t, dir, "formatter", "../examples/formatter/cmd/formatter", "9.9.9")
	host, secret, token := advertise(t), m1Random(t), m1Random(t)
	w.adminToken = token
	common := []string{
		"BACKPLANE_CONSUL_ADDR=" + addr, "BACKPLANE_NATS_URL=" + natsURL, "BACKPLANE_TEMPORAL_ADDR=" + temporalAddr,
		"BACKPLANE_ADVERTISE=" + host, "BACKPLANE_INTERNAL_SECRET=" + secret,
		"BACKPLANE_SHUTDOWN_DRAIN=100ms", "BACKPLANE_CONSUL_CHECK_INTERVAL=1s",
	}

	for i := range 2 {
		consolePort := freePort(t)

		env := append([]string(nil), common...)
		env = append(env, "BACKPLANE_INSTANCE="+fmt.Sprintf("replica-%d", i+1),
			"BACKPLANE_INTERNAL_PORT="+freePort(t), "BACKPLANE_PUBLIC_PORT="+freePort(t),
			"BACKPLANE_PG_DSN="+scratch, "BACKPLANE_VALKEY_ADDR="+testValkey(t), "BACKPLANE_ADMIN_TOKEN="+token,
			"BACKPLANE_CONSOLE_LISTEN=:"+consolePort, "BACKPLANE_AUDIT_LISTEN=:"+freePort(t),
			"BACKPLANE_CONSOLE_PREFIX="+m1Prefix, "BACKPLANE_CONSOLE_INSECURE_COOKIE=true",
			"BACKPLANE_LIVE_CONFIG_RECONCILE_INTERVAL=200ms", "BACKPLANE_NEXUS_RECONCILE_INTERVAL=200ms",
			"BACKPLANE_NEXUS_ABSENCE_GRACE=1s", "BACKPLANE_AUDIT_EXPORT_URL="+w.auditSink.url)

		if os.Getenv("BACKPLANE_TEST_BROWSER") == "1" {
			assets, err := filepath.Abs("../web/apps/console/dist")
			if err != nil {
				t.Fatal(err)
			}

			env = append(env, "BACKPLANE_CONSOLE_ASSETS_DIR="+assets)
		}

		w.peersEnv[i] = env
		w.bases[i] = "http://localhost:" + consolePort
		w.peers[i] = m1Start(t, fmt.Sprintf("backplane-%d", i+1), w.backplaneBin, env...)
	}

	w.backplane = w.peers[0]

	helloPort := freePort(t)
	w.helloURL = "http://127.0.0.1:" + helloPort
	w.helloEnv = append(append([]string(nil), common...), "BACKPLANE_INSTANCE=replicas-hello", "HELLO_LEGACY_LISTEN=:"+freePort(t), "BACKPLANE_INTERNAL_PORT="+freePort(t), "BACKPLANE_PUBLIC_PORT="+helloPort)
	w.hello = m1Start(t, "hello", w.helloBin, w.helloEnv...)
	formatterPort := freePort(t)
	formatterEnv := append(append([]string(nil), common...), "BACKPLANE_INSTANCE=replicas-formatter", "BACKPLANE_INTERNAL_PORT="+freePort(t), "BACKPLANE_PUBLIC_PORT="+formatterPort)
	w.formatter = m1Start(t, "formatter", formatterBin, formatterEnv...)
	w.formatterURL = "http://localhost:" + formatterPort + "/formatter/"

	for i := range 2 {
		m1Until(t, m1Wait, "backplane ready", func() (bool, string) { return m1Ready(w.peers[i], w.peersEnv[i]) })

		w.clients[i], err = demo.Connect(t.Context(), w.bases[i]+m1Prefix, token)
		if err != nil {
			t.Fatal(err)
		}

		conn := w.clients[i]

		t.Cleanup(func() { _ = conn.Close() })
	}

	w.cc = w.clients[0]

	m1Until(t, m1Wait, "hello ready", func() (bool, string) { return m1Ready(w.hello, w.helloEnv) })
	m1Until(t, m1Wait, "formatter ready", func() (bool, string) { return m1Ready(w.formatter, formatterEnv) })

	for _, cc := range w.clients {
		m1Until(t, m1Wait, "both services discovered", func() (bool, string) {
			var res consolev1.ListServicesResponse
			if err := m1Call(t.Context(), cc, "/backplane.console.v1.CatalogService/ListServices", &consolev1.ListServicesRequest{}, &res); err != nil {
				return false, err.Error()
			}

			seen := 0

			for _, s := range res.GetServices() {
				if (s.GetName() == "hello" || s.GetName() == "formatter") && s.GetHealthy() > 0 {
					seen++
				}
			}

			return seen == 2, res.String()
		})
	}

	return w
}

func (w *replicaWorld) converged(t *testing.T) {
	t.Helper()

	for _, peer := range w.peers {
		m1Until(t, m2Wait, peer.name+" consumes the shared rule and serves Nexus", func() (bool, string) {
			return peer.logged("rule consuming", w.ruleID) > 0 && peer.logged("nexus worker serves hooks", "hello") > 0, peer.name
		})
	}
}

func (w *replicaWorld) awaitHook(t *testing.T, want string) {
	t.Helper()
	m1Until(t, m2Wait, "hook answers "+want, func() (bool, string) {
		res, err := w.callHook(t, "probe")
		if err != nil {
			return false, err.Error()
		}

		return res.GetError() == "" && jsonField(res.GetOutput(), "text") == want, res.String()
	})
}

func (w *replicaWorld) greeting(t *testing.T, name, want string, count uint64) {
	t.Helper()
	// Wait for the route without emitting a greeting.
	m1Until(t, m1Wait, "formatter serves HTTP", func() (bool, string) {
		code, body := httpGet(w.formatterURL)
		return code == http.StatusOK, body
	})
	m1Until(t, m1Wait, "hello serves its public port", func() (bool, string) {
		code, body := httpGet(w.helloURL + "/ws/")
		return code == http.StatusBadRequest || code == http.StatusUpgradeRequired, fmt.Sprintf("%d %s", code, body)
	})
	w.greetOnce(t, name, want, nil)

	event := w.greetedOf(t, name)
	if jsonField(event.data, "text") != want {
		t.Fatalf("bound greeting payload: %s", event.data)
	}

	w.ranOnce(t, event.ceID, m2Wait)
	m1Until(t, m2Wait, "formatter rule and independent reactor received "+name, func() (bool, string) {
		code, body := httpGet(w.formatterURL)

		var stats struct {
			Recorded uint64 `json:"recorded"`
			Observed uint64 `json:"observed"`
		}

		err := json.Unmarshal([]byte(body), &stats)

		return code == http.StatusOK && err == nil && stats.Recorded == count && stats.Observed == count, body
	})
}

func (w *replicaWorld) configure(t *testing.T, prefix string) {
	t.Helper()

	var res consolev1.SaveRevisionResponse
	w.call(t, "/backplane.console.v1.ConfigService/SaveRevision", &consolev1.SaveRevisionRequest{Service: "formatter", Values: map[string]string{"formatter.prefix": fmt.Sprintf("%q", prefix)}}, &res)

	if len(res.GetViolations()) != 0 || res.GetRevision() == nil {
		t.Fatalf("save config: %v", &res)
	}
}

func (w *replicaWorld) repair(t *testing.T, prefix string) {
	t.Helper()

	if _, err := w.consul.KV().DeleteTree("config/formatter/", nil); err != nil {
		t.Fatal(err)
	}

	m1Until(t, m1Wait, "reconciler restores formatter config", func() (bool, string) {
		p, _, err := w.consul.KV().Get("config/formatter/formatter/prefix", nil)
		if err != nil {
			return false, err.Error()
		}

		if p == nil {
			return false, "missing"
		}

		return string(p.Value) == prefix, string(p.Value)
	})
}

func (w *replicaWorld) crash(t *testing.T, index int) {
	t.Helper()

	p := w.peers[index]
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	_ = p.cmd.Wait()

	p.done = true
	if strings.Contains(p.logs.String(), "DATA RACE") {
		t.Fatal("replica reported a data race")
	}
}

func (w *replicaWorld) endpoint(t *testing.T, exists bool) {
	t.Helper()
	m1Until(t, m2Wait, fmt.Sprintf("hello endpoint exists=%v", exists), func() (bool, string) {
		res, err := w.temporal.OperatorService().ListNexusEndpoints(t.Context(), &operatorservice.ListNexusEndpointsRequest{Name: "hello", PageSize: 1})
		if err != nil {
			return false, err.Error()
		}

		return (len(res.GetEndpoints()) == 1) == exists, res.String()
	})
}
