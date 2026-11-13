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
	"math"
	"strconv"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

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
	// ErrExecMode: exec_mode names no pgx query execution mode.
	ErrExecMode = errors.New(
		"postgres: exec_mode: want cache_statement, cache_describe, describe_exec, exec or simple_protocol")
)

// Defaults of the section: explicit, never derived from the node's CPUs. A
// zero field of a Config built by hand reads its default too.
const (
	DefaultConnectTimeout           = 5 * time.Second
	DefaultExecMode                 = "cache_statement"
	DefaultStatementCacheCapacity   = 512
	DefaultDescriptionCacheCapacity = 512
	DefaultMaxConns                 = 10
	DefaultListenMaxConns           = 2
	DefaultMaxConnLifetime          = time.Hour
	DefaultMaxConnLifetimeJitter    = 5 * time.Minute
	DefaultMaxConnIdleTime          = 30 * time.Minute
	DefaultHealthCheckPeriod        = time.Minute
	DefaultAcquireTimeout           = 5 * time.Second
)

// Config is the connection's section.
type Config struct {
	// DSN is a libpq URL or key=value string (secret: it may hold the
	// password).
	DSN config.Secret `desc:"libpq URL or key=value string." json:"dsn,omitempty"`
	// QueryLog is the lowest level of the per-query log: trace, debug,
	// info, warn, error or none. Arguments are never logged.
	QueryLog string `desc:"Lowest level of the per-query log: trace, debug, info, warn, error or none. Arguments are never logged." json:"query_log" schemapb:"default=warn"`
	// ConnectTimeout bounds establishing one connection.
	ConnectTimeout time.Duration `desc:"Bound of establishing one connection." json:"connect_timeout" schemapb:"default=5s;gt=0"`
	// StatementTimeout and IdleInTransactionSessionTimeout are the server
	// settings every connection starts with; 0: the server's own.
	StatementTimeout                time.Duration `desc:"statement_timeout of every connection; 0: the server's."                   json:"statement_timeout"                   schemapb:"default=0s;gte=0"`
	IdleInTransactionSessionTimeout time.Duration `desc:"idle_in_transaction_session_timeout of every connection; 0: the server's." json:"idle_in_transaction_session_timeout" schemapb:"default=0s;gte=0"`
	// ExecMode is how queries run: cache_statement (prepared and cached),
	// cache_describe, describe_exec, exec or simple_protocol; the last
	// three, and cache_describe, work behind a transaction-mode pooler.
	ExecMode                 string `desc:"Query execution: cache_statement, cache_describe, describe_exec, exec or simple_protocol." json:"exec_mode"                  schemapb:"default=cache_statement"`
	StatementCacheCapacity   int64  `desc:"Prepared statements cached per connection (cache_statement)."                              json:"statement_cache_capacity"   schemapb:"default=512;gte=0"`
	DescriptionCacheCapacity int64  `desc:"Statement descriptions cached per connection (cache_describe)."                            json:"description_cache_capacity" schemapb:"default=512;gte=0"`
	Pool                     Pool   `desc:"Connection pool of the queries."                                                           json:"pool"`
	// Listen is the separate small pool of LISTEN/NOTIFY sessions
	// (DB.ListenPool): listeners never take the queries' connections.
	Listen Listen `desc:"Separate pool of LISTEN sessions." json:"listen"`
}

// Pool bounds the queries' pool.
type Pool struct {
	MaxConns              int64         `desc:"Most open connections."                                                                json:"max_conns"                schemapb:"default=10;gt=0"`
	MinConns              int64         `desc:"Connections kept open."                                                                json:"min_conns"                schemapb:"default=0;gte=0"`
	MinIdleConns          int64         `desc:"Idle connections kept open."                                                           json:"min_idle_conns"           schemapb:"default=0;gte=0"`
	MaxConnLifetime       time.Duration `desc:"A connection this old is closed."                                                      json:"max_conn_lifetime"        schemapb:"default=1h;gt=0"`
	MaxConnLifetimeJitter time.Duration `desc:"Random extra lifetime, so connections do not all close at once."                       json:"max_conn_lifetime_jitter" schemapb:"default=5m;gte=0"`
	MaxConnIdleTime       time.Duration `desc:"A connection idle this long is closed."                                                json:"max_conn_idle_time"       schemapb:"default=30m;gt=0"`
	HealthCheckPeriod     time.Duration `desc:"How often idle connections are checked."                                               json:"health_check_period"      schemapb:"default=1m;gt=0"`
	AcquireTimeout        time.Duration `desc:"Longest wait for a free connection, then ErrAcquireTimeout; 0: the caller's deadline." json:"acquire_timeout"          schemapb:"default=5s;gte=0"`
}

// Listen is the LISTEN sessions' pool.
type Listen struct {
	// DSN is a direct connection that bypasses a transaction-mode pooler;
	// empty: the section's dsn.
	DSN                   config.Secret `desc:"Direct connection for LISTEN, bypassing a pooler; empty: dsn." json:"dsn,omitempty"`
	MaxConns              int64         `desc:"Most open listening connections."                              json:"max_conns"                schemapb:"default=2;gt=0"`
	MaxConnLifetime       time.Duration `desc:"A connection this old is closed."                              json:"max_conn_lifetime"        schemapb:"default=1h;gt=0"`
	MaxConnLifetimeJitter time.Duration `desc:"Random extra lifetime."                                        json:"max_conn_lifetime_jitter" schemapb:"default=5m;gte=0"`
	MaxConnIdleTime       time.Duration `desc:"A connection idle this long is closed."                        json:"max_conn_idle_time"       schemapb:"default=30m;gt=0"`
	HealthCheckPeriod     time.Duration `desc:"How often idle connections are checked."                       json:"health_check_period"      schemapb:"default=1m;gt=0"`
}

// Enabled reports whether a connection string is set.
func (c Config) Enabled() bool { return c.DSN.Reveal() != "" }

// Validate checks what the schema cannot: the connection strings are set
// and parse, the query log level and the exec mode are known.
func (c Config) Validate() error {
	if _, err := c.poolConfig(); err != nil {
		return err
	}

	_, err := c.listenConfig()

	return err
}

// orDefault is v, or def when v is zero (a Config built by hand).
func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}

	return v
}

// conns is n as pgx's int32, bounded to its range.
func conns(n int64) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}

// connConfig parses dsn with the section's connection settings.
func (c Config) connConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, ErrDSN
	}

	if _, err := queryLogLevel(c.QueryLog); err != nil {
		return nil, err
	}

	mode, err := execMode(orDefault(c.ExecMode, DefaultExecMode))
	if err != nil {
		return nil, err
	}

	cc := cfg.ConnConfig
	cc.ConnectTimeout = orDefault(c.ConnectTimeout, DefaultConnectTimeout)
	cc.DefaultQueryExecMode = mode
	cc.StatementCacheCapacity = int(orDefault(c.StatementCacheCapacity, DefaultStatementCacheCapacity))
	cc.DescriptionCacheCapacity = int(orDefault(c.DescriptionCacheCapacity, DefaultDescriptionCacheCapacity))

	if c.StatementTimeout > 0 {
		cc.RuntimeParams["statement_timeout"] = strconv.FormatInt(c.StatementTimeout.Milliseconds(), 10)
	}

	if c.IdleInTransactionSessionTimeout > 0 {
		cc.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(
			c.IdleInTransactionSessionTimeout.Milliseconds(), 10)
	}

	return cfg, nil
}

func (c Config) poolConfig() (*pgxpool.Config, error) {
	if !c.Enabled() {
		return nil, ErrNoDSN
	}

	cfg, err := c.connConfig(c.DSN.Reveal())
	if err != nil {
		return nil, err
	}

	p := c.Pool
	cfg.MaxConns = conns(orDefault(p.MaxConns, DefaultMaxConns))
	cfg.MinConns = conns(p.MinConns)
	cfg.MinIdleConns = conns(p.MinIdleConns)
	cfg.MaxConnLifetime = orDefault(p.MaxConnLifetime, DefaultMaxConnLifetime)
	cfg.MaxConnLifetimeJitter = orDefault(p.MaxConnLifetimeJitter, DefaultMaxConnLifetimeJitter)
	cfg.MaxConnIdleTime = orDefault(p.MaxConnIdleTime, DefaultMaxConnIdleTime)
	cfg.HealthCheckPeriod = orDefault(p.HealthCheckPeriod, DefaultHealthCheckPeriod)

	return cfg, nil
}

// listenConfig is the LISTEN pool: listen.dsn, else dsn.
func (c Config) listenConfig() (*pgxpool.Config, error) {
	dsn := orDefault(c.Listen.DSN, c.DSN).Reveal()
	if dsn == "" {
		return nil, ErrNoDSN
	}

	cfg, err := c.connConfig(dsn)
	if err != nil {
		return nil, err
	}

	l := c.Listen
	cfg.MaxConns = conns(orDefault(l.MaxConns, DefaultListenMaxConns))
	cfg.MaxConnLifetime = orDefault(l.MaxConnLifetime, DefaultMaxConnLifetime)
	cfg.MaxConnLifetimeJitter = orDefault(l.MaxConnLifetimeJitter, DefaultMaxConnLifetimeJitter)
	cfg.MaxConnIdleTime = orDefault(l.MaxConnIdleTime, DefaultMaxConnIdleTime)
	cfg.HealthCheckPeriod = orDefault(l.HealthCheckPeriod, DefaultHealthCheckPeriod)

	return cfg, nil
}

func execMode(name string) (pgx.QueryExecMode, error) {
	switch name {
	case "cache_statement":
		return pgx.QueryExecModeCacheStatement, nil
	case "cache_describe":
		return pgx.QueryExecModeCacheDescribe, nil
	case "describe_exec":
		return pgx.QueryExecModeDescribeExec, nil
	case "exec":
		return pgx.QueryExecModeExec, nil
	case "simple_protocol":
		return pgx.QueryExecModeSimpleProtocol, nil
	default:
		return 0, ErrExecMode
	}
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
// holds the pools.
type DB struct {
	// Pool for what DB does not cover (COPY, a raw connection). Its own
	// Acquire is not bounded by pool.acquire_timeout; DB's queries are.
	Pool *pgxpool.Pool
	// Trm owns transaction boundaries: tx.Do, tx.DoSerializable.
	Trm tx.Trm
	// DB routes to the context's transaction, else the pool; generated
	// queries run on it, waiting at most pool.acquire_timeout for a free
	// connection (ErrAcquireTimeout).
	DB tx.DB

	listen *pgxpool.Pool
}

// ListenPool is the separate small pool of LISTEN/NOTIFY sessions (on
// listen.dsn, else dsn): listeners never take the queries' connections.
// It connects on first use.
func (db *DB) ListenPool() *pgxpool.Pool { return db.listen }

// Ping checks both pools.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: ping: %w", err)
	}

	if err := db.listen.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: ping listen pool: %w", err)
	}

	return nil
}

// Close releases the pools.
func (db *DB) Close() {
	db.listen.Close()
	db.Pool.Close()
}

// Option configures Open and New.
type Option func(o *options)

type options struct {
	schema       string
	migrations   []fs.FS
	serializable bool
	name         string
}

// Name labels the pools' metrics (attribute pool; the listen pool's is
// <name>.listen); New sets the dependency's name. Default "postgres".
func Name(name string) Option { return func(o *options) { o.name = name } }

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
	o := options{name: "postgres"}
	for _, opt := range opts {
		opt(&o)
	}

	poolCfg, err := cfg.poolConfig()
	if err != nil {
		return nil, err
	}

	listenCfg, err := cfg.listenConfig()
	if err != nil {
		return nil, err
	}

	level, _ := queryLogLevel(cfg.QueryLog) // poolConfig checked it

	queryLog, err := xlogpgx.NewTracer(log, xlogpgx.WithLogLevel(level))
	if err != nil {
		return nil, fmt.Errorf("postgres: query log: %w", err)
	}

	queryLog.Logger = redacted{inner: queryLog.Logger}

	for _, c := range []*pgxpool.Config{poolCfg, listenCfg} {
		if o.schema != "" {
			c.ConnConfig.RuntimeParams["search_path"] = o.schema
		}

		c.ConnConfig.Tracer = multitracer.New(otelpgx.NewTracer(), queryLog)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: pool: %w", err)
	}

	listen, err := pgxpool.NewWithConfig(ctx, listenCfg)
	if err != nil {
		pool.Close()

		return nil, fmt.Errorf("postgres: listen pool: %w", err)
	}

	queries := bounded{
		pool: pool, timeout: orDefault(cfg.Pool.AcquireTimeout, DefaultAcquireTimeout),
		attrs: metric.WithAttributes(attribute.String("pool", o.name)),
	}

	queries.timeouts, err = otel.Meter(meterName).Int64Counter("backplane.postgres.acquire.timeouts",
		metric.WithDescription("Waits for a free connection cut by pool.acquire_timeout"))
	if err != nil {
		log.Warn("postgres: acquire timeout counter unavailable", xlog.Err(err))
	}

	db, err := prepare(ctx, pool, queries, o)
	if err != nil {
		listen.Close()
		pool.Close()

		return nil, err
	}

	db.listen = listen

	// Pool gauges: saturation does not show in per-query spans.
	for name, p := range map[string]*pgxpool.Pool{o.name: pool, o.name + ".listen": listen} {
		if err := otelpgx.RecordStats(p, otelpgx.WithStatsAttributes(attribute.String("pool", name))); err != nil {
			log.Warn("postgres: pool metrics unavailable", xlog.Err(err))
		}
	}

	return db, nil
}

// meterName is the instrumentation scope of the SDK's own pool metrics.
const meterName = "github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"

func prepare(ctx context.Context, pool *pgxpool.Pool, queries bounded, o options) (*DB, error) {
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

	trm, err := pgtx.NewTxManager(queries, isolation)
	if err != nil {
		return nil, fmt.Errorf("postgres: tx manager: %w", err)
	}

	return &DB{Pool: pool, Trm: trm, DB: pgtx.NewTxDB(queries)}, nil
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
	db, err := Open(ctx, s.Log(), p.cfg, append([]Option{Name(s.Name())}, p.opts...)...)
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
