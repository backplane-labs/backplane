package valkey_test

import (
	"errors"
	"testing"

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
