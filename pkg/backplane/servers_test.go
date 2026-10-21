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
