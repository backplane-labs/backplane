package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gopherex/backplane/internal/server"
	"github.com/gopherex/backplane/pkg/backplane/config"
	consuldriver "github.com/gopherex/backplane/pkg/backplane/drivers/consul"
)

func load(t *testing.T) (server.Config, error) {
	t.Helper()

	return config.Load[server.Config](context.Background(), consuldriver.Config(), config.Service(server.Name), config.WithoutFile())
}

// The environment: BACKPLANE_* for the server's sections and the block.
func TestConfigFromEnv(t *testing.T) {
	t.Setenv("BACKPLANE_PG_DSN", "postgres://pg/backplane")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "consul:8500")
	t.Setenv("BACKPLANE_ADMIN_TOKEN", "admin-token-0123456789")
	t.Setenv("BACKPLANE_CONSOLE_ORIGINS", `["https://console.example.com"]`)
	t.Setenv("BACKPLANE_CONSOLE_TRUSTED_PROXIES", `["10.0.0.0/8","192.168.1.1"]`)
	t.Setenv("BACKPLANE_OBS_METRICS_URL", "http://grafana:3000")
	t.Setenv("BACKPLANE_OTLP_URL", "http://collector:4318")
	t.Setenv("BACKPLANE_OTLP_KEYS", `["previous-ingest-key","current-ingest-key"]`)

	cfg, err := load(t)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.PG.DSN.Reveal() != "postgres://pg/backplane" || cfg.Consul.Addr != "consul:8500" ||
		cfg.AdminToken.Reveal() != "admin-token-0123456789" ||
		len(cfg.Console.Origins) != 1 || len(cfg.Console.TrustedProxies) != 2 ||
		cfg.Obs.MetricsURL != "http://grafana:3000" {
		t.Fatalf("env: %+v", cfg)
	}

	if cfg.Console.Listen != ":8081" || cfg.Audit.Listen != ":4317" || cfg.InternalPort != 9400 {
		t.Fatalf("defaults: console %q internal %d", cfg.Console.Listen, cfg.InternalPort)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	if cfg.OTLP.Concurrent != 16 || cfg.OTLP.BodyBytes != 2<<20 || len(cfg.OTLP.Keys) != 2 ||
		cfg.OTLP.Keys[1].Reveal() != "current-ingest-key" {
		t.Fatal("OTLP admission defaults or deployment keys were not loaded")
	}
}

// Without BACKPLANE_ADMIN_TOKEN the configuration does not open, and the
// error names the variable.
func TestConfigNoAdminToken(t *testing.T) {
	t.Setenv("BACKPLANE_PG_DSN", "postgres://pg/backplane")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "consul:8500")

	cfg, err := load(t)
	if err == nil {
		err = cfg.Validate()
	}

	if !errors.Is(err, server.ErrNoAdminToken) || !strings.Contains(err.Error(), "BACKPLANE_ADMIN_TOKEN") {
		t.Fatalf("want %v, got %v", server.ErrNoAdminToken, err)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	valid := func() server.Config {
		var c server.Config

		c.PG.DSN = "postgres://pg/backplane"
		c.Consul.Addr = "consul:8500"
		c.AdminToken = "admin-token-0123456789"
		c.InternalPort, c.PublicPort = 9400, 8080
		c.Console.Listen, c.Audit.Listen = ":8081", ":4317"

		return c
	}

	if err := valid().Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}

	cases := map[string]struct {
		edit func(c *server.Config)
		want error
	}{
		"no dsn":            {func(c *server.Config) { c.PG.DSN = "" }, server.ErrNoPG},
		"no consul":         {func(c *server.Config) { c.Consul.Addr = "" }, server.ErrNoConsul},
		"no admin token":    {func(c *server.Config) { c.AdminToken = "" }, server.ErrNoAdminToken},
		"short admin token": {func(c *server.Config) { c.AdminToken = "short" }, server.ErrShortAdminToken},
		"bad listen":        {func(c *server.Config) { c.Console.Listen = "8081" }, server.ErrAddr},
		"port 0":            {func(c *server.Config) { c.Console.Listen = ":0" }, server.ErrAddr},
		"console on public": {func(c *server.Config) { c.Console.Listen = ":8080" }, server.ErrPortTaken},
		"same ports":        {func(c *server.Config) { c.Console.Listen = "127.0.0.1:4317" }, server.ErrPortTaken},
		"prefix slash":      {func(c *server.Config) { c.Console.Prefix = "/bp/" }, server.ErrPrefix},
		"origin path":       {func(c *server.Config) { c.Console.Origins = []string{"https://c.example.com/x"} }, server.ErrOrigin},
		"origin scheme":     {func(c *server.Config) { c.Console.Origins = []string{"c.example.com"} }, server.ErrOrigin},
		"proxy":             {func(c *server.Config) { c.Console.TrustedProxies = []string{"10.0.0.0/33"} }, server.ErrProxy},
		"obs not a url":     {func(c *server.Config) { c.Obs.TracesURL = "tempo:3200" }, server.ErrURL},
		"audit on console":  {func(c *server.Config) { c.Audit.Listen = ":8081" }, server.ErrPortTaken},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := valid()
			tc.edit(&c)

			if err := c.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}
