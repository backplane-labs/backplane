// Package api configures listeners and middleware served by the SDK.
package api

import (
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
)

//nolint:gochecknoinits // installs the private accessors for the SDK core
func init() {
	link.APIs.GRPC = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]GRPCOption)
		for _, o := range list {
			o.applyGRPC(&m)
		}

		return m
	}
	link.APIs.HTTP = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]HTTPOption)
		for _, o := range list {
			o.applyHTTP(&m)
		}

		return m
	}
	link.APIs.WS = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]WSProtoOption)
		for _, o := range list {
			o.applyWS(&m)
		}

		return m
	}
}

// Option interfaces, one per function.
type (
	// GRPCOption configures Service.GRPC.
	GRPCOption interface{ applyGRPC(m *routes.Managed) }
	// HTTPOption configures Service.HTTP and Service.GraphQL.
	HTTPOption interface{ applyHTTP(m *routes.Managed) }
	// WSProtoOption configures wsproto.Serve.
	WSProtoOption interface{ applyWS(m *routes.Managed) }
)

type listen struct{ name, addr string }

func (l listen) applyGRPC(m *routes.Managed) { m.Listener, m.Listen = l.name, l.addr }
func (l listen) applyHTTP(m *routes.Managed) { m.Listener, m.Listen = l.name, l.addr }
func (l listen) applyWS(m *routes.Managed)   { m.Listener, m.Listen = l.name, l.addr }

// ListenOption applies to managed routes.
type ListenOption interface {
	GRPCOption
	HTTPOption
	WSProtoOption
}

// Listen serves a managed route on its own listener, named in code, at addr
// (usually from configuration) instead of the shared public port. Routes on
// one listener share one server. The manifest names the listener; the
// instance state carries its port, so the manifest does not depend on addr.
// One name has one address.
func Listen(name, addr string) ListenOption { return listen{name: name, addr: addr} }

type host string

func (h host) applyGRPC(m *routes.Managed) { m.Host = string(h) }
func (h host) applyHTTP(m *routes.Managed) { m.Host = string(h) }
func (h host) applyWS(m *routes.Managed)   { m.Host = string(h) }

// HostOption applies to managed HTTP/ws-proto APIs and API declarations.
type HostOption interface {
	HTTPOption
	WSProtoOption
}

// Host matches the route by Host header in addition to its prefix: an exact
// host ("api.example.com") or a wildcard ("*.example.com"). Managed HTTP
// routes with one prefix and different hosts share a port.
func Host(h string) HostOption { return host(h) }
