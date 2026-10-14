package apperrors

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/store/db"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Defaults of the settings.
const (
	DefaultRetention      = 90 * 24 * time.Hour
	DefaultBurstPerMinute = 60
	DefaultBurstPerDay    = 2000
	cleanupTick           = time.Hour
	cleanupBatch          = 5000
)

// Settings are deployment-only: BACKPLANE_ERRORS_*.
type Settings struct {
	// Retention of occurrences; 0 keeps them for ever.
	Retention time.Duration `json:"retention" schemapb:"default=2160h;gte=0"`
	// BurstPerMinute and BurstPerDay bound the occurrences stored per group
	// (one fingerprint in one service) in a minute and in a day; past them
	// the group only counts.
	BurstPerMinute int64 `json:"burst_per_minute" schemapb:"default=60;gte=1"`
	BurstPerDay    int64 `json:"burst_per_day"    schemapb:"default=2000;gte=1"`
	// MaxRecordBytes bounds a stored record; a bigger one has its stack and
	// envelope cut and is marked truncated.
	MaxRecordBytes int64 `json:"max_record_bytes" schemapb:"default=524288;gte=4096"`
}

func (s Settings) limits() Limits {
	l := DefaultLimits
	if s.MaxRecordBytes > 0 {
		l.MaxRecord = int(s.MaxRecordBytes)
	}

	return l
}

var errStoreUnavailable = errors.New("apperrors: database unavailable")

// Service keeps errors: the store's writer and the console's ErrorService.
type Service struct {
	deps.Component

	store    deps.Dependency[*store.Store]
	settings Settings
	related  Related
	author   func(context.Context) string
	records  metric.Int64Counter
}

// Option configures New.
type Option func(*Service)

// WithRelated installs the reader of related logs and traces (the
// telemetry stores); without one RelatedLogs says unavailable.
func WithRelated(r Related) Option { return func(s *Service) { s.related = r } }

// New creates the service under parent, over st.
func New(parent deps.Scope, st deps.Dependency[*store.Store], settings Settings, opts ...Option) *Service {
	s := &Service{
		Component: deps.NewComponent(parent, "errors"), store: st, settings: settings,
		author: func(context.Context) string { return DefaultAuthor },
	}
	for _, o := range opts {
		o(s)
	}

	s.records, _ = s.Meter().Int64Counter("backplane.errors.ingest.records",
		metric.WithDescription("Error records received, by outcome: stored, counted, duplicate, rejected"))

	s.Go(s.cleanupLoop)

	return s
}

// Limits of a stored record.
func (s *Service) Limits() Limits { return s.settings.limits() }

// Ready reports whether records can be stored.
func (s *Service) Ready() bool { return !s.store.IsZero() && s.store.Ready() }

// Save stores the records: each touches its group and is stored while the
// group's burst windows allow; a retried record (same key) is stored once.
// Returns how many were stored and how many only counted.
func (s *Service) Save(ctx context.Context, records []Record) (int64, int64, error) {
	if !s.Ready() {
		return 0, 0, errStoreUnavailable
	}

	st := s.store.Get()

	var stored, counted int64

	for i := range records {
		r := &records[i]

		err := st.InTx(ctx, func(ctx context.Context) error {
			windows, err := st.Q.TouchErrorGroup(ctx, r.Touch)
			if err != nil {
				return fmt.Errorf("touch group: %w", err)
			}

			if int64(windows.MinuteCount) > s.settings.BurstPerMinute || int64(windows.DayCount) > s.settings.BurstPerDay {
				counted++

				return nil
			}

			n, err := st.Q.InsertError(ctx, r.Insert)
			if err != nil {
				return fmt.Errorf("insert error: %w", err)
			}

			if n == 0 {
				// A retry of a stored record: its touch counted it twice.
				return st.Q.UncountErrorDuplicate(ctx, db.UncountErrorDuplicateParams{
					Fingerprint: r.Touch.Fingerprint, Service: r.Touch.Service,
				})
			}

			stored++

			return st.Q.CountErrorStored(ctx, db.CountErrorStoredParams{
				Fingerprint: r.Touch.Fingerprint, Service: r.Touch.Service,
			})
		})
		if err != nil {
			return stored, counted, fmt.Errorf("store error record: %w", err)
		}
	}

	s.count(ctx, "stored", stored)
	s.count(ctx, "counted", counted)
	s.count(ctx, "duplicate", int64(len(records))-stored-counted)

	return stored, counted, nil
}

// Count counts rejected records (the ingest's).
func (s *Service) Count(ctx context.Context, outcome string, n int64) { s.count(ctx, outcome, n) }

func (s *Service) count(ctx context.Context, outcome string, n int64) {
	if n > 0 && s.records != nil {
		s.records.Add(ctx, n, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

func (s *Service) cleanupLoop(ctx context.Context) error {
	if s.settings.Retention <= 0 {
		return nil
	}

	ticker := time.NewTicker(cleanupTick)
	defer ticker.Stop()

	for {
		if err := s.Expire(ctx, time.Now().Add(-s.settings.Retention)); err != nil && ctx.Err() == nil {
			s.Log().Warn("errors cleanup retry", xlog.Err(err))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Expire deletes the occurrences before cutoff, a batch at a time until
// none is left, then the groups without occurrences last seen before it.
func (s *Service) Expire(ctx context.Context, cutoff time.Time) error {
	if !s.Ready() {
		return errStoreUnavailable
	}

	st := s.store.Get()

	for range 1000 { // bounds one cleanup run
		var before, after int64

		err := st.InTx(ctx, func(ctx context.Context) error {
			n, err := st.Q.CountExpiredErrors(ctx, cutoff)
			if err != nil {
				return err //nolint:wrapcheck // wrapped below
			}

			if before = n.Count; before == 0 {
				return nil
			}

			if err := st.Q.ExpireErrors(ctx, db.ExpireErrorsParams{Cutoff: cutoff, BatchSize: cleanupBatch}); err != nil {
				return err //nolint:wrapcheck // wrapped below
			}

			if n, err = st.Q.CountExpiredErrors(ctx, cutoff); err != nil {
				return err //nolint:wrapcheck // wrapped below
			}

			after = n.Count

			return nil
		})
		if err != nil {
			return fmt.Errorf("expire errors: %w", err)
		}

		if before == 0 || after == before {
			break
		}
	}

	if err := st.Q.ExpireErrorGroups(ctx, cutoff); err != nil {
		return fmt.Errorf("expire error groups: %w", err)
	}

	return nil
}
