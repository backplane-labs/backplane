package valkey_test

import (
	"errors"
	"os"
	"strings"
	"testing"

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

	if err := (valkey.Config{Addr: "v:6379"}).Validate(); err != nil {
		t.Fatal(err)
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
