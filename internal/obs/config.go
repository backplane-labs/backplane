// Package obs provides bounded, read-only queries of independent Victoria stores.
package obs

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

const (
	defaultConcurrent      = 8
	defaultLimit           = 200
	defaultMaxLimit        = 2000
	defaultPoints          = 20000
	defaultResponseBytes   = 8 << 20
	defaultExpressionBytes = 32 << 10
	defaultTimeout         = 30 * time.Second
	maxTimeout             = 5 * time.Minute
	defaultRange           = 31 * 24 * time.Hour
)

// Config belongs to deployment, never to UI. URLs are read API roots; a path
// prefix (such as /select/0/prometheus) is allowed. Disabled stores need no URL.
type Config struct {
	SourceOverrides map[string]SourceSelection `json:"source_overrides,omitempty"`

	MetricsURL           string        `json:"metrics_url,omitempty"`
	LogsURL              string        `json:"logs_url,omitempty"`
	TracesURL            string        `json:"traces_url,omitempty"`
	MetricsAuthorization config.Secret `json:"metrics_authorization,omitempty"`
	LogsAuthorization    config.Secret `json:"logs_authorization,omitempty"`
	TracesAuthorization  config.Secret `json:"traces_authorization,omitempty"`
	TraceQL              bool          `json:"traceql"                         schemapb:"default=false"`
	Concurrent           int           `json:"concurrent"                      schemapb:"default=8;gte=1;lte=128"`
	DefaultLimit         uint32        `json:"default_limit"                   schemapb:"default=200;gte=1;lte=10000"`
	MaxLimit             uint32        `json:"max_limit"                       schemapb:"default=2000;gte=1;lte=10000"`
	MaxPoints            uint32        `json:"max_points"                      schemapb:"default=20000;gte=1;lte=1000000"`
	ResponseBytes        int64         `json:"response_bytes"                  schemapb:"default=8388608;gte=1024"`
	ExpressionBytes      int           `json:"expression_bytes"                schemapb:"default=32768;gte=1;lte=1048576"`
	Timeout              time.Duration `json:"timeout"                         schemapb:"default=30s;gt=0;lte=5m"`
	MaxRange             time.Duration `json:"max_range"                       schemapb:"default=744h;gt=0"`
}

var errConfig = errors.New("invalid observability configuration")

// ErrURL identifies an invalid deployment query API root.
var ErrURL = errors.New("want HTTP(S) API root without credentials, query or fragment")

// Validate reports no credential-bearing URL or header contents.
func (c Config) Validate() error {
	c = c.defaults()
	for _, endpoint := range []string{c.MetricsURL, c.LogsURL, c.TracesURL} {
		if endpoint == "" {
			continue
		}

		if !validURL(endpoint) {
			return ErrURL
		}
	}

	if err := c.validateBudgets(); err != nil {
		return err
	}

	for _, authorization := range []config.Secret{
		c.MetricsAuthorization, c.LogsAuthorization, c.TracesAuthorization,
	} {
		if strings.ContainsAny(authorization.Reveal(), "\r\n") {
			return fmt.Errorf("%w: authorization contains a newline", errConfig)
		}
	}

	return nil
}

func (c Config) defaults() Config {
	if c.Concurrent == 0 {
		c.Concurrent = defaultConcurrent
	}

	if c.DefaultLimit == 0 {
		c.DefaultLimit = defaultLimit
	}

	if c.MaxLimit == 0 {
		c.MaxLimit = defaultMaxLimit
	}

	if c.MaxPoints == 0 {
		c.MaxPoints = defaultPoints
	}

	if c.ResponseBytes == 0 {
		c.ResponseBytes = defaultResponseBytes
	}

	if c.ExpressionBytes == 0 {
		c.ExpressionBytes = defaultExpressionBytes
	}

	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}

	if c.MaxRange == 0 {
		c.MaxRange = defaultRange
	}

	return c
}

func validURL(endpoint string) bool {
	parsed, err := url.Parse(endpoint)

	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

func (c Config) validateBudgets() error {
	for _, bound := range []struct{ value, min, max int64 }{
		{int64(c.Concurrent), 1, 128},
		{int64(c.DefaultLimit), 1, int64(c.MaxLimit)},
		{int64(c.MaxLimit), 1, 10000},
		{int64(c.MaxPoints), 1, 1000000},
		{c.ResponseBytes, 1024, 64 << 20},
		{int64(c.ExpressionBytes), 1, 1 << 20},
		{int64(c.Timeout), 1, int64(maxTimeout)},
		{int64(c.MaxRange), 1, int64(365 * 24 * time.Hour)},
	} {
		if bound.value < bound.min || bound.value > bound.max {
			return fmt.Errorf("%w: query budgets out of bounds", errConfig)
		}
	}

	return nil
}
