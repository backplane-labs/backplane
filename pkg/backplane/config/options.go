package config

import (
	"errors"
	"time"

	"github.com/gopherex/xconf"
)

// Option configures Load and Open.
type Option func(*settings)

type settings struct {
	service    string
	file       string
	fileSet    bool
	noFile     bool
	envPrefix  string
	prefixSet  bool
	noEnv      bool
	noConsul   bool
	extra      []xconf.Source
	defaults   map[string]any
	block      map[string]any
	remote     RemoteFactory
	backoffMin time.Duration
	backoffMax time.Duration
}

// Service names the service when build.Service is not stamped.
func Service(name string) Option { return func(s *settings) { s.service = name } }

// File reads this file (JSON or YAML by extension) instead of
// BACKPLANE_CONFIG_FILE.
func File(path string) Option { return func(s *settings) { s.file, s.fileSet = path, true } }

// WithoutFile disables the file layer.
func WithoutFile() Option { return func(s *settings) { s.noFile = true } }

// EnvPrefix replaces the derived prefix <SERVICE>_.
func EnvPrefix(prefix string) Option {
	return func(s *settings) { s.envPrefix, s.prefixSet = prefix, true }
}

// WithoutEnv disables the environment layer.
func WithoutEnv() Option { return func(s *settings) { s.noEnv = true } }

// Defaults are the service's own defaults, in the configuration's JSON
// shape (durations as strings, "30s"): a layer above the schema's defaults
// and below the file, so the file, the environment and Consul override
// them. Later calls merge over earlier ones.
func Defaults(values map[string]any) Option {
	return func(s *settings) { s.defaults = mergeValues(s.defaults, values) }
}

// BlockDefaults are Defaults of the SDK block, relative to it ("server":
// {...}) whatever key the configuration embeds it under.
func BlockDefaults(values map[string]any) Option {
	return func(s *settings) { s.block = mergeValues(s.block, values) }
}

// Source appends layers above file and environment, below Consul KV, in
// order.
func Source(sources ...xconf.Source) Option {
	return func(s *settings) { s.extra = append(s.extra, sources...) }
}

// WithoutConsul keeps Open off Consul KV even when the block names it.
func WithoutConsul() Option { return func(s *settings) { s.noConsul = true } }

// RemoteSource is a watched configuration layer carrying a console revision.
type RemoteSource interface {
	xconf.Source
	LastRevision() uint64
}

// RemoteFactory creates the remote layer after file and environment are loaded.
type RemoteFactory func(Backplane, string) (RemoteSource, error)

// WithRemote installs a remote configuration driver. The runtime restricts it
// to Live fields and preserves its last valid values during an outage.
func WithRemote(factory RemoteFactory) Option {
	return func(s *settings) { s.remote = factory }
}

// ConsulBackoff bounds the backoff between retries of an unreachable Consul
// (default 1s..30s).
func ConsulBackoff(minDelay, maxDelay time.Duration) Option {
	return func(s *settings) { s.backoffMin, s.backoffMax = minDelay, maxDelay }
}

// Remote driver configuration errors.
var (
	ErrRemoteDriver = errors.New("config: consul configured without a remote configuration driver")
	ErrRemoteSource = errors.New("config: remote configuration driver returned nil")
)
