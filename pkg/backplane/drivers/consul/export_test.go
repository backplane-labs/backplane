package consul

import (
	"crypto/tls"
	"net/http"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// ConsulAPIConfig is the client configuration the block maps to.
func ConsulAPIConfig(c config.Consul) (*api.Config, error) { return apiConfig(c) }

// ConsulClientTLS builds the client's HTTP client and returns the TLS
// settings of its transport.
func ConsulClientTLS(c config.Consul) (*tls.Config, error) {
	cfg, err := apiConfig(c)
	if err != nil {
		return nil, err
	}

	hc, err := api.NewHttpClient(cfg.Transport, cfg.TLSConfig)
	if err != nil {
		return nil, err
	}

	return hc.Transport.(*http.Transport).TLSClientConfig, nil
}
