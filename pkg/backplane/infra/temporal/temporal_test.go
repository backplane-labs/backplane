package temporal_test

import (
	"errors"
	"os"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
)

func TestConfig(t *testing.T) {
	t.Parallel()

	if err := (temporal.Config{}).Validate(); !errors.Is(err, temporal.ErrNoAddr) {
		t.Fatalf("no addr: %v", err)
	}

	bad := temporal.Config{Addr: "h:7233", TLS: config.TLS{Enabled: true, CA: "junk"}}
	if err := bad.Validate(); !errors.Is(err, config.ErrTLSCA) {
		t.Fatalf("tls: %v", err)
	}

	if _, err := temporal.Options(bad, "svc-1", nil); !errors.Is(err, config.ErrTLSCA) {
		t.Fatalf("options tls: %v", err)
	}

	opts, err := temporal.Options(temporal.Config{Addr: "h:7233", APIKey: "secret"}, "svc-1", nil)
	if err != nil || opts.Namespace != temporal.DefaultNamespace || opts.Credentials == nil ||
		opts.Identity != "svc-1" || len(opts.Interceptors) != 1 || opts.MetricsHandler == nil {
		t.Fatalf("options: %+v %v", opts, err)
	}
}

// TestProvider dials BACKPLANE_TEST_TEMPORAL (make up: localhost:7233);
// skipped without.
func TestProvider(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_TEMPORAL not set")
	}

	h := backplanetest.New(t)
	deps.NewDependency(h.Root(), temporal.New(temporal.Config{Addr: addr}), deps.Name("billing"))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}
}
