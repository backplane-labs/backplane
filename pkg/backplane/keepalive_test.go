package backplane_test

import (
	"math"
	"testing"
	"time"

	"google.golang.org/grpc/keepalive"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// A zero connection bound is grpc-go's "never"; the rest pass as set.
func TestKeepaliveParams(t *testing.T) {
	t.Parallel()

	never := time.Duration(math.MaxInt64)

	got := backplane.KeepaliveParams(config.Server{GRPCKeepaliveTime: time.Minute, GRPCKeepaliveTimeout: 5 * time.Second})
	if want := (keepalive.ServerParameters{
		Time: time.Minute, Timeout: 5 * time.Second, MaxConnectionIdle: never, MaxConnectionAge: never, MaxConnectionAgeGrace: never,
	}); got != want {
		t.Fatalf("zero bounds: %+v", got)
	}

	got = backplane.KeepaliveParams(config.Server{
		GRPCMaxConnectionIdle: time.Minute, GRPCMaxConnectionAge: time.Hour, GRPCMaxConnectionAgeGrace: time.Second,
	})
	if got.MaxConnectionIdle != time.Minute || got.MaxConnectionAge != time.Hour || got.MaxConnectionAgeGrace != time.Second {
		t.Fatalf("bounds: %+v", got)
	}
}

// ServerKeepalive is the service's default; the environment overrides it.
func TestServerKeepalive(t *testing.T) {
	setup(t, "")

	open := func() time.Duration {
		var got time.Duration

		svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
			got = root.Config().BackplaneConfig().Server.GRPCKeepaliveTime

			return &testState{}, nil
		}, baseOptions("svc-keepalive", backplane.ServerKeepalive(30*time.Second))...)
		if err != nil {
			t.Fatal(err)
		}

		if err := svc.Close(); err != nil {
			t.Fatal(err)
		}

		return got
	}

	if got := open(); got != 30*time.Second {
		t.Fatalf("service default: %v", got)
	}

	t.Setenv("BACKPLANE_SERVER_GRPC_KEEPALIVE_TIME", "1m")

	if got := open(); got != time.Minute {
		t.Fatalf("environment: %v", got)
	}
}
