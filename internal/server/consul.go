package server

import (
	"fmt"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// consulClient is backplane's own Consul client, built from the SDK block
// as the SDK builds its own (which it does not share): api.DefaultConfig
// fills what the block leaves empty (CONSUL_HTTP_TOKEN, CONSUL_CACERT, …),
// every field the block sets wins, and the block's TLS, when enabled,
// replaces the environment's entirely.
func consulClient(c config.Consul) (*api.Client, error) {
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

	// Built here so api.NewClient keeps the block's TLS on the transport.
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
