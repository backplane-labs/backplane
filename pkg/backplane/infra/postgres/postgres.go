// Package postgres is a PostgreSQL connection as a dependency: a
// configuration section, a pgx pool traced and measured through the
// service's OpenTelemetry providers, a query log, pgtx transactions, an
// optional schema and sqld migrations applied at start, a ping probe.
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    DB postgres.Config `json:"db"` // GREETER_DB_DSN, GREETER_DB_POOL_MAX_CONNS, ...
//	}
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//
//	db := deps.NewDependency(root, postgres.New(cfg.DB, postgres.Migrations(migrations)))
//	err := tx.DoSerializable(ctx, db.Get().Trm, func(ctx context.Context) error { ... })
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"

	"github.com/gopherex/pgtx"
	"github.com/gopherex/pgtx/pkg/tx"
	"github.com/gopherex/sqld/pkg/migrate"
	"github.com/gopherex/xlog"
	xlogpgx "github.com/gopherex/xlog/contrib/libs/pgx"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Errors of Config.
var (
	// ErrNoDSN: the section has no connection string.
	ErrNoDSN = errors.New("postgres: dsn is required")
	// ErrDSN: the connection string does not parse (not quoted: it may
	// hold a password).
	ErrDSN = errors.New("postgres: dsn does not parse")
)

// Config is the connection's section.
type Config struct {
	// DSN is a libpq URL or key=value string (secret: it may hold the
	// password).
	DSN config.Secret `json:"dsn,omitempty"`
	// QueryLog is the lowest level of the per-query log: trace, debug,
	// info, warn, error or none. Arguments are never logged.
	QueryLog string `json:"query_log" schemapb:"default=warn"`
	Pool     Pool   `json:"pool"`
}

// Pool bounds the pgx pool; a zero field is pgx's default. Set MaxConns
// explicitly in production: the default follows the node's CPUs.
type Pool struct {
	MaxConns        int64         `json:"max_conns"          schemapb:"default=0;gte=0"`
	MinConns        int64         `json:"min_conns"          schemapb:"default=0;gte=0"`
	MaxConnLifetime time.Duration `json:"max_conn_lifetime"  schemapb:"default=0s;gte=0"`
	MaxConnIdleTime time.Duration `json:"max_conn_idle_time" schemapb:"default=0s;gte=0"`
}

// Enabled reports whether a connection string is set.
func (c Config) Enabled() bool { return c.DSN.Reveal() != "" }

// Validate checks what the schema cannot: the connection string is set and
// parses, the query log level is known.
func (c Config) Validate() error {
	_, err := c.poolConfig()

	return err
}

func (c Config) poolConfig() (*pgxpool.Config, error) {
	if !c.Enabled() {
		return nil, ErrNoDSN
	}

	cfg, err := pgxpool.ParseConfig(c.DSN.Reveal())
	if err != nil {
		return nil, ErrDSN
	}

	if _, err := queryLogLevel(c.QueryLog); err != nil {
		return nil, err
	}

	if c.Pool.MaxConns > 0 {
		cfg.MaxConns = int32(min(c.Pool.MaxConns, 1<<31-1)) //nolint:gosec // bounded above
	}

	if c.Pool.MinConns > 0 {
		cfg.MinConns = int32(min(c.Pool.MinConns, 1<<31-1)) //nolint:gosec // bounded above
	}

	if c.Pool.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = c.Pool.MaxConnLifetime
	}

	if c.Pool.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = c.Pool.MaxConnIdleTime
	}

	return cfg, nil
}

func queryLogLevel(s string) (tracelog.LogLevel, error) {
	if s == "" {
		return tracelog.LogLevelWarn, nil
	}

	level, err := tracelog.LogLevelFromString(s)
	if err != nil {
		return 0, fmt.Errorf("postgres: query_log %q: %w", s, err)
	}

	return level, nil
}

// DB is the pool with its transaction manager. It is shared by pointer: it
// holds the pool.
type DB struct {
	// Pool for what DB does not cover (LISTEN, COPY).
	Pool *pgxpool.Pool
	// Trm owns transaction boundaries: tx.Do, tx.DoSerializable.
	Trm tx.Trm
	// DB routes to the context's transaction, else the pool; generated
	// queries run on it.
	DB tx.DB
}

// Ping checks the connection.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: ping: %w", err)
	}

	return nil
}

// Close releases the pool.
func (db *DB) Close() { db.Pool.Close() }

// Option configures Open and New.
type Option func(o *options)

type options struct {
	schema       string
	migrations   []fs.FS
	serializable bool
}

// Schema makes every connection run with search_path=name and creates the
// schema before migrating, so the migrations' bookkeeping lives there too.
func Schema(name string) Option { return func(o *options) { o.schema = name } }

// Migrations are applied at start in version order (sqld's migrate, under
// its advisory lock): directories of *.sql, e.g. an embed.FS.
func Migrations(fsys ...fs.FS) Option {
	return func(o *options) { o.migrations = append(o.migrations, fsys...) }
}

// Serializable makes serializable the transaction manager's default
// isolation (read committed without it).
func Serializable() Option { return func(o *options) { o.serializable = true } }

// Open connects, pings, creates the schema and applies the migrations.
func Open(ctx context.Context, log *xlog.Logger, cfg Config, opts ...Option) (*DB, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	poolCfg, err := cfg.poolConfig()
	if err != nil {
		return nil, err
	}

	if o.schema != "" {
		poolCfg.ConnConfig.RuntimeParams["search_path"] = o.schema
	}

	level, _ := queryLogLevel(cfg.QueryLog) // poolConfig checked it

	queryLog, err := xlogpgx.NewTracer(log, xlogpgx.WithLogLevel(level))
	if err != nil {
		return nil, fmt.Errorf("postgres: query log: %w", err)
	}

	queryLog.Logger = redacted{inner: queryLog.Logger}
	poolCfg.ConnConfig.Tracer = multitracer.New(otelpgx.NewTracer(), queryLog)

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: pool: %w", err)
	}

	db, err := prepare(ctx, pool, o)
	if err != nil {
		pool.Close()

		return nil, err
	}

	// Pool gauges: saturation does not show in per-query spans.
	if err := otelpgx.RecordStats(pool); err != nil {
		log.Warn("postgres: pool metrics unavailable", xlog.Err(err))
	}

	return db, nil
}

func prepare(ctx context.Context, pool *pgxpool.Pool, o options) (*DB, error) {
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	if o.schema != "" {
		if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{o.schema}.Sanitize()); err != nil {
			return nil, fmt.Errorf("postgres: create schema: %w", err)
		}
	}

	if len(o.migrations) > 0 {
		if err := migrate.Migrate(ctx, pool, o.migrations...); err != nil {
			return nil, fmt.Errorf("postgres: %w", err)
		}
	}

	isolation := tx.ReadCommitted()
	if o.serializable {
		isolation = tx.Serializable()
	}

	trm, err := pgtx.NewTxManager(pool, isolation)
	if err != nil {
		return nil, fmt.Errorf("postgres: tx manager: %w", err)
	}

	return &DB{Pool: pool, Trm: trm, DB: pgtx.NewTxDB(pool)}, nil
}

// New is the provider of the connection: Provide opens it (Open), the probe
// pings, Close releases the pool.
func New(cfg Config, opts ...Option) deps.Provider[*DB] { return provider{cfg: cfg, opts: opts} }

type provider struct {
	cfg  Config
	opts []Option
}

func (provider) Name() string { return "postgres" }

func (p provider) Provide(ctx context.Context, s deps.Scope) (*DB, error) {
	db, err := Open(ctx, s.Log(), p.cfg, p.opts...)
	if err != nil {
		return nil, err
	}

	host, name := db.Pool.Config().ConnConfig.Host, db.Pool.Config().ConnConfig.Database
	s.Log().Info("postgres ready", xlog.String("host", host), xlog.String("database", name))

	return db, nil
}

func (provider) Probe(ctx context.Context, db *DB) error { return db.Ping(ctx) }

func (provider) Close(_ context.Context, db *DB) error {
	db.Close()

	return nil
}

// redacted drops query arguments from the log: they may be secrets.
type redacted struct{ inner tracelog.Logger }

func (l redacted) Log(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]any) {
	safe := make(map[string]any, len(data))

	for key, value := range data {
		if key != "args" {
			safe[key] = value

			continue
		}

		if args, ok := value.([]any); ok {
			safe["arg_count"] = len(args)
		}
	}

	l.inner.Log(ctx, level, msg, safe)
}
