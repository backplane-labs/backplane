package consul

import (
	"github.com/gopherex/xconf"
	consulsrc "github.com/gopherex/xconf/contrib/sources/consul"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
)

// Config installs the Consul KV configuration source. Driver includes this option.
func Config(opts ...consulsrc.Option) config.Option {
	return config.WithRemote(func(cfg config.Backplane, service string) (config.RemoteSource, error) {
		client, err := newClient(cfg.Consul)
		if err != nil {
			return nil, err
		}

		prefix := "config/" + service + "/"
		options := append([]consulsrc.Option{
			consulsrc.Name(configrt.SourceConsul + prefix), consulsrc.IgnoreKeys(configrt.RevisionKey),
		}, opts...)
		lister := revisionLister{kv: client.KV(), key: prefix + configrt.RevisionKey}

		return &revisioned{inner: consulsrc.NewPrefix(lister, prefix, options...)}, nil
	})
}

var _ xconf.Watcher = (*revisioned)(nil)
