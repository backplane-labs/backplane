package config_test

import (
	"context"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// The service's defaults sit above the schema's and below file and env.
func TestDefaultsUnderFileAndEnv(t *testing.T) {
	t.Setenv("BACKPLANE_CONSUL_ADDR", "")

	opts := []config.Option{
		config.Service("hello"),
		config.Defaults(map[string]any{"salute": "Hey", "postgres": map[string]any{"dsn": "x", "timeout": "7s"}}),
		config.Defaults(map[string]any{"postgres": map[string]any{"timeout": "9s"}}),
		config.BlockDefaults(map[string]any{"server": map[string]any{"grpc_keepalive_time": "30s"}}),
	}

	cfg, err := config.Load[Config](context.Background(), append(opts, config.WithoutFile())...)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Salute != "Hey" || cfg.Postgres.DSN.Reveal() != "x" || cfg.Postgres.Timeout != 9*time.Second {
		t.Fatalf("service defaults: %+v", cfg)
	}

	if got := cfg.Server.GRPCKeepaliveTime; got != 30*time.Second {
		t.Fatalf("block default: %v", got)
	}

	if got := cfg.Server.GRPCKeepaliveTimeout; got != 20*time.Second {
		t.Fatalf("schema default beside it: %v", got)
	}

	t.Setenv("BACKPLANE_SERVER_GRPC_KEEPALIVE_TIME", "1m")
	t.Setenv("HELLO_SALUTE", "Hi")

	path := write(t, "postgres:\n  timeout: 3s\n")

	cfg, err = config.Load[Config](context.Background(), append(opts, config.File(path))...)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.GRPCKeepaliveTime != time.Minute || cfg.Salute != "Hi" || cfg.Postgres.Timeout != 3*time.Second {
		t.Fatalf("overridden: keepalive %v salute %q timeout %v", cfg.Server.GRPCKeepaliveTime, cfg.Salute, cfg.Postgres.Timeout)
	}
}
