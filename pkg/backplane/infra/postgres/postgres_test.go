package postgres_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gopherex/pgtx/pkg/tx"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		cfg  postgres.Config
		want error
	}{
		"no dsn":    {postgres.Config{}, postgres.ErrNoDSN},
		"bad dsn":   {postgres.Config{DSN: "postgres://%zz"}, postgres.ErrDSN},
		"query log": {postgres.Config{DSN: "postgres://pg/db", QueryLog: "loud"}, nil},
	} {
		err := c.cfg.Validate()
		if err == nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	if err := (postgres.Config{DSN: "postgres://pg/db", QueryLog: "none"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestProvider opens BACKPLANE_TEST_PG in a schema of its own, migrates it
// and runs a transaction; skipped without.
func TestProvider(t *testing.T) {
	t.Parallel()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	schema := "infra_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	h := backplanetest.New(t)
	db := deps.NewDependency(h.Root(), postgres.New(postgres.Config{DSN: config.Secret(dsn)},
		postgres.Schema(schema), postgres.Migrations(os.DirFS("testdata/migrations")), postgres.Serializable()))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = db.Get().Pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	})

	ctx := t.Context()

	err := tx.DoSerializable(ctx, db.Get().Trm, func(ctx context.Context) error {
		_, err := db.Get().DB.Exec(ctx, "INSERT INTO greeting (name) VALUES ('world')")

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.Get().Pool.QueryRow(ctx, "SELECT count(*) FROM greeting").Scan(&n); err != nil || n != 1 {
		t.Fatalf("count %d: %v", n, err)
	}
}

// Queries are spans only under an operation (otelpgx traces only under a
// recording span): a query from a context without a span starts no trace,
// one under an operation is its child.
//
//nolint:paralleltest // sets the global tracer provider the pool reads
func TestQuerySpansOnlyUnderAnOperation(t *testing.T) {
	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	rec := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()

	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	h := backplanetest.New(t)
	db := deps.NewDependency(h.Root(), postgres.New(postgres.Config{DSN: config.Secret(dsn)}))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Get().Pool.Exec(t.Context(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}

	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("queries without an operation made %d spans", n)
	}

	ctx, operation := provider.Tracer("test").Start(t.Context(), "operation")
	if _, err := db.Get().Pool.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}

	operation.End()

	children := 0

	for _, s := range rec.Ended() {
		if s.Parent().SpanID() == operation.SpanContext().SpanID() {
			children++
		}
	}

	if children == 0 {
		t.Fatal("the query under the operation made no span")
	}
}
