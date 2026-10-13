package server

import (
	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/gopherex/backplane/pkg/backplane"
)

// Declare is what the server serves beyond its tree, declared before Run
// from svc.State(): the readiness. Component listeners belong to their nodes.
func Declare(svc *backplane.Service[State]) {
	// The SDK deliberately permits unguarded local mode with an empty secret.
	// Telemetry reads are not exposed on that listener in this mode.
	if svc.State().internalObs {
		svc.Internal(svc.State().Obs.Register)
	}

	svc.ReadinessProbe(probe.FromError(svc.State().Ready))
}
