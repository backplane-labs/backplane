// Package link lets SDK packages reach what public types keep private: the
// node behind a deps.Scope. deps installs the accessor at init.
package link

import (
	"net/http"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/configrt"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
)

// NodeOf returns the node a deps.Scope wraps, or nil for a zero scope.
//
//nolint:gochecknoglobals // set once by deps at init
var NodeOf func(scope any) *node.Node

// Scope wraps a node as a deps.Component; the SDK uses it for the root.
//
//nolint:gochecknoglobals // set once by deps at init
var Scope func(n *node.Node) any

// ConfigState returns the SDK view of a *config.Runtime[C].
//
//nolint:gochecknoglobals // set once by config at init
var ConfigState func(rt any) configrt.State

// SetLive sets a *config.Live[T] to v (backplanetest).
//
//nolint:gochecknoglobals // set once by config at init
var SetLive func(live, v any)

// APIs extracts what the public api package keeps private.
//
//nolint:gochecknoglobals // set once by api at init
var APIs struct {
	Decl  func(d any) (*backplanev1.API, error)
	Files func(d any) *routes.SchemaFiles
	GRPC  func(opts any) routes.Managed
	HTTP  func(opts any) routes.Managed
	WS    func(opts any) routes.Managed
}

// MountWS serves a ws-proto endpoint on a *backplane.Service.
//
//nolint:gochecknoglobals // set once by backplane at init
var MountWS func(svc any, prefix string, h http.Handler, services []string, spec routes.Managed)
