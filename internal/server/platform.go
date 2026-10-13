package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/consul/api"
	"go.temporal.io/sdk/client"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/internal/platform"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	natsdriver "github.com/gopherex/backplane/pkg/backplane/drivers/nats"
	"github.com/gopherex/backplane/pkg/backplane/workflows"
)

var (
	errHealthUnavailable = errors.New("health endpoint unavailable")
	errHealthURL         = errors.New("health endpoints require HTTP(S) URLs without credentials, query or fragment")
)

// Infrastructure contains optional deployment-owned health endpoints. Collector
// and gateway expose no universal health URL: deployments supply theirs here.
type Infrastructure struct {
	CollectorURL string `json:"collector_url,omitempty"`
	GatewayURL   string `json:"gateway_url,omitempty"`
	MetricsURL   string `json:"metrics_url,omitempty"`
	LogsURL      string `json:"logs_url,omitempty"`
	TracesURL    string `json:"traces_url,omitempty"`
}

func capabilities(c Config) []*consolev1.Capability {
	return []*consolev1.Capability{
		{Name: "events", Enabled: c.NATS.Enabled()},
		{Name: "workflows", Enabled: c.Temporal.Enabled()},
		{Name: "schedules", Enabled: c.Temporal.Enabled()},
		{Name: "bindings", Enabled: c.Temporal.Enabled()},
		{Name: "rules", Enabled: c.NATS.Enabled() && c.Temporal.Enabled()},
		{Name: "metrics", Enabled: c.Obs.MetricsURL != ""},
		{Name: "logs", Enabled: c.Obs.LogsURL != ""},
		{Name: "traces", Enabled: c.Obs.TracesURL != ""},
		{Name: "telemetry", Enabled: c.OTLP.URL != ""},
		{Name: "audit", Enabled: true},
		{Name: "audit_export", Enabled: c.Audit.ExportURL != ""},
	}
}

func disabledMethods(c Config) func(string, string) bool {
	return func(service, method string) bool {
		switch strings.TrimPrefix(service, "backplane.console.v1.") {
		case "WorkflowService", "ScheduleService", "CallService":
			return !c.Temporal.Enabled()
		case "EventService":
			return !c.NATS.Enabled()
		case "BindingService":
			switch method {
			case "TestBinding", "ListBindingRuns", "GetBindingRun", "CancelBindingRun":
				return !c.Temporal.Enabled()
			}
		case "RuleService":
			switch method {
			case "TestRule", "ListRuleRuns", "GetRuleRun", "CancelRuleRun":
				return !c.Temporal.Enabled() || !c.NATS.Enabled()
			}
		}

		return false
	}
}

//nolint:wrapcheck // probe errors are classified and never exposed verbatim
func (st *State) probes(root backplane.Root[Config], cfg Config) []platform.Probe {
	probes := []platform.Probe{
		{Name: "postgres", Required: true, Run: func(ctx context.Context) error {
			if !st.Store.Ready() {
				return deps.ErrNotReady
			}

			return st.Store.Get().Pool.Ping(ctx)
		}},
		{Name: "consul", Required: true},
		{Name: "valkey", Required: true, Run: func(ctx context.Context) error {
			if !st.Valkey.Ready() {
				return deps.ErrNotReady
			}

			v := st.Valkey.Get()

			return v.Do(ctx, v.B().Ping().Build()).Error()
		}},
	}
	// A KV read also checks that the configured Consul token works.
	probes[1].Run = func(ctx context.Context) error {
		_, _, err := st.Consul.KV().Get("backplane/health", (&api.QueryOptions{}).WithContext(ctx))
		return err
	}
	if cfg.Temporal.Enabled() {
		probes = append(probes, platform.Probe{Name: "temporal", Run: func(ctx context.Context) error {
			c, err := workflows.Client(root)
			if err != nil {
				return err
			}

			_, err = c.CheckHealth(ctx, &client.CheckHealthRequest{})

			return err
		}})
	}

	if cfg.NATS.Enabled() {
		probes = append(probes, platform.Probe{Name: "nats", Run: func(ctx context.Context) error {
			j, err := natsdriver.JetStream(root)
			if err != nil {
				return err
			}

			_, err = j.AccountInfo(ctx)

			return err
		}})
	}

	for _, p := range []struct {
		name, url string
		auth      config.Secret
	}{
		{"collector", cfg.Infrastructure.CollectorURL, ""},
		{"gateway", cfg.Infrastructure.GatewayURL, ""},
		{"metrics", healthURL(cfg.Obs.MetricsURL, cfg.Infrastructure.MetricsURL), cfg.Obs.MetricsAuthorization},
		{"logs", healthURL(cfg.Obs.LogsURL, cfg.Infrastructure.LogsURL), cfg.Obs.LogsAuthorization},
		{"traces", healthURL(cfg.Obs.TracesURL, cfg.Infrastructure.TracesURL), cfg.Obs.TracesAuthorization},
	} {
		if p.url != "" {
			probes = append(probes, platform.Probe{Name: p.name, Run: httpProbe(p.url, p.auth)})
		}
	}

	return probes
}

// Victoria stores have /health at their origin. An explicit URL supports
// deployments that expose a different route through their ingress.
func healthURL(backend, override string) string {
	if backend == "" {
		return ""
	}

	if override != "" {
		return override
	}

	parsed, err := url.Parse(backend)
	if err != nil {
		return ""
	}

	parsed.Path = "/health"
	parsed.RawPath = ""

	return parsed.String()
}

func httpProbe(endpoint string, auth config.Secret) func(context.Context) error {
	httpClient := &http.Client{CheckRedirect: func(*http.Request,
		[]*http.Request,
	) error {
		return http.ErrUseLastResponse
	}}

	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
		if err != nil {
			return fmt.Errorf("health request: %w", err)
		}

		if auth.Reveal() != "" {
			req.Header.Set("Authorization", auth.Reveal())
		}

		res, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("health check: %w", err)
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			return errHealthUnavailable
		}

		return nil
	}
}

// Validate prevents malformed endpoints and credentials in URLs.
func (c Infrastructure) Validate() error {
	for _, endpoint := range []string{c.CollectorURL, c.GatewayURL, c.MetricsURL, c.LogsURL, c.TracesURL} {
		if endpoint == "" {
			continue
		}

		u, err := url.Parse(endpoint)
		if err != nil || !httpURL(endpoint) || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errHealthURL
		}
	}

	return nil
}
