package conformance_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ws-proto/wsrpc"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/examples/demo"
)

const replicaVersion = "0.0.0-replicas"

type replicaWorld struct {
	auditSink    *auditCollector
	consul       *api.Client
	cc           *wsrpc.ClientConn
	hello        *m1Proc
	peers        [2]*m1Proc
	peersEnv     [2][]string
	clients      [2]*wsrpc.ClientConn
	bases        [2]string
	backplaneBin string
	helloBin     string
	helloEnv     []string
	formatter    *m1Proc
}

// TestReplicas verifies config repair, service discovery and durable audit across a crash and rejoin.
//
//nolint:paralleltest // owns shared example names and service identities
func TestReplicas(t *testing.T) {
	w := newReplicaWorld(t)
	// Both reconcilers converge on the same revision after direct KV damage.
	w.configure(t, "Greetings")
	w.repair(t, "Greetings")
	w.crash(t, 0)
	w.cc = w.clients[1]
	w.repair(t, "Greetings")
	w.peers[0] = m1Start(t, "backplane-1-restarted", w.backplaneBin, w.peersEnv[0]...)
	m1Until(t, m1Wait, "restarted replica ready", func() (bool, string) { return m1Ready(w.peers[0], w.peersEnv[0]) })
	w.hello.stop(t)

	pair, _, err := w.consul.KV().Get("backplane/services/hello/manifests/9.9.9", nil)
	if err != nil || pair == nil {
		t.Fatalf("manifest unexpectedly removed: %v", err)
	}

	w.hello = m1Start(t, "hello-returned", w.helloBin, w.helloEnv...)
	m1Until(t, m1Wait, "returned hello ready", func() (bool, string) { return m1Ready(w.hello, w.helloEnv) })
	w.auditDelivered(t)
}

func newReplicaWorld(t *testing.T) *replicaWorld {
	t.Helper()
	addr, dsn := m1Env(t)

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
	w := &replicaWorld{auditSink: newAuditCollector(t), consul: c}
	dir := t.TempDir()
	w.backplaneBin = m1Build(t, dir, "backplane", "../cmd/backplane", replicaVersion)
	w.helloBin = m1Build(t, dir, "hello", "../examples/hello/cmd/hello", "9.9.9")
	formatterBin := m1Build(t, dir, "formatter", "../examples/formatter/cmd/formatter", "9.9.9")
	host, secret, token := advertise(t), m1Random(t), m1Random(t)
	common := []string{
		"BACKPLANE_CONSUL_ADDR=" + addr,
		"BACKPLANE_ADVERTISE=" + host, "BACKPLANE_INTERNAL_SECRET=" + secret,
		"BACKPLANE_SHUTDOWN_DRAIN=100ms", "BACKPLANE_CONSUL_CHECK_INTERVAL=1s",
	}

	for i := range 2 {
		consolePort := freePort(t)

		env := append([]string(nil), common...)
		env = append(env, "BACKPLANE_INSTANCE="+fmt.Sprintf("replica-%d", i+1),
			"BACKPLANE_INTERNAL_PORT="+freePort(t), "BACKPLANE_PUBLIC_PORT="+freePort(t),
			"BACKPLANE_PG_DSN="+scratch, "BACKPLANE_ADMIN_TOKEN="+token,
			"BACKPLANE_CONSOLE_LISTEN=:"+consolePort, "BACKPLANE_AUDIT_LISTEN=:"+freePort(t),
			"BACKPLANE_CONSOLE_PREFIX="+m1Prefix, "BACKPLANE_CONSOLE_INSECURE_COOKIE=true",
			"BACKPLANE_LIVE_CONFIG_RECONCILE_INTERVAL=200ms",
			"BACKPLANE_AUDIT_EXPORT_URL="+w.auditSink.url)

		w.peersEnv[i] = env
		w.bases[i] = "http://localhost:" + consolePort
		w.peers[i] = m1Start(t, fmt.Sprintf("backplane-%d", i+1), w.backplaneBin, env...)
	}

	helloPort := freePort(t)
	w.helloEnv = append(append([]string(nil), common...), "BACKPLANE_INSTANCE=replicas-hello", "HELLO_LEGACY_LISTEN=:"+freePort(t), "BACKPLANE_INTERNAL_PORT="+freePort(t), "BACKPLANE_PUBLIC_PORT="+helloPort)
	w.hello = m1Start(t, "hello", w.helloBin, w.helloEnv...)
	formatterPort := freePort(t)
	formatterEnv := append(append([]string(nil), common...), "BACKPLANE_INSTANCE=replicas-formatter", "BACKPLANE_INTERNAL_PORT="+freePort(t), "BACKPLANE_PUBLIC_PORT="+formatterPort)
	w.formatter = m1Start(t, "formatter", formatterBin, formatterEnv...)

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

func (w *replicaWorld) call(t *testing.T, method string, req, res proto.Message) {
	t.Helper()

	if err := m1Call(t.Context(), w.cc, method, req, res); err != nil {
		t.Fatalf("%s(%v): %v", method, req, err)
	}
}
