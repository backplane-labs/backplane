// Package legacy is a listener hello serves itself, outside the SDK's
// public port: the service announces it with a declarative route
// (svc.API(api.HTTP(...))) so its API appears in the catalog.
package legacy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gopherex/backplane/pkg/backplane/deps"
)

const readHeaderTimeout = 5 * time.Second

// Prefix is what the legacy listener serves.
const Prefix = "/legacy/"

// Config of the listener.
type Config struct {
	// Listen address. Keep its port stable across restarts and replicas:
	// declarative routes are part of the immutable per-version manifest.
	Listen string `json:"listen" schemapb:"default=:8083"`
}

// Server is a component owning its own listener, open from New so that the
// route can name its port before Run.
type Server struct {
	deps.Component

	ln  net.Listener
	srv *http.Server
}

// New listens on cfg.Listen and serves from the start of the tree to its
// stop.
func New(parent deps.Scope, cfg *Config) (*Server, error) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("legacy: listen: %w", err)
	}

	s := &Server{Component: deps.NewComponent(parent, "legacy"), ln: ln}

	mux := http.NewServeMux()
	mux.HandleFunc(Prefix, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "served by hello's own listener")
	})
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}

	s.Go(func(context.Context) error {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("legacy: serve: %w", err)
		}

		return nil
	})
	// The stop shuts the server down; Serve then returns and the stop's
	// wait for the goroutine ends.
	s.OnStop(func(ctx context.Context) error {
		if err := s.srv.Shutdown(ctx); err != nil {
			return fmt.Errorf("legacy: shutdown: %w", err)
		}

		_ = ln.Close()

		return nil
	})

	return s, nil
}

// Port is the port the listener got.
func (s *Server) Port() uint16 {
	if a, ok := s.ln.Addr().(*net.TCPAddr); ok {
		return uint16(a.Port) //nolint:gosec // a TCP port fits
	}

	return 0
}
