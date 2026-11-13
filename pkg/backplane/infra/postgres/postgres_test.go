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

	for name, c := range map[string]struct {
		cfg  postgres.Config
		want error
	}{
		"exec mode":     {postgres.Config{DSN: "postgres://pg/db", ExecMode: "fast"}, postgres.ErrExecMode},
		"bad listen":    {postgres.Config{DSN: "postgres://pg/db", Listen: postgres.Listen{DSN: "postgres://%zz"}}, postgres.ErrDSN},
		"simple":        {postgres.Config{DSN: "postgres://pg/db", ExecMode: "simple_protocol"}, nil},
		"direct listen": {postgres.Config{DSN: "postgres://pgbouncer/db", Listen: postgres.Listen{DSN: "postgres://pg/db"}}, nil},
	} {
		if err := c.cfg.Validate(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// live opens BACKPLANE_TEST_PG with cfg's knobs; skipped without.
func live(t *testing.T, cfg postgres.Config) *postgres.DB {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	cfg.DSN = config.Secret(dsn)
	h := backplanetest.New(t)
	db := deps.NewDependency(h.Root(), postgres.New(cfg))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return db.Get()
}

// A configured pool is what the section says, not what the node's CPUs
// would give; server settings reach every connection.
func TestPoolFollowsTheSection(t *testing.T) {
	t.Parallel()

	db := live(t, postgres.Config{
		Pool:             postgres.Pool{MaxConns: 3},
		StatementTimeout: 1500 * time.Millisecond, IdleInTransactionSessionTimeout: time.Minute,
		ExecMode: "exec",
	})

	got := db.Pool.Config()
	if got.MaxConns != 3 || got.MaxConnLifetime != time.Hour || got.MaxConnLifetimeJitter != 5*time.Minute ||
		got.HealthCheckPeriod != time.Minute || got.ConnConfig.ConnectTimeout != 5*time.Second ||
		got.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatalf("pool config = %+v", got)
	}

	if listen := db.ListenPool().Config(); listen.MaxConns != 2 {
		t.Fatalf("listen pool max = %d", listen.MaxConns)
	}

	var statement, idle string
	if err := db.DB.QueryRow(t.Context(), "SELECT current_setting('statement_timeout'), "+
		"current_setting('idle_in_transaction_session_timeout')").Scan(&statement, &idle); err != nil {
		t.Fatal(err)
	}

	if statement != "1500ms" || idle != "1min" {
		t.Fatalf("server settings: statement_timeout %q, idle_in_transaction_session_timeout %q", statement, idle)
	}
}

// Chaos: every connection is taken. A query, a batch and a transaction
// give up after acquire_timeout with ErrAcquireTimeout instead of waiting
// for the caller's deadline; the listen pool is unaffected; once a
// connection is free everything runs and gives it back.
func TestAcquireTimeout(t *testing.T) {
	t.Parallel()

	db := live(t, postgres.Config{Pool: postgres.Pool{MaxConns: 1, AcquireTimeout: 200 * time.Millisecond}})
	ctx := t.Context()

	held, err := db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}

	bounded := map[string]func() error{
		"exec": func() error { _, err := db.DB.Exec(ctx, "SELECT 1"); return err },
		"query": func() error {
			rows, err := db.DB.Query(ctx, "SELECT 1")
			if err == nil {
				rows.Close()
			}

			return err
		},
		"row": func() error { var n int; return db.DB.QueryRow(ctx, "SELECT 1").Scan(&n) },
		"transaction": func() error {
			return tx.DoSerializable(ctx, db.Trm, func(ctx context.Context) error { _, err := db.DB.Exec(ctx, "SELECT 1"); return err })
		},
	}

	for name, call := range bounded {
		began := time.Now()

		if err := call(); !errors.Is(err, postgres.ErrAcquireTimeout) {
			t.Fatalf("%s while the pool is taken: %v", name, err)
		}

		if took := time.Since(began); took > 2*time.Second {
			t.Fatalf("%s waited %v", name, took)
		}
	}

	// Listeners have their own connections.
	if _, err := db.ListenPool().Exec(ctx, "LISTEN infra_test"); err != nil {
		t.Fatalf("listen pool while the queries' pool is taken: %v", err)
	}

	held.Release()

	var n int

	rows, err := db.DB.Query(ctx, "SELECT generate_series(1, 3)")
	if err != nil {
		t.Fatal(err)
	}

	for rows.Next() {
		n++
	}

	results := db.DB.SendBatch(ctx, &pgx.Batch{QueuedQueries: []*pgx.QueuedQuery{{SQL: "SELECT 1"}}})
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}

	err = tx.DoSerializable(ctx, db.Trm, func(ctx context.Context) error { _, err := db.DB.Exec(ctx, "SELECT 1"); return err })
	if err != nil || n != 3 {
		t.Fatalf("after release: %d rows, %v", n, err)
	}

	// Rows run out, the batch closed, the transaction ended: nothing is held.
	if acquired := db.Pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("%d connections still held", acquired)
	}

	if err := db.DB.QueryRow(ctx, "SELECT 1 WHERE false").Scan(&n); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("no rows: %v", err)
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
