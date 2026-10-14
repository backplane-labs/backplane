// Package standard composes the standard platform drivers. Applications opt in
// once at their composition root; component packages only import SDK contracts.
package standard

import (
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/consul"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/temporal"
)

// Drivers installs Consul, NATS and Temporal. Each connects only when configured.
func Drivers() backplane.Option {
	return backplane.Options(consul.Driver(), nats.Driver(), temporal.Driver())
}
