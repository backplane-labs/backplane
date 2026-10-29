// Package consul installs optional discovery and live configuration.
package consul

import (
	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/consul"
)

// Driver installs Consul discovery, its live configuration source, the
// discovery of backplane's replicas for the direct hook transport and the
// mesh: mutual TLS between services with Consul's CA and intentions.
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
				Listeners:    p.Listeners,
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
			Sections:   p.Sections,
		})
	})

	mesh := backplane.WithMesh(func(cfg config.Backplane, service string, log *xlog.Logger) (backplane.Mesh, error) {
		client, err := newClient(cfg.Consul)
		if err != nil {
			return nil, err
		}

		return internal.NewMesh(internal.MeshParams{
			Client: client, Service: service, DefaultAllow: cfg.Mesh.Default != config.MeshDeny, Log: log,
		}), nil
	})

	return backplane.Options(remote, presence, discovery, mesh)
}
