// Package web holds hello's HTTP-facing pieces: the /hello/ handler, a
// tiny GraphQL endpoint, the middleware and the gRPC interceptors its
// routes are declared with.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
)

// Hello serves a greeting using the service's current configuration.
func Hello(g *greeter.Greeter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		name := r.URL.Query().Get("name")
		if name == "" {
			name = "world"
		}

		text, err := g.Text(r.Context(), name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		fmt.Fprintln(w, text) //nolint:gosec // text/plain, not HTML
	})
}

// query is the one query the endpoint answers: { greeting(name: "...") }.
var query = regexp.MustCompile(`^\s*(?:query\s*)?\{\s*greeting\s*\(\s*name\s*:\s*"([^"]*)"\s*\)\s*\}\s*$`)

type (
	gqlRequest struct {
		Query string `json:"query"`
	}
	gqlError struct {
		Message string `json:"message"`
	}
	gqlResponse struct {
		Data   map[string]string `json:"data,omitempty"`
		Errors []gqlError        `json:"errors,omitempty"`
	}
)

// GraphQL is a tiny GraphQL endpoint: POST {"query": "{ greeting(name: \"x\") }"}.
func GraphQL(g *greeter.Greeter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var req gqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, http.StatusBadRequest, gqlResponse{Errors: []gqlError{{Message: "bad request: " + err.Error()}}})

			return
		}

		m := query.FindStringSubmatch(req.Query)
		if m == nil {
			reply(w, http.StatusOK, gqlResponse{Errors: []gqlError{{Message: `only { greeting(name: "...") } is served`}}})

			return
		}

		text, err := g.Text(r.Context(), m[1])
		if err != nil {
			reply(w, http.StatusOK, gqlResponse{Errors: []gqlError{{Message: err.Error()}}})

			return
		}

		reply(w, http.StatusOK, gqlResponse{Data: map[string]string{"greeting": text}})
	})
}

func reply(w http.ResponseWriter, status int, body gqlResponse) {
	b, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// ServedBy is HTTP middleware naming the service in a response header.
func ServedBy(service string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Served-By", service)
			next.ServeHTTP(w, r)
		})
	}
}

// Unary logs every unary call of the services it wraps at debug.
func Unary(log *xlog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		start := time.Now()
		res, err := next(ctx, req)
		log.Ctx().Debug(ctx, "unary call", xlog.String("method", info.FullMethod),
			xlog.Duration("took", time.Since(start)), xlog.Err(err))

		return res, err
	}
}

// Stream logs every streaming call of the services it wraps at debug.
func Stream(log *xlog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		start := time.Now()
		err := next(srv, ss)
		log.Debug("stream call", xlog.String("method", info.FullMethod),
			xlog.Duration("took", time.Since(start)), xlog.Err(err))

		return err
	}
}
