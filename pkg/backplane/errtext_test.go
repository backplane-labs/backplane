package backplane_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// records keeps what the logs pipeline exports.
type records struct {
	mu   sync.Mutex
	text []string
}

func (r *records) Export(_ context.Context, rs []sdklog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, rec := range rs {
		var b strings.Builder

		_, _ = fmt.Fprint(&b, rec.Body())
		rec.WalkAttributes(func(kv attribute.KeyValue) bool {
			b.WriteString(" " + string(kv.Key) + "=" + kv.Value.String())

			return true
		})
		r.text = append(r.text, b.String())
	}

	return nil
}

func (*records) Shutdown(context.Context) error   { return nil }
func (*records) ForceFlush(context.Context) error { return nil }

// secretErrors are real errors whose text carries what must not leave the
// service: a signed URL's path and query, a row value in a PostgreSQL
// error's detail (BACKPLANE_TEST_PG; skipped without).
func secretErrors(t *testing.T) map[string]error {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://127.0.0.1:1/secret-path/file?X-Amz-Signature=secret-signature", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	_, urlErr := http.DefaultClient.Do(req)

	var ue *url.Error
	if !errors.As(urlErr, &ue) {
		t.Fatalf("not a url.Error: %v", urlErr)
	}

	out := map[string]error{"url": urlErr}

	if dsn := os.Getenv("BACKPLANE_TEST_PG"); dsn != "" {
		conn, err := pgx.Connect(t.Context(), dsn)
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = conn.Close(context.Background()) })

		_, _ = conn.Exec(t.Context(), "CREATE TEMP TABLE secrets (v text PRIMARY KEY)")
		_, _ = conn.Exec(t.Context(), "INSERT INTO secrets VALUES ('secret-row-value')")
		_, pgErr := conn.Exec(t.Context(), "INSERT INTO secrets VALUES ('secret-row-value')")

		var pe *pgconn.PgError
		if !errors.As(pgErr, &pe) || !strings.Contains(pe.Detail, "secret-row-value") {
			t.Fatalf("not a PgError with the value: %v", pgErr)
		}

		out["pg"] = pgErr
	}

	return out
}

// An error logged by the service's root logger or recorded by a
// component's span leaves without the URL's path and query or the row
// values; errors.Is/As on the error itself still work. The global logs and
// traces providers the roots export through are installed after
// t.Parallel: the serial tests (TestStopWithHungCollector installs real
// ones) are over by then, and no parallel test installs any.
func TestRootsRenderErrors(t *testing.T) {
	t.Parallel()

	exported := &records{}
	global.SetLoggerProvider(sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exported))))

	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))

	log := backplane.NewLogger(backplane.Identity{Service: "svc"}, nil, config.Backplane{LogLevel: config.LiveOf("info")})
	h := backplanetest.New(t)

	for name, err := range secretErrors(t) {
		log.Error("failed", xlog.Err(err))
		_ = h.Root().Span(t.Context(), "op-"+name, func(context.Context) error { return err })
	}

	var leaked []string

	secret := func(text string) bool {
		for _, v := range []string{"secret-row-value", "secret-signature", "secret-path"} {
			if strings.Contains(text, v) {
				return true
			}
		}

		return false
	}

	exported.mu.Lock()
	for _, text := range exported.text {
		if secret(text) {
			leaked = append(leaked, "log: "+text)
		}
	}
	exported.mu.Unlock()

	for _, s := range spans.Ended() {
		if secret(s.Status().Description) {
			leaked = append(leaked, "span status: "+s.Status().Description)
		}

		for _, e := range s.Events() {
			for _, a := range e.Attributes {
				if secret(a.Value.String()) {
					leaked = append(leaked, "span event: "+a.Value.String())
				}
			}
		}
	}

	if len(leaked) > 0 {
		t.Fatalf("secrets left the service:\n%s", strings.Join(leaked, "\n"))
	}

	if len(exported.text) == 0 || len(spans.Ended()) == 0 {
		t.Fatalf("nothing exported: %d records, %d spans", len(exported.text), len(spans.Ended()))
	}
}

func TestRenderError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "plain", err: errors.New("boom"), want: "boom"},
		{
			name: "url error keeps scheme and host",
			err:  &url.Error{Op: "Get", URL: "https://s3.example.com/bucket/key?X-Amz-Signature=secret", Err: errors.New("timeout")},
			want: `Get "https://s3.example.com": timeout`,
		},
		{
			name: "postgres error keeps its code",
			err: fmt.Errorf("insert user: %w", &pgconn.PgError{
				Code: "23505", Message: "duplicate key", Detail: "Key (email)=(a@b.c) already exists",
			}),
			want: "insert user: SQLSTATE 23505",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := backplane.RenderError(tc.err); got != tc.want {
				t.Fatalf("RenderError = %q, want %q", got, tc.want)
			}
		})
	}
}

func BenchmarkRenderError(b *testing.B) {
	err := fmt.Errorf("insert user: %w", &pgconn.PgError{Code: "23505", Detail: "Key (email)=(a@b.c) already exists"})

	for b.Loop() {
		_ = backplane.RenderError(err)
	}
}
