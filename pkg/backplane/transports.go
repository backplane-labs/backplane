package backplane

import (
	"context"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// TaskGroup runs background tasks within a service lifecycle node.
type TaskGroup = node.Group

// ConfigurationState exposes masked configuration and live update notifications.
type ConfigurationState = configrt.State

// Presence publishes instance state and manages catalog registration.
type Presence interface {
	Start(ctx context.Context, group TaskGroup) error
	Stop(ctx context.Context) error
	Register(ctx context.Context) error
	Deregister(ctx context.Context) error
}

// PresenceParams provides a discovery driver with the service's sealed contract.
type PresenceParams struct {
	Config        config.Backplane
	Identity      Identity
	Manifest      *backplanev1.Manifest
	Log           *xlog.Logger
	Configuration ConfigurationState
	PlatformPort  uint16
	PrimaryPort   uint16
	// Listeners are the ports of the route listeners by name ("": the
	// default public port).
	Listeners  map[string]uint16
	Commit     string
	Transports func() []*backplanev1.TransportStatus
	Nodes      func() []*backplanev1.NodeStatus
}

// PresenceFactory builds a discovery session without starting it.
type PresenceFactory func(PresenceParams) (Presence, error)

// WithPresence installs the discovery driver used by the service lifecycle.
func WithPresence(factory PresenceFactory) Option {
	return func(o *options) { o.presence = factory }
}
