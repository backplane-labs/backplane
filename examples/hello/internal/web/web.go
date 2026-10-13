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

	"github.com/gopherex/backplane/examples/hello/internal/flows"
	"github.com/gopherex/backplane/examples/hello/internal/greeter"
	"github.com/gopherex/backplane/pkg/backplane/hook"
)

// GreetTimeout bounds the hook call of the /hello/ handler.
const GreetTimeout = 2 * time.Second

// IdempotencyKey is the request header that makes a greeting idempotent:
// the hook call runs once per key (hook.Key).
const IdempotencyKey = "Idempotency-Key"

// Hello serves /hello/?name=: a bound Greet hook answers first; the local
// greeter is the fallback when there is no Temporal, no binding or no
// answer in time.
func Hello(greet hook.Ref[flows.GreetIn, flows.GreetOut], g *greeter.Greeter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		name := r.URL.Query().Get("name")
		if name == "" {
			name = "world"
		}

		opts := []hook.CallOption{hook.Timeout(GreetTimeout)}
		if k := r.Header.Get(IdempotencyKey); k != "" {
			opts = append(opts, hook.Key(k))
		}

		if out, err := greet.Call(r.Context(), flows.GreetIn{Name: name}, opts...); err == nil {
			g.Record(r.Context(), name, out.Text)
			fmt.Fprintln(w, out.Text)

			return
		}

		text, err := g.Text(r.Context(), name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		fmt.Fprintln(w, text) //nolint:gosec // text/plain, not HTML
	})
}

// Introspection is the GraphQL endpoint's introspection result, announced
// with the api.
//
//nolint:gochecknoglobals // constant document
var Introspection = []byte(`{"__schema":{"queryType":{"name":"Query"},"types":[` +
	`{"kind":"OBJECT","name":"Query","description":"One greeting query with an inline name argument.",` +
	`"fields":[{"name":"greeting","description":"Returns a local greeting. ` +
	`GraphQL errors use the errors array with HTTP 200.",` +
	`"args":[{"name":"name","description":"Name as an inline string literal.",` +
	`"type":{"kind":"NON_NULL","ofType":` +
	`{"kind":"SCALAR","name":"String"}}}],"type":{"kind":"SCALAR","name":"String"}}]},` +
	`{"kind":"SCALAR","name":"String","description":"UTF-8 text."}]}}`)

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
