package consul

import (
	"fmt"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// apiConfig maps the block onto the Consul client configuration. It starts
// from api.DefaultConfig, so the standard CONSUL_HTTP_ADDR,
// CONSUL_HTTP_TOKEN(_FILE), CONSUL_HTTP_SSL, CONSUL_CACERT, CONSUL_CAPATH,
// CONSUL_CLIENT_CERT/KEY, CONSUL_TLS_SERVER_NAME and CONSUL_HTTP_SSL_VERIFY
// fill what the block leaves empty; every field the block sets wins. With
// the block's TLS enabled its TLS replaces the environment's entirely.
func apiConfig(c config.Consul) (*api.Config, error) {
	cfg := api.DefaultConfig()
	cfg.Address = c.Addr

	if token := c.Token.Reveal(); token != "" {
		cfg.Token, cfg.TokenFile = token, ""
	}

	if c.Datacenter != "" {
		cfg.Datacenter = c.Datacenter
	}

	tlsCfg, err := c.TLS.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("consul: %w", err)
	}

	if tlsCfg != nil {
		cfg.Scheme = "https"
		cfg.TLSConfig = api.TLSConfig{}
		cfg.Transport.TLSClientConfig = tlsCfg
	}

	return cfg, nil
}

// client builds the Consul API client. The HTTP client is built here: with
// the block's TLS on the transport api.NewHttpClient keeps it, and
// api.NewClient does not refill TLS from the environment behind the
// block's back.
func newClient(c config.Consul) (*api.Client, error) {
	cfg, err := apiConfig(c)
	if err != nil {
		return nil, err
	}

	hc, err := api.NewHttpClient(cfg.Transport, cfg.TLSConfig)
	if err != nil {
		return nil, fmt.Errorf("consul tls: %w", err)
	}

	cfg.HttpClient = hc

	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("consul client %s: %w", c.Addr, err)
	}

	return client, nil
}
