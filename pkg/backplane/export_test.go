package backplane

import (
	"os"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// WithSignals replaces SIGINT/SIGTERM with ch.
func WithSignals(ch <-chan os.Signal) Option { return func(o *options) { o.signals = ch } }

// WithExit replaces os.Exit with fn.
func WithExit(fn func(code int)) Option { return func(o *options) { o.exit = fn } }

// SystemNodes names the service root's children in start order.
func SystemNodes[St any](s *Service[St]) []string {
	children := s.svc.Children()
	names := make([]string, 0, len(children))

	for _, n := range children {
		names = append(names, n.Name())
	}

	return names
}

// NodeStatuses is what the instance state reports for the author's tree.
func NodeStatuses[St any](s *Service[St]) []*backplanev1.NodeStatus { return s.nodes() }

// Listeners is what the instance state reports for the route listeners.
func Listeners[St any](s *Service[St]) map[string]uint16 { return s.listeners() }

// TransportStatuses is what the instance state reports for the transports.
func TransportStatuses[St any](s *Service[St]) []*backplanev1.TransportStatus { return s.transports() }

// ProbeKind is probeKind.
func ProbeKind(args []string) (string, bool, error) { return probeKind(args) }

// KeepaliveParams is keepaliveParams.
var KeepaliveParams = keepaliveParams
