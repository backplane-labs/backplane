// Package otlp admits public OTLP/HTTP into a deployment-owned Collector.
// It does not depend on operator sessions, IAM, the registry or PostgreSQL.
package otlp

import (
	"errors"
	"net/netip"
	"net/url"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// Config is deployment-only; an empty URL disables the ingress route.
type Config struct {
	URL            string          `json:"url,omitempty"`
	Keys           []config.Secret `json:"keys,omitempty"`
	TrustedProxies []string        `json:"trusted_proxies,omitempty"`
	BodyBytes      int64           `json:"body_bytes"                schemapb:"default=2097152;gte=1024;lte=16777216"`
	Concurrent     int64           `json:"concurrent"                schemapb:"default=16;gte=1;lte=1024"`
	RatePerMinute  int64           `json:"rate_per_minute"           schemapb:"default=300;gte=1;lte=1000000"`
	Burst          int64           `json:"burst"                     schemapb:"default=120;gte=1;lte=1000000"`
	MaxIPs         int64           `json:"max_ips"                   schemapb:"default=4096;gte=1;lte=1000000"`
	IdleTTL        time.Duration   `json:"idle_ttl"                  schemapb:"default=10m;gt=0"`
	Timeout        time.Duration   `json:"timeout"                   schemapb:"default=10s;gt=0"`
}

const minKeyBytes = 16

// IngestHeader carries IngestProxy on every request the proxy forwards: the
// Collector (include_metadata) turns it into the backplane.ingest attribute
// its audit pipeline drops.
const (
	IngestHeader = "X-Backplane-Ingest"
	IngestProxy  = "proxy"
)

// ErrConfig indicates an unsafe or incomplete admission configuration.
var ErrConfig = errors.New("invalid OTLP admission configuration")

// Validate also applies to callers that do not use the schema loader.
func (c Config) Validate() error {
	if c.URL == "" {
		return nil
	}

	if !collectorURL(c.URL) {
		return ErrConfig
	}

	for _, bound := range [][3]int64{
		{c.BodyBytes, 1024, 16 << 20},
		{c.Concurrent, 1, 1024},
		{c.RatePerMinute, 1, 1_000_000},
		{c.Burst, 1, 1_000_000},
		{c.MaxIPs, 1, 1_000_000},
	} {
		if bound[0] < bound[1] || bound[0] > bound[2] {
			return ErrConfig
		}
	}

	if c.IdleTTL <= 0 || c.Timeout <= 0 || len(c.Keys) > 64 {
		return ErrConfig
	}
	// Expiration cannot refill a token bucket faster than its configured rate.
	if c.IdleTTL < time.Duration(c.Burst)*time.Minute/time.Duration(c.RatePerMinute) {
		return ErrConfig
	}

	for _, key := range c.Keys {
		if len(key.Reveal()) < minKeyBytes {
			return ErrConfig
		}
	}

	for _, proxy := range c.TrustedProxies {
		if _, err := netip.ParsePrefix(proxy); err != nil {
			return ErrConfig
		}
	}

	return nil
}

func collectorURL(raw string) bool {
	u, err := url.Parse(raw)

	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
