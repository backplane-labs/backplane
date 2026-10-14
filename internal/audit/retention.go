package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

const (
	cleanupBatch = 5000
	maxKeys      = 64
	minKeyBytes  = 16
)

var (
	errRetention = errors.New("audit.retention must be nonnegative")
	errClock     = errors.New("audit expiry clock is missing")
)

// Settings is deployment-only; zero retention keeps all control audit
// records. Entries awaiting event delivery are never expired. Application
// audit is never expired. No UI exposes these settings.
type Settings struct {
	Retention time.Duration `json:"retention" schemapb:"default=0s;gte=0"`
	// Listen is the OTLP audit listener (gRPC LogsService) the Collector's
	// audit pipeline exports to: BACKPLANE_AUDIT_LISTEN.
	Listen string `json:"listen" schemapb:"default=:4317"`
	// Keys the Collector presents as a bearer token; empty: none required
	// (the listener is internal): BACKPLANE_AUDIT_KEYS, a JSON array.
	Keys []config.Secret `json:"keys,omitempty"`
	// ExportURL is the optional complete OTLP/HTTP logs endpoint. Empty disables
	// export, without changing the durable PostgreSQL history or ingest API.
	ExportURL           string        `json:"export_url,omitempty"`
	ExportAuthorization config.Secret `json:"export_authorization,omitempty"`
}

// Validate rejects negative retention without imposing a deletion policy,
// and short keys.
func (s Settings) Validate() error {
	if err := validateExport(s); err != nil {
		return err
	}

	if s.Retention < 0 {
		return errRetention
	}

	if len(s.Keys) > maxKeys {
		return errIngestKeys
	}

	for _, key := range s.Keys {
		if len(key.Reveal()) < minKeyBytes {
			return errIngestKeys
		}
	}

	return nil
}

// WithRetention enables deployment-controlled cleanup; zero keeps all records.
func WithRetention(retention time.Duration) Option {
	return func(s *Service) { s.retention = retention }
}

func (s *Service) cleanupLoop(ctx context.Context) error {
	if s.retention <= 0 {
		return nil
	}

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		if err := s.Expire(ctx, time.Now().Add(-s.retention)); err != nil && ctx.Err() == nil {
			s.Log().Warn("audit cleanup retry", xlog.Err(err))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Expire removes a bounded delivered prefix and advances the cursor floor in
// the same transaction. A pending outbox record stops expiry before its sequence.
func (s *Service) Expire(ctx context.Context, cutoff time.Time) error {
	st := s.store.Get()

	err := st.InTx(ctx, func(ctx context.Context) error {
		clock, err := st.Q.GetAuditClock(ctx)
		if err != nil {
			return fmt.Errorf("read audit retention clock: %w", err)
		}

		boundary, err := st.Q.GetAuditExpiryBoundary(ctx, cutoff)
		if err != nil {
			return fmt.Errorf("audit expiry boundary: %w", err)
		}

		if boundary.ThroughSequence == nil {
			return errClock
		}

		through := min(*boundary.ThroughSequence, clock.RetainedAfter+cleanupBatch)
		if through <= clock.RetainedAfter {
			return nil
		}

		if err = st.Q.DeleteExpiredAuditOutbox(ctx, through); err != nil {
			return fmt.Errorf("expire audit outbox: %w", err)
		}

		if err = st.Q.DeleteExpiredAuditEntries(ctx, through); err != nil {
			return fmt.Errorf("expire audit entries: %w", err)
		}

		if err = st.Q.AdvanceAuditRetention(ctx, through); err != nil {
			return fmt.Errorf("advance audit retention: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("audit cleanup: %w", err)
	}

	return nil
}
