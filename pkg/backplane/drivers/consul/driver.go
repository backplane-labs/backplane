// Package consul installs optional discovery and live configuration.
package consul

import (
	"github.com/backplane-labs/backplane/pkg/backplane"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/consul"
)

// Driver installs Consul discovery and its live configuration source.
func Driver() backplane.Option {
	remote := backplane.ConfigOptions(Config())
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

	return backplane.Options(remote, presence)
}
