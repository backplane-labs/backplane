package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/backplane-labs/backplane/internal/store"
)

func TestAuditRollbackIsAtomic(t *testing.T) {
	t.Parallel()
	st := open(t)
	operation := uuid.New()

	err := st.InTx(t.Context(), func(ctx context.Context) error {
		if _, execErr := st.DB.Exec(ctx, "CREATE TEMP TABLE audit_rollback_fixture (id integer)"); execErr != nil {
			return execErr
		}

		_, appendErr := st.AppendAudit(ctx, store.AuditDraft{Actor: "test", Action: "test.rollback", Subject: operation.String(), Outcome: "succeeded", OperationID: operation})
		if appendErr != nil {
			return appendErr
		}

		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}

	var count int
	if err = st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.audit_entry WHERE operation_id=$1", operation).Scan(&count); err != nil || count != 0 {
		t.Fatal("entry survived rollback", count, err)
	}

	if err = st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.audit_outbox o JOIN backplane.audit_entry e USING (sequence) WHERE e.operation_id=$1", operation).Scan(&count); err != nil || count != 0 {
		t.Fatal("outbox survived rollback", count, err)
	}
}

func TestAuditCommitOrderAcrossReplicas(t *testing.T) {
	t.Parallel()
	first, second := open(t), open(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	assigned := make(chan int64, 1)
	release := make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()

	firstDone := make(chan error, 1)

	go func() {
		firstDone <- first.InTx(ctx, func(ctx context.Context) error {
			entry, err := first.AppendAudit(ctx, store.AuditDraft{Actor: "test", Action: "test.slow_commit", Subject: uuid.NewString(), Outcome: "succeeded"})
			if err != nil {
				return err
			}

			select {
			case assigned <- entry.Sequence:
			default:
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		})
	}()

	var lower int64
	select {
	case lower = <-assigned:
	case err := <-firstDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	secondDone := make(chan error, 1)

	var higher int64

	go func() {
		entry, err := second.AppendAudit(ctx, store.AuditDraft{Actor: "test", Action: "test.fast_commit", Subject: uuid.NewString(), Outcome: "succeeded"})
		higher = entry.Sequence

		secondDone <- err
	}()

	clock, err := second.Q.GetAuditClock(ctx)
	if err != nil || clock.Sequence >= lower {
		t.Fatal("uncommitted sequence became a visible cursor", clock, lower, err)
	}

	if _, err = second.Q.GetAuditEntry(ctx, lower); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("uncommitted audit is visible", err)
	}

	select {
	case err = <-secondDone:
		t.Fatal("later writer overtook blocked transaction", err)
	case <-time.After(50 * time.Millisecond):
	}

	unblock()

	if err = <-firstDone; err != nil {
		t.Fatal(err)
	}

	if err = <-secondDone; err != nil || higher <= lower {
		t.Fatal("wrong commit order", lower, higher, err)
	}

	for _, sequence := range []int64{lower, higher} {
		var count int
		if err = second.Pool.QueryRow(ctx, "SELECT count(*) FROM backplane.audit_outbox WHERE sequence=$1", sequence).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing committed outbox", count, err)
		}
	}
}

// Concurrent entries on their own all commit: they queue on the clock
// instead of failing serializable on it. Not parallel: the burst would
// starve the serializable transactions of the other tests.
func TestAuditConcurrentAppends(t *testing.T) { //nolint:paralleltest // see above
	st := open(t)
	operation := uuid.New()

	const writers = 16

	var writing sync.WaitGroup

	errs := make(chan error, writers)

	for range writers {
		writing.Go(func() {
			_, err := st.AppendAudit(t.Context(), store.AuditDraft{Actor: "test", Action: "test.concurrent", Subject: operation.String(), Outcome: "intent", OperationID: operation})
			errs <- err
		})
	}

	writing.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}

	var count int
	if err := st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.audit_entry WHERE operation_id=$1", operation).Scan(&count); err != nil || count != writers {
		t.Fatalf("entries %d, want %d: %v", count, writers, err)
	}
}
