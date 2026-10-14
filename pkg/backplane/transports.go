package backplane

import (
	"context"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/direct"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// TaskGroup runs background tasks within a service lifecycle node.
type TaskGroup = node.Group

// EventMessage is the transport-neutral event envelope.
type EventMessage = env.Message

// Connection is a transport managed by the service lifecycle.
type Connection interface {
	Connect(ctx context.Context, group TaskGroup) error
	Close(ctx context.Context) error
	Connected() bool
}

// EventTransport publishes events and serves declared consumers.
type EventTransport interface {
	Connection
	Publish(ctx context.Context, message EventMessage) error
	StartReactors(ctx context.Context, group TaskGroup) error
	StopReactors(ctx context.Context) error
}

// WorkflowTransport carries hooks and serves the service's declared activities
// and workflows. Native workflow APIs belong to the selected driver.
type WorkflowTransport interface {
	Connection
	Call(ctx context.Context, hook string, payload []byte) ([]byte, error)
	StartHookWorker(ctx context.Context, group TaskGroup) error
	StopHookWorker(ctx context.Context) error
	StartWorker(ctx context.Context, group TaskGroup) error
	StopWorker(ctx context.Context) error
}

// EventTransportFactory builds one transport per service, without connecting.
type EventTransportFactory func(config.Backplane, deps.Scope, Identity) EventTransport

// WorkflowTransportFactory builds one transport per service, without connecting.
type WorkflowTransportFactory func(config.Backplane, deps.Scope, Identity) WorkflowTransport

// WithEventTransport installs an event driver explicitly at the composition root.
func WithEventTransport(factory EventTransportFactory) Option {
	return func(o *options) { o.events = factory }
}

// WithWorkflowTransport installs a workflow driver explicitly at the composition root.
func WithWorkflowTransport(factory WorkflowTransportFactory) Option {
	return func(o *options) { o.workflows = factory }
}

// ConfigurationState exposes masked configuration and live update notifications.
type ConfigurationState = configrt.State

// Presence publishes instance state and manages catalog registration.
type Presence interface {
	Start(ctx context.Context, group TaskGroup) error
	Stop(ctx context.Context) error
	Register(ctx context.Context) error
	Deregister(ctx context.Context) error
}

// HookDiscovery finds the platform addresses of backplane's replicas for
// the direct hook transport.
type HookDiscovery = direct.Discovery

// HookDiscoveryFactory builds the discovery from the configuration.
type HookDiscoveryFactory func(cfg config.Backplane, log *xlog.Logger) (HookDiscovery, error)

// WithHookDiscovery installs how the direct hook transport finds backplane
// (the Consul driver installs one over the catalog's instance states).
func WithHookDiscovery(factory HookDiscoveryFactory) Option {
	return func(o *options) { o.discovery = factory }
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
	Commit        string
	Transports    func() []*backplanev1.TransportStatus
	Nodes         func() []*backplanev1.NodeStatus
}

// PresenceFactory builds a discovery session without starting it.
type PresenceFactory func(PresenceParams) (Presence, error)

// WithPresence installs the discovery driver used by the service lifecycle.
func WithPresence(factory PresenceFactory) Option {
	return func(o *options) { o.presence = factory }
}
