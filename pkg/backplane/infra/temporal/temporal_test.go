package temporal_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"go.temporal.io/sdk/client"

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

// The connection's knobs reach the client's options: explicit values, a
// hand-built zero reads the default, gzip is Temporal's own default.
func TestConnectionOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		conn config.TemporalConnection
		want client.ConnectionOptions
		gzip bool
	}{
		{name: "defaults", want: client.ConnectionOptions{
			KeepAliveTime: 30 * time.Second, KeepAliveTimeout: 15 * time.Second, GetSystemInfoTimeout: 5 * time.Second,
		}, gzip: true},
		{name: "explicit", conn: config.TemporalConnection{
			KeepAliveTime: 10 * time.Second, KeepAliveTimeout: 3 * time.Second, GetSystemInfoTimeout: time.Second,
			MaxPayloadSize: 4 << 20, GRPCCompression: "none",
		}, want: client.ConnectionOptions{
			KeepAliveTime: 10 * time.Second, KeepAliveTimeout: 3 * time.Second, GetSystemInfoTimeout: time.Second,
			MaxPayloadSize: 4 << 20,
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts, err := temporal.Options(temporal.Config{Addr: "t:7233", TemporalConnection: tc.conn}, "test", nil)
			if err != nil {
				t.Fatal(err)
			}

			got := opts.ConnectionOptions
			if got.KeepAliveTime != tc.want.KeepAliveTime || got.KeepAliveTimeout != tc.want.KeepAliveTimeout ||
				got.GetSystemInfoTimeout != tc.want.GetSystemInfoTimeout || got.MaxPayloadSize != tc.want.MaxPayloadSize {
				t.Fatalf("connection options = %+v", got)
			}

			_, isGzip := got.GrpcCompression.(*client.GrpcCompressionGzip)
			if isGzip != tc.gzip {
				t.Fatalf("compression = %T", got.GrpcCompression)
			}
		})
	}

	if err := (temporal.Config{Addr: "t:7233", TemporalConnection: config.TemporalConnection{GRPCCompression: "zstd"}}).
		Validate(); !errors.Is(err, config.ErrTemporalCompression) {
		t.Fatalf("zstd: %v", err)
	}
}

// The loaded sections (infra and the SDK's own) carry explicit defaults.
func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	type cfg struct {
		config.Backplane `json:"backplane"`

		Billing temporal.Config `json:"billing"`
	}

	got := backplanetest.Config[cfg](t, map[string]string{"BILLING_ADDR": "t:7233", "BACKPLANE_TEMPORAL_KEEPALIVE_TIME": "45s"})

	want := config.TemporalConnection{
		KeepAliveTime: 30 * time.Second, KeepAliveTimeout: 15 * time.Second, GetSystemInfoTimeout: 5 * time.Second,
		GRPCCompression: "gzip",
	}
	if got.Billing.TemporalConnection != want {
		t.Fatalf("billing = %+v", got.Billing.TemporalConnection)
	}

	if got.Temporal.KeepAliveTime != 45*time.Second {
		t.Fatalf("sdk = %+v", got.Temporal.TemporalConnection)
	}
}
