package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
)

// ErrAcquireTimeout: no connection of the pool came free within
// pool.acquire_timeout; the caller's own deadline had not passed.
var ErrAcquireTimeout = errors.New("postgres: no free connection within acquire_timeout")

// bounded runs queries on the pool, waiting at most timeout for a free
// connection (0: the caller's context alone bounds the wait). It is the
// pool's whole query surface, so a transaction manager can run on it too.
// Its errors wrap pgx's (errors.Is(err, pgx.ErrNoRows) holds).
type bounded struct {
	pool     *pgxpool.Pool
	timeout  time.Duration
	timeouts metric.Int64Counter
	attrs    metric.MeasurementOption
}

// acquire takes a connection, the wait bounded by timeout.
func (b bounded) acquire(ctx context.Context) (*pgxpool.Conn, error) {
	if b.timeout <= 0 {
		conn, err := b.pool.Acquire(ctx)
		if err != nil {
			return nil, fmt.Errorf("postgres: acquire: %w", err)
		}

		return conn, nil
	}

	wait, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	conn, err := b.pool.Acquire(wait)
	if err == nil {
		return conn, nil
	}

	if ctx.Err() == nil && errors.Is(wait.Err(), context.DeadlineExceeded) {
		b.timeouts.Add(context.WithoutCancel(ctx), 1, b.attrs)

		return nil, fmt.Errorf("%w (%s): %w", ErrAcquireTimeout, b.timeout, err)
	}

	return nil, fmt.Errorf("postgres: acquire: %w", err)
}

func (b bounded) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	conn, err := b.acquire(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer conn.Release()

	tag, err := conn.Exec(ctx, sql, args...)
	if err != nil {
		return tag, fmt.Errorf("postgres: %w", err)
	}

	return tag, nil
}

func (b bounded) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	conn, err := b.acquire(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		conn.Release()

		return nil, fmt.Errorf("postgres: %w", err)
	}

	return &releasingRows{Rows: rows, conn: conn}, nil
}

func (b bounded) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	conn, err := b.acquire(ctx)
	if err != nil {
		return errRow{err: err}
	}

	return releasingRow{row: conn.QueryRow(ctx, sql, args...), conn: conn}
}

func (b bounded) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	conn, err := b.acquire(ctx)
	if err != nil {
		return errBatch{err: err}
	}

	return &releasingBatch{BatchResults: conn.SendBatch(ctx, batch), conn: conn}
}

func (b bounded) CopyFrom(
	ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource,
) (int64, error) {
	conn, err := b.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	n, err := conn.CopyFrom(ctx, table, columns, src)
	if err != nil {
		return n, fmt.Errorf("postgres: %w", err)
	}

	return n, nil
}

func (b bounded) Begin(ctx context.Context) (pgx.Tx, error) {
	return b.BeginTx(ctx, pgx.TxOptions{})
}

func (b bounded) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	conn, err := b.acquire(ctx)
	if err != nil {
		return nil, err
	}

	begun, err := conn.BeginTx(ctx, opts)
	if err != nil {
		conn.Release()

		return nil, fmt.Errorf("postgres: begin: %w", err)
	}

	return &releasingTx{Tx: begun, conn: conn}, nil
}

// releasingRows gives the connection back once the rows are done, as
// pgxpool's own rows do: on Close, or when Next runs out.
type releasingRows struct {
	pgx.Rows

	conn *pgxpool.Conn
	once sync.Once
}

func (r *releasingRows) Close() {
	r.Rows.Close()
	r.once.Do(r.conn.Release)
}

func (r *releasingRows) Next() bool {
	if r.Rows.Next() {
		return true
	}

	r.Close()

	return false
}

type releasingRow struct {
	row  pgx.Row
	conn *pgxpool.Conn
}

func (r releasingRow) Scan(dest ...any) error {
	defer r.conn.Release()

	if err := r.row.Scan(dest...); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}

	return nil
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

type releasingBatch struct {
	pgx.BatchResults

	conn *pgxpool.Conn
	once sync.Once
}

func (b *releasingBatch) Close() error {
	err := b.BatchResults.Close()
	b.once.Do(b.conn.Release)

	if err != nil {
		return fmt.Errorf("postgres: batch: %w", err)
	}

	return nil
}

type errBatch struct{ err error }

func (b errBatch) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, b.err }

func (b errBatch) Query() (pgx.Rows, error) { return nil, b.err }

func (b errBatch) QueryRow() pgx.Row { return errRow(b) }
func (b errBatch) Close() error      { return b.err }

// releasingTx gives the connection back when the transaction ends.
type releasingTx struct {
	pgx.Tx

	conn *pgxpool.Conn
	once sync.Once
}

func (t *releasingTx) Commit(ctx context.Context) error {
	err := t.Tx.Commit(ctx)
	t.once.Do(t.conn.Release)

	if err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}

	return nil
}

func (t *releasingTx) Rollback(ctx context.Context) error {
	err := t.Tx.Rollback(ctx)
	t.once.Do(t.conn.Release)

	if err != nil {
		return fmt.Errorf("postgres: rollback: %w", err)
	}

	return nil
}
