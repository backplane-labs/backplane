package config_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/xconf"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	consuldriver "github.com/backplane-labs/backplane/pkg/backplane/drivers/consul"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
)

type Postgres struct {
	DSN      config.Secret       `json:"dsn"`
	LogLevel config.Live[string] `json:"log_level" schemapb:"default=warn"`
	Timeout  time.Duration       `json:"timeout"   schemapb:"default=5s"`
}

type Config struct {
	config.Backplane `json:"backplane"`

	Postgres Postgres            `json:"postgres"`
	Salute   string              `json:"salute"   schemapb:"default=Hello"`
	Suffix   config.Live[string] `json:"suffix"   schemapb:"default=!"`
}

func write(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "hello.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoadLayers(t *testing.T) {
	path := write(t, "postgres:\n  dsn: postgres://x\n  log_level: info\nsalute: Hi\n")
	t.Setenv("HELLO_SUFFIX", "?")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "consul:8500")

	cfg, err := config.Load[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.File(path))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Postgres.DSN.Reveal() != "postgres://x" || cfg.Postgres.LogLevel.Get() != "info" || cfg.Salute != "Hi" {
		t.Fatalf("file layer: %+v", cfg)
	}

	if cfg.Suffix.Get() != "?" || cfg.Postgres.Timeout != 5*time.Second {
		t.Fatalf("env/defaults: %q %v", cfg.Suffix.Get(), cfg.Postgres.Timeout)
	}

	if cfg.BackplaneConfig().Consul.Addr != "consul:8500" || cfg.BackplaneConfig().InternalPort != 9400 {
		t.Fatalf("block: %+v", cfg.BackplaneConfig())
	}
}

func TestTemporalWorkerFromEnv(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")

	cfg, err := config.Load[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}

	if w := cfg.BackplaneConfig().Temporal.Worker; !w.Enabled || w.MaxConcurrentActivities != 0 || w.WorkflowPollers != 0 {
		t.Fatalf("defaults: %+v", w)
	}

	t.Setenv("BACKPLANE_TEMPORAL_WORKER_ENABLED", "false")
	t.Setenv("BACKPLANE_TEMPORAL_WORKER_MAX_CONCURRENT_ACTIVITIES", "8")
	t.Setenv("BACKPLANE_TEMPORAL_WORKER_MAX_CONCURRENT_WORKFLOW_TASKS", "4")
	t.Setenv("BACKPLANE_TEMPORAL_WORKER_ACTIVITY_POLLERS", "3")
	t.Setenv("BACKPLANE_TEMPORAL_WORKER_WORKFLOW_POLLERS", "2")

	cfg, err = config.Load[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}

	if w := cfg.BackplaneConfig().Temporal.Worker; w.Enabled || w.MaxConcurrentActivities != 8 ||
		w.MaxConcurrentWorkflowTasks != 4 || w.ActivityPollers != 3 || w.WorkflowPollers != 2 {
		t.Fatalf("env: %+v", w)
	}

	t.Setenv("BACKPLANE_TEMPORAL_WORKER_ACTIVITY_POLLERS", "-1")

	if _, err := config.Load[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.WithoutFile()); err == nil {
		t.Fatal("negative pollers accepted")
	}
}

func TestLivePathsFromSchema(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")

	rt, err := config.Open[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	got := fmt.Sprint(link.ConfigState(rt).LivePaths())
	if got != "[backplane.log_level postgres.log_level suffix]" {
		t.Fatalf("live paths: %s", got)
	}

	if rt.Value().Suffix.Get() != "!" {
		t.Fatalf("value: %q", rt.Value().Suffix.Get())
	}
}

func TestOpenWithoutReachableConsul(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "127.0.0.1:1")

	rt, err := config.Open[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatalf("must open without consul: %v", err)
	}
	defer rt.Close()

	if rt.Degraded() == nil {
		t.Fatal("expected degraded")
	}
}

// Runs against a real Consul: BACKPLANE_TEST_CONSUL=localhost:8500 (make up).
func TestLiveReloadFromConsul(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	client, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = client.KV().DeleteTree("config/live-test/", nil) })
	t.Setenv("LIVE_TEST_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", addr)

	// The runtime outlives the context that bounded loading.
	openCtx, cancelOpen := context.WithCancel(context.Background())

	rt, err := config.Open[Config](openCtx, consuldriver.Config(), config.Service("live-test"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	cancelOpen()

	section := &rt.Value().Postgres // a component holding its section sees live changes
	applied := make(chan string, 4)

	section.LogLevel.Watch(func(v string) { applied <- v })

	put := func(key, value string) {
		if _, err := client.KV().Put(&api.KVPair{Key: "config/live-test/" + key, Value: []byte(value)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	put("salute", "ignored") // static: KV must not apply
	put("postgres/log_level", "debug")

	select {
	case v := <-applied:
		if v != "debug" {
			t.Fatalf("applied %q", v)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no live update")
	}

	if rt.Value().Salute != "Hello" {
		t.Fatalf("static field changed from KV: %q", rt.Value().Salute)
	}

	if src := link.ConfigState(rt).Effective().Sources["postgres.log_level"].String(); src != "CONFIG_SOURCE_KV" {
		t.Fatalf("source: %s", src)
	}
}

type (
	item struct {
		Weight config.Live[int] `json:"weight"`
	}
	nestedItem struct {
		Inner item `json:"inner"`
	}
	liveInSlice struct {
		config.Backplane `json:"backplane"`

		Items []item `json:"items"`
	}
	liveInMap struct {
		config.Backplane `json:"backplane"`

		Items map[string]*nestedItem `json:"items"`
	}
	liveList struct {
		config.Backplane `json:"backplane"`

		Weights []config.Live[int] `json:"weights"`
	}
)

func TestOpenRejectsLiveInCollections(t *testing.T) {
	t.Setenv("BACKPLANE_CONSUL_ADDR", "")

	opts := []config.Option{consuldriver.Config(), config.Service("collections"), config.WithoutFile()}

	check := func(name string, err error) {
		t.Helper()

		if err == nil || !strings.Contains(err.Error(), "lists and maps cannot hold Live fields") {
			t.Errorf("%s: %v", name, err)
		}
	}

	_, err := config.Open[liveInSlice](t.Context(), opts...)
	check("[]struct{Live}", err)

	_, err = config.Open[liveInMap](t.Context(), opts...)
	check("map[string]*struct{struct{Live}}", err)

	_, err = config.Open[liveList](t.Context(), opts...)
	check("[]Live", err)
}

func TestRuntimeCloseIsIdempotent(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "")

	rt, err := config.Open[Config](t.Context(), consuldriver.Config(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}

	if rt.Degraded() != nil {
		t.Fatalf("without consul: degraded %v", rt.Degraded())
	}

	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
}

type GatedConfig struct {
	config.Backplane `json:"backplane"`

	Postgres Postgres            `json:"postgres"`
	Gate     config.Live[string] `json:"gate"` // required, only Consul may hold it
}

func TestOpenWaitsForRequiredLiveWhileConsulDown(t *testing.T) {
	t.Setenv("GATED_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "127.0.0.1:1")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()

	_, err := config.Open[GatedConfig](ctx,
		consuldriver.Config(), config.Service("gated"), config.WithoutFile(), config.ConsulBackoff(10*time.Millisecond, 50*time.Millisecond))

	var degraded *xconf.DegradedError
	if !errors.As(err, &degraded) {
		t.Fatalf("want DegradedError, got %v", err)
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}

	if since := time.Since(start); since < 250*time.Millisecond || since > 3*time.Second {
		t.Fatalf("waited %v", since)
	}
}

func TestOpenFailsFastOnMissingStaticWhileConsulDown(t *testing.T) {
	t.Setenv("BACKPLANE_CONSUL_ADDR", "127.0.0.1:1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()

	if _, err := config.Open[GatedConfig](ctx, consuldriver.Config(), config.Service("gated"), config.WithoutFile()); err == nil {
		t.Fatal("missing postgres.dsn must fail")
	}

	if since := time.Since(start); since > time.Second {
		t.Fatalf("static error must not wait for Consul: %v", since)
	}
}

// Runs against a real Consul: BACKPLANE_TEST_CONSUL=localhost:8500 (make up).
func TestOpenRequiredLiveFromConsul(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	client, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = client.KV().DeleteTree("config/gated-test/", nil) })

	if _, err := client.KV().Put(&api.KVPair{Key: "config/gated-test/gate", Value: []byte("open")}, nil); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GATED_TEST_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", addr)

	rt, err := config.Open[GatedConfig](context.Background(), consuldriver.Config(), config.Service("gated-test"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if got := rt.Value().Gate.Get(); got != "open" {
		t.Fatalf("gate: %q", got)
	}
}

func TestNATSStreamSettings(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")

	cfg, err := config.Load[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}

	n := cfg.BackplaneConfig().NATS
	if n.MaxAge != 7*24*time.Hour || n.MaxBytes != 0 || n.Replicas != 1 || n.DedupWindow != 2*time.Minute ||
		n.DLQMaxAge != 30*24*time.Hour {
		t.Fatalf("defaults: %+v", n)
	}

	t.Setenv("BACKPLANE_NATS_MAX_AGE", "24h")
	t.Setenv("BACKPLANE_NATS_MAX_BYTES", "1073741824")
	t.Setenv("BACKPLANE_NATS_REPLICAS", "3")
	t.Setenv("BACKPLANE_NATS_DEDUP_WINDOW", "5m")
	t.Setenv("BACKPLANE_NATS_DLQ_MAX_AGE", "0s")

	cfg, err = config.Load[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}

	n = cfg.BackplaneConfig().NATS
	if n.MaxAge != 24*time.Hour || n.MaxBytes != 1<<30 || n.Replicas != 3 || n.DedupWindow != 5*time.Minute ||
		n.DLQMaxAge != 0 {
		t.Fatalf("env: %+v", n)
	}

	t.Setenv("BACKPLANE_NATS_REPLICAS", "7")

	if _, err := config.Load[Config](context.Background(), consuldriver.Config(), config.Service("hello"), config.WithoutFile()); err == nil {
		t.Fatal("7 replicas accepted")
	}
}

// A service named "backplane" reads BACKPLANE_* for its own fields and for
// the block: two env sources over one prefix, each against its schema.
func TestServiceNamedBackplane(t *testing.T) {
	type Own struct {
		config.Backplane `json:"backplane"`

		PG struct {
			DSN config.Secret `json:"dsn"`
		} `json:"pg"`
	}

	t.Setenv("BACKPLANE_PG_DSN", "postgres://x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "consul:8500")

	cfg, err := config.Load[Own](context.Background(), consuldriver.Config(), config.Service("backplane"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}

	if cfg.PG.DSN.Reveal() != "postgres://x" || cfg.Consul.Addr != "consul:8500" {
		t.Fatalf("pg %q consul %q", cfg.PG.DSN.Reveal(), cfg.Consul.Addr)
	}
}

type unsetLiveConfig struct {
	config.Backplane `json:"backplane"`

	Tags config.Live[[]string] `json:"tags,omitempty"` // no default, no layer sets it
}

// A Live field no layer set at Open still takes a later KV value, and a
// copy taken before it did sees it too.
func TestUnsetLiveTakesKV(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	client, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = client.KV().DeleteTree("config/unset-live/", nil) })
	t.Setenv("BACKPLANE_CONSUL_ADDR", addr)

	rt, err := config.Open[unsetLiveConfig](t.Context(), consuldriver.Config(), config.Service("unset-live"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	copied := *rt.Value() // a component's copy, taken before any value
	applied := make(chan []string, 1)

	copied.Tags.Watch(func(v []string) { applied <- v })

	if _, err := client.KV().Put(&api.KVPair{Key: "config/unset-live/tags", Value: []byte(`["a","b"]`)}, nil); err != nil {
		t.Fatal(err)
	}

	select {
	case v := <-applied:
		if fmt.Sprint(v) != "[a b]" || fmt.Sprint(copied.Tags.Get()) != "[a b]" {
			t.Fatalf("applied %v, copy reads %v", v, copied.Tags.Get())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("KV value never reached the unset Live field")
	}
}

func TestHistogramRulesFromTheEnvironment(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_TELEMETRY_METRICS_INTERVAL", "5s")
	t.Setenv("BACKPLANE_TELEMETRY_METRICS_HISTOGRAMS",
		`[{"instrument":"templates.*","aggregation":"exponential","max_size":40},{"instrument":"*","unit":"s","boundaries":[0.1,1]}]`)

	cfg, err := config.Load[Config](t.Context(), config.Service("hello"), config.WithoutFile(), config.WithoutConsul())
	if err != nil {
		t.Fatal(err)
	}

	m := cfg.BackplaneConfig().Telemetry.Metrics
	if m.Interval != 5*time.Second || len(m.Histograms) != 2 || m.Histograms[0].MaxSize != 40 ||
		m.Histograms[0].Aggregation != config.AggregationExponential || m.Histograms[1].Unit != "s" ||
		len(m.Histograms[1].Boundaries) != 2 {
		t.Fatalf("metrics: %+v", m)
	}

	// An aggregation left out is explicit.
	if m.Histograms[1].Aggregation != config.AggregationExplicit {
		t.Fatalf("default aggregation: %q", m.Histograms[1].Aggregation)
	}
}
