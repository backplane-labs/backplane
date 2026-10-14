// Package consul installs optional discovery and live configuration.
package consul

import (
	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/consul"
)

// Driver installs Consul discovery, its live configuration source and the
// discovery of backplane's replicas for the direct hook transport.
func Driver() backplane.Option {
	remote := backplane.ConfigOptions(Config())
	discovery := backplane.WithHookDiscovery(func(cfg config.Backplane, _ *xlog.Logger) (backplane.HookDiscovery, error) {
		client, err := newClient(cfg.Consul)
		if err != nil {
			return nil, err
		}

		return internal.NewDiscovery(client), nil
	})
	presence := backplane.WithPresence(func(p backplane.PresenceParams) (backplane.Presence, error) {
		client, err := newClient(p.Config.Consul)
		if err != nil {
			return nil, err
		}

		return internal.New(internal.Params{
			Client: client,
			Log:    p.Log,
			Identity: internal.Identity{
				Service:      p.Identity.Service,
				Version:      p.Manifest.GetVersion(),
				Instance:     p.Identity.Instance,
				Address:      p.Identity.Advertise,
				PlatformPort: p.PlatformPort,
				PublicPort:   p.PrimaryPort,
				Commit:       p.Commit,
			},
			Manifest: p.Manifest,
			Register: p.Config.Consul.Register,
			Tags:     p.Config.Consul.Tags,
			Check: internal.Check{
				Interval: p.Config.Consul.CheckInterval, Timeout: p.Config.Consul.CheckTimeout,
				DeregisterAfter: p.Config.Consul.DeregisterAfter,
			},
			SessionTTL: p.Config.Consul.SessionTTL,
			Config:     p.Configuration,
			Transports: p.Transports,
			Nodes:      p.Nodes,
		})
	})

	return backplane.Options(remote, presence, discovery)
}
