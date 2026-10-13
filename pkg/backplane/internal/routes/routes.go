// Package routes holds what route options configure, for the SDK core.
package routes

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/grpc"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/apifiles"
)

// Errors.
var (
	// ErrPrefix: an HTTP path prefix the SDK cannot announce consistently.
	ErrPrefix = errors.New("route: bad path prefix")
)

// Managed is a route the SDK serves.
type Managed struct {
	Listen      string
	Host        string
	OpenAPI     []byte // HTTP
	SchemaFiles *SchemaFiles
	// gRPC and ws-proto: interceptors of this registration only.
	Unary  []grpc.UnaryServerInterceptor
	Stream []grpc.StreamServerInterceptor
	// HTTP and ws-proto: wraps this route's handler; the first is outermost.
	Middleware []func(http.Handler) http.Handler
	// gRPC: serve grpc.reflection on the route's port.
	Reflection bool
}

// SchemaFiles is a file snapshot and its manifest reference (or a declaration error).
type SchemaFiles struct {
	Files *apifiles.Files
	Ref   *backplanev1.APISchemaBundle
	Err   error
}

// Wrap applies the middleware to h, the first one outermost.
func (m Managed) Wrap(h http.Handler) http.Handler {
	for i := len(m.Middleware) - 1; i >= 0; i-- {
		h = m.Middleware[i](h)
	}

	return h
}

// Prefix normalizes an HTTP path prefix for the service HTTP mux: it
// must start with "/", carry no method, host or wildcard, and ends with "/"
// ("/api" serves "/api/..." and redirects "/api").
func Prefix(p string) (string, error) {
	switch {
	case !strings.HasPrefix(p, "/"):
		return "", fmt.Errorf("%w %q: must start with /", ErrPrefix, p)
	case strings.ContainsAny(p, " \t{}"):
		return "", fmt.Errorf("%w %q: no method, host or wildcard, only a path", ErrPrefix, p)
	case !strings.HasSuffix(p, "/"):
		p += "/"
	}

	return p, nil
}
