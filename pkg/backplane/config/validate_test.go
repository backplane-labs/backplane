package config_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	consuldriver "github.com/backplane-labs/backplane/pkg/backplane/drivers/consul"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
)

type Limits struct {
	Max   config.Live[int] `json:"max"   schemapb:"default=10"`
	Burst int              `json:"burst" schemapb:"default=1"`
}

// Pointer receiver on a section.
func (l *Limits) Validate() error {
	if l.Max.Get() <= 0 {
		return errors.New("max must be positive")
	}

	return nil
}

type Names struct {
	Prefix config.Live[string] `json:"prefix" schemapb:"default=a"`
}

// Value receiver on a section.
func (n Names) Validate() error {
	if strings.Contains(n.Prefix.Get(), " ") {
		return errors.New("prefix has a space")
	}

	return nil
}

type ValidatedConfig struct {
	config.Backplane `json:"backplane"`

	Limits Limits `json:"limits"`
	Names  *Names `json:"names"`
}

// Value receiver on the whole configuration: across sections.
func (c ValidatedConfig) Validate() error {
	if c.Limits.Burst > c.Limits.Max.Get() {
		return errors.New("burst above max")
	}

	return nil
}

func TestValidateAtOpen(t *testing.T) {
	t.Setenv("BACKPLANE_CONSUL_ADDR", "")

	opts := []config.Option{consuldriver.Config(), config.Service("validated"), config.WithoutFile()}

	rt, err := config.Open[ValidatedConfig](t.Context(), opts...)
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	_ = rt.Close()

	for env, want := range map[string]string{
		"VALIDATED_LIMITS_MAX":   "limits: max must be positive",
		"VALIDATED_NAMES_PREFIX": "names: prefix has a space",
		"VALIDATED_LIMITS_BURST": "burst above max",
	} {
		t.Run(env, func(t *testing.T) {
			value := map[string]string{
				"VALIDATED_LIMITS_MAX": "0", "VALIDATED_NAMES_PREFIX": "a b", "VALIDATED_LIMITS_BURST": "11",
			}[env]
			t.Setenv(env, value)

			_, err := config.Open[ValidatedConfig](t.Context(), opts...)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
}

// safeBuf collects log output across goroutines.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *safeBuf) has(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return strings.Contains(b.buf.String(), s)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("not reached: %s", what)
}

// The Consul layer going down and coming back is logged once each way.
func TestConsulDegradeRecoverLogged(t *testing.T) {
	var down atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusInternalServerError)

			return
		}

		if r.URL.Query().Get("index") != "" {
			time.Sleep(20 * time.Millisecond) // a short blocking query
		}

		w.Header().Set("X-Consul-Index", "1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"Key":"config/degrade/_revision","Value":"Mw=="}]`)) // "3"
	}))
	defer srv.Close()

	t.Setenv("BACKPLANE_CONSUL_ADDR", srv.Listener.Addr().String())

	rt, err := config.Open[ValidatedConfig](t.Context(), consuldriver.Config(), config.Service("degrade"), config.WithoutFile(),
		config.ConsulBackoff(10*time.Millisecond, 50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	state := link.ConfigState(rt)
	logs := &safeBuf{}
	state.SetLog(xlog.NewJSON(xlog.WithWriter(logs)))

	if rev := state.Effective().Revision; rev != 3 {
		t.Fatalf("revision: %d", rev)
	}

	down.Store(true)
	waitFor(t, "degraded logged", func() bool { return logs.has("consul layer unavailable") })

	if rev := state.Effective().Revision; rev != 3 {
		t.Fatalf("revision while degraded: %d", rev)
	}

	down.Store(false)
	waitFor(t, "recovery logged", func() bool { return logs.has("consul layer recovered") })

	if rt.Degraded() != nil {
		t.Fatalf("still degraded: %v", rt.Degraded())
	}
}

// Runs against a real Consul: the applied revision is the one read with the
// values; a rejected update keeps the values and reports its revision.
func TestRevisionAppliedAndRejected(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	client, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	const prefix = "config/revision-test/"

	t.Cleanup(func() { _, _ = client.KV().DeleteTree(prefix, nil) })

	_, _ = client.KV().DeleteTree(prefix, nil)

	// As the console writes: values and _revision in one transaction.
	commit := func(rev string, kv map[string]string) {
		t.Helper()

		ops := make(api.KVTxnOps, 0, 1+len(kv))
		ops = append(ops, &api.KVTxnOp{Verb: api.KVSet, Key: prefix + "_revision", Value: []byte(rev)})

		for k, v := range kv {
			ops = append(ops, &api.KVTxnOp{Verb: api.KVSet, Key: prefix + k, Value: []byte(v)})
		}

		ok, resp, _, err := client.KV().Txn(ops, nil)
		if err != nil || !ok {
			t.Fatalf("txn: %v %v", err, resp)
		}
	}

	commit("5", map[string]string{"limits/max": "20"})
	t.Setenv("BACKPLANE_CONSUL_ADDR", addr)

	rt, err := config.Open[ValidatedConfig](context.Background(), consuldriver.Config(), config.Service("revision-test"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	state := link.ConfigState(rt)
	logs := &safeBuf{}
	state.SetLog(xlog.NewJSON(xlog.WithWriter(logs)))

	if eff := state.Effective(); eff.Revision != 5 || eff.Err != nil || rt.Value().Limits.Max.Get() != 20 {
		t.Fatalf("open: rev %d err %v max %d", eff.Revision, eff.Err, rt.Value().Limits.Max.Get())
	}

	// Rejected by the author's Validate.
	commit("6", map[string]string{"limits/max": "-1"})
	waitFor(t, "validate rejection", func() bool { return state.Effective().RejectedRevision == 6 })

	eff := state.Effective()
	if eff.Revision != 5 || eff.Err == nil || !strings.Contains(eff.Err.Error(), "max must be positive") ||
		rt.Value().Limits.Max.Get() != 20 || !strings.Contains(string(eff.Values), `"max":20`) {
		t.Fatalf("after reject: rev %d err %v max %d values %s", eff.Revision, eff.Err, rt.Value().Limits.Max.Get(),
			eff.Values)
	}

	waitFor(t, "rejection logged", func() bool { return logs.has("config update rejected") })

	// Rejected by the schema: not an int.
	commit("7", map[string]string{"limits/max": "many"})
	waitFor(t, "schema rejection", func() bool { return state.Effective().RejectedRevision == 7 })

	if got := state.Effective(); got.Revision != 5 || rt.Value().Limits.Max.Get() != 20 {
		t.Fatalf("after schema reject: rev %d max %d", got.Revision, rt.Value().Limits.Max.Get())
	}

	// A valid update clears the rejection.
	commit("8", map[string]string{"limits/max": "30"})
	waitFor(t, "applied", func() bool { return state.Effective().Revision == 8 })

	if got := state.Effective(); got.Err != nil || got.RejectedRevision != 0 || rt.Value().Limits.Max.Get() != 30 {
		t.Fatalf("after apply: err %v rejected %d max %d", got.Err, got.RejectedRevision, rt.Value().Limits.Max.Get())
	}

	// Only the revision changes: the new revision is reported as applied.
	commit("9", nil)
	waitFor(t, "revision-only update", func() bool { return state.Effective().Revision == 9 })
}
