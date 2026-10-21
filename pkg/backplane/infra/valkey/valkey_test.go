package valkey_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/valkey"
)

// The provider itself runs live in internal/console (BACKPLANE_TEST_VALKEY).
func TestValidate(t *testing.T) {
	t.Parallel()

	if err := (valkey.Config{}).Validate(); !errors.Is(err, valkey.ErrNoAddr) {
		t.Fatalf("no addr: %v", err)
	}

	bad := valkey.Config{Addr: "v:6379", TLS: config.TLS{Enabled: true, CA: "junk"}}
	if err := bad.Validate(); !errors.Is(err, config.ErrTLSCA) {
		t.Fatalf("tls: %v", err)
	}

	if err := (valkey.Config{Addr: " , "}).Validate(); !errors.Is(err, valkey.ErrNoAddr) {
		t.Fatalf("only commas: %v", err)
	}

	for _, addr := range []string{"v:6379", "a:6379, b:6379"} {
		if err := (valkey.Config{Addr: addr}).Validate(); err != nil {
			t.Fatalf("%q: %v", addr, err)
		}
	}
}

// A Valkey 9+ cluster with cluster-databases keeps numbered databases
// apart, and is found from any seed that answers:
// BACKPLANE_TEST_VALKEY_CLUSTER is such a cluster with cluster-databases
// above 5.
func TestClusterDatabases(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_VALKEY_CLUSTER")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_VALKEY_CLUSTER not set")
	}

	h := backplanetest.New(t)
	h.Start()

	open := func(db int64) valkey.Client {
		// The first seed does not answer.
		c, err := valkey.New(valkey.Config{Addr: "127.0.0.1:1," + addr, DB: db}).Provide(t.Context(), h.Root())
		if err != nil {
			t.Fatalf("db %d: %v", db, err)
		}

		t.Cleanup(c.Close)

		return c
	}

	db0, db5 := open(0), open(5)
	key := fmt.Sprintf("bp-test-%d", time.Now().UnixNano())

	if err := db5.Do(t.Context(), db5.B().Set().Key(key).Value("five").Ex(time.Minute).Build()).Error(); err != nil {
		t.Fatal(err)
	}

	if v, err := db5.Do(t.Context(), db5.B().Get().Key(key).Build()).ToString(); err != nil || v != "five" {
		t.Fatalf("db 5: %q, %v", v, err)
	}

	if n, err := db0.Do(t.Context(), db0.B().Exists().Key(key).Build()).AsInt64(); err != nil || n != 0 {
		t.Fatalf("db 0 sees db 5's key: %d, %v", n, err)
	}
}

// A classic cluster has database 0 only, and says so:
// BACKPLANE_TEST_VALKEY_CLASSIC_CLUSTER is a cluster without
// cluster-databases (Valkey 8, say).
func TestClassicClusterDatabase(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_VALKEY_CLASSIC_CLUSTER")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_VALKEY_CLASSIC_CLUSTER not set")
	}

	h := backplanetest.New(t)
	h.Start()

	c, err := valkey.New(valkey.Config{Addr: addr}).Provide(t.Context(), h.Root())
	if err != nil {
		t.Fatalf("db 0: %v", err)
	}

	c.Close()

	if _, err := valkey.New(valkey.Config{Addr: addr, DB: 1}).Provide(t.Context(), h.Root()); !errors.Is(err, valkey.ErrClusterDB) {
		t.Fatalf("db 1: %v", err)
	}
}

// A database number is the server's to allow (its databases setting), not
// the client's: BACKPLANE_TEST_VALKEY must have databases above 4000.
func TestDatabaseBeyondSixteen(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_VALKEY")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_VALKEY not set")
	}

	h := backplanetest.New(t)
	h.Start()

	client, err := valkey.New(valkey.Config{Addr: addr, DB: 4000}).Provide(t.Context(), h.Root())
	if err != nil {
		t.Fatalf("database 4000: %v", err)
	}

	client.Close()

	if _, err := valkey.New(valkey.Config{Addr: addr, DB: 1 << 20}).Provide(t.Context(), h.Root()); err == nil ||
		!strings.Contains(err.Error(), "out of range") {
		t.Fatalf("a database the server does not have: %v", err)
	}
}

// The schema bounds the number below only: any database the server has
// passes validation.
func TestConfigTakesAnyDatabase(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Valkey valkey.Config `json:"valkey"`
	}

	got := backplanetest.Config[cfg](t, map[string]string{"VALKEY_ADDR": "v:6379", "VALKEY_DB": "4095"})
	if got.Valkey.DB != 4095 {
		t.Fatalf("db = %d", got.Valkey.DB)
	}
}

// Cache-aside: one fill per miss across clients, then client-side cached
// until the server invalidates the key.
func TestAside(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_VALKEY")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_VALKEY not set")
	}

	h := backplanetest.New(t)
	h.Start()

	cfg := valkey.Config{Addr: addr, DB: 3}
	provider := valkey.NewAside(cfg, valkey.AsideClientTTL(5*time.Second))

	first, err := provider.Provide(t.Context(), h.Root())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := provider.Provide(t.Context(), h.Root())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	key := fmt.Sprintf("aside:%s:%d", t.Name(), time.Now().UnixNano())

	var fills atomic.Int32

	load := func(context.Context, string) (string, error) {
		return fmt.Sprintf("v%d", fills.Add(1)), nil
	}

	for range 3 {
		for _, c := range []valkey.AsideClient{first, second} {
			if v, err := c.Get(t.Context(), time.Minute, key, load); err != nil || v != "v1" {
				t.Fatalf("get: %q %v", v, err)
			}
		}
	}

	if n := fills.Load(); n != 1 {
		t.Fatalf("fills = %d, want 1", n)
	}

	// A delete invalidates every client's copy: the next read fills again.
	if err := first.Del(t.Context(), key); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)

	for {
		v, err := second.Get(t.Context(), time.Minute, key, load)
		if err != nil {
			t.Fatal(err)
		}

		if v == "v2" {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("second client still reads %q after the delete", v)
		}

		time.Sleep(10 * time.Millisecond)
	}

	if err := provider.Probe(t.Context(), first); err != nil {
		t.Fatalf("probe: %v", err)
	}
}
