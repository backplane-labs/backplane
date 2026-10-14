// Package audit exposes durable control history and optionally exports its outbox over OTLP.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/store/db"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

const (
	pollInterval   = 500 * time.Millisecond
	publishTimeout = 3 * time.Second
	leaseSeconds   = 60
	retrySeconds   = 2
	batchSize      = 16
)

// Entry is a durable platform control audit record. Numeric identities are strings to
// preserve their values across consumers; id is the deduplication identity.
type Entry struct {
	ID          string            `json:"id"`
	Sequence    string            `json:"sequence"`
	CreatedAt   time.Time         `json:"created_at"`
	Actor       string            `json:"actor"`
	Action      string            `json:"action"`
	Subject     string            `json:"subject"`
	Outcome     string            `json:"outcome"`
	OperationID string            `json:"operation_id"`
	Detail      store.AuditDetail `json:"detail"`
	Service     string            `json:"service,omitempty"`
}

// Service reads committed history. Delivery failures leave the outbox intact.
type Service struct {
	consolev1.UnimplementedAuditServiceServer
	deps.Component
	store     deps.Dependency[*store.Store]
	publish   func(context.Context, Entry) error
	runQueue  RunQueueFunc
	retention time.Duration
}

// RunQueueFunc is the task queue of a run (Temporal's
// DescribeWorkflowExecution).
type RunQueueFunc func(ctx context.Context, workflowID, runID string) (string, error)

// Option replaces integration boundaries for acceptance testing.
type Option func(*Service)

// WithRunQueue attributes a run command whose workflow id follows no
// Backplane convention to the service of the run's task queue.
func WithRunQueue(fn RunQueueFunc) Option {
	return func(s *Service) { s.runQueue = fn }
}

// WithPublisher configures acknowledged export; persistence and claiming stay real.
func WithPublisher(publish func(context.Context, Entry) error) Option {
	return func(s *Service) { s.publish = publish }
}

// New starts outbox delivery after the store dependency is ready.
func New(parent deps.Scope, st deps.Dependency[*store.Store], options ...Option) *Service {
	s := &Service{Component: deps.NewComponent(parent, "audit"), store: st}
	for _, option := range options {
		option(s)
	}

	if s.publish != nil {
		s.Go(s.run)
	}

	if s.retention > 0 {
		s.Go(s.cleanupLoop)
	}

	return s
}

// Register exposes the audit API on the authenticated console registrar.
func (s *Service) Register(registrar grpc.ServiceRegistrar) {
	consolev1.RegisterAuditServiceServer(registrar, s)
}

func (s *Service) run(ctx context.Context) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if err := s.Deliver(ctx); err != nil && ctx.Err() == nil {
			s.Log().Warn("audit outbox retry", xlog.Err(err))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Deliver claims a bounded batch. Leases protect acknowledgments from stale
// replicas. A crash after publish and before acknowledgment may redeliver.
func (s *Service) Deliver(ctx context.Context) error {
	if s.publish == nil {
		return nil
	}

	st := s.store.Get()
	lease := uuid.New()

	claimed, err := st.Q.ClaimAuditOutbox(ctx, db.ClaimAuditOutboxParams{
		Lease:        &lease,
		LeaseSeconds: leaseSeconds,
		BatchSize:    batchSize,
	})
	if err != nil {
		return fmt.Errorf("claim audit outbox: %w", err)
	}

	var failures []error

	for _, claim := range claimed {
		entry, readErr := st.Q.GetAuditEntry(ctx, claim.Sequence)
		if readErr != nil {
			failures = append(failures, fmt.Errorf("read audit outbox entry: %w", readErr))
			continue
		}

		if deliverErr := s.deliverOne(ctx, entry, lease); deliverErr != nil {
			failures = append(failures, deliverErr)
		}
	}

	return errors.Join(failures...)
}

func (s *Service) deliverOne(ctx context.Context, row db.GetAuditEntryRow, lease uuid.UUID) error {
	var detail store.AuditDetail
	if err := json.Unmarshal(row.Detail, &detail); err != nil {
		return fmt.Errorf("decode audit detail: %w", err)
	}

	entry := Entry{
		ID: row.ID.String(), Sequence: strconv.FormatInt(row.Sequence, 10), CreatedAt: row.CreatedAt,
		Actor: row.Actor, Action: row.Action, Subject: row.Subject, Service: row.Service,
		Outcome: row.Outcome, OperationID: row.OperationID.String(), Detail: detail,
	}

	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	publishErr := s.publish(publishCtx, entry)

	cancel()

	if publishErr != nil {
		retry := db.RetryAuditOutboxParams{Sequence: row.Sequence, Lease: &lease, RetrySeconds: retrySeconds}
		if retryErr := s.store.Get().Q.RetryAuditOutbox(ctx, retry); retryErr != nil {
			return fmt.Errorf("schedule audit retry: %w", retryErr)
		}

		return fmt.Errorf("publish audit entry: %w", publishErr)
	}

	ack := db.AcknowledgeAuditOutboxParams{Sequence: row.Sequence, Lease: &lease}
	if err := s.store.Get().Q.AcknowledgeAuditOutbox(ctx, ack); err != nil {
		return fmt.Errorf("acknowledge audit entry: %w", err)
	}

	return nil
}
