package console

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/pgtx/pkg/tx"

	"github.com/gopherex/backplane/internal/store"
	"github.com/gopherex/backplane/internal/store/db"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// ErrAttempts: the login attempts cannot be counted; the login is refused.
var ErrAttempts = errors.New("console: login attempts unavailable")

// Attempts counts login attempts for brute force protection (§11.3), shared
// by every replica: PostgreSQL in the server, memory in tests. When they cannot
// be counted the login is refused.
type Attempts interface {
	// Allow takes one attempt from addr; refused, it is how long to wait.
	Allow(ctx context.Context, addr string) (time.Duration, error)
	// Failed records a wrong token from anywhere.
	Failed(ctx context.Context) error
	// Succeeded resets the global backoff.
	Succeeded(ctx context.Context) error
}

// LoginLimits of shared login attempts.
type LoginLimits struct {
	// Every address has Burst attempts per Window.
	Burst  int
	Window time.Duration
	// After Threshold failures (any address, none older than Forget apart)
	// every login waits Base, doubling per further failure up to Max; a
	// success resets it.
	Threshold int
	Base, Max time.Duration
	Forget    time.Duration
}

const (
	loginBurst     = 5
	loginThreshold = 10
)

// DefaultLoginLimits: 5 attempts a minute per address; after 10 failures
// in a row 1s, doubling up to a minute.
func DefaultLoginLimits() LoginLimits {
	return LoginLimits{
		Burst: loginBurst, Window: time.Minute,
		Threshold: loginThreshold, Base: time.Second, Max: time.Minute, Forget: time.Hour,
	}
}

// PGAttempts counts attempts in the platform's PostgreSQL schema.
// A database outage refuses logins; there is no per-replica fallback.
type PGAttempts struct {
	st     deps.Dependency[*store.Store]
	limits LoginLimits
}

var _ Attempts = PGAttempts{}

const (
	loginOperationTimeout = 3 * time.Second
	maxLoginBackoffShift  = 16
	loginCleanupBatchSize = 1000
	loginCleanupBatches   = 10
)

type attemptsPruner interface {
	Prune(ctx context.Context) error
}

func NewPGAttempts(st deps.Dependency[*store.Store], limits LoginLimits) PGAttempts {
	return PGAttempts{st: st, limits: limits}
}

func (p PGAttempts) store() (*store.Store, error) {
	if !p.st.Ready() {
		return nil, ErrAttempts
	}

	return p.st.Get(), nil
}

func (p PGAttempts) Allow(ctx context.Context, address string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, loginOperationTimeout)
	defer cancel()

	st, err := p.store()
	if err != nil {
		return 0, err
	}

	var wait time.Duration

	err = tx.DoReadCommitted(ctx, st.Trm, func(ctx context.Context) error {
		wait = 0 // transaction-local result

		state, err := st.Q.GetLoginState(ctx)
		if err != nil {
			return fmt.Errorf("login state: %w", err)
		}

		clock, err := st.Q.LoginNow(ctx)
		if err != nil {
			return fmt.Errorf("login state: %w", err)
		}

		if state.BlockedUntil.After(clock.Now) {
			wait = state.BlockedUntil.Sub(clock.Now)
			return nil
		}

		_, err = st.Q.TakeLoginAttempt(ctx, db.TakeLoginAttemptParams{
			Address: address, ExpiresAt: clock.Now.Add(p.limits.Window), Now: clock.Now, Burst: int64(p.limits.Burst),
		})
		if err == nil {
			return nil
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("take login attempt: %w", err)
		}

		row, err := st.Q.GetLoginAttempt(ctx, address)
		if err != nil {
			return fmt.Errorf("login state: %w", err)
		}

		wait = max(row.ExpiresAt.Sub(clock.Now), time.Millisecond)

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrAttempts, err)
	}

	return wait, nil
}

func (p PGAttempts) Failed(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, loginOperationTimeout)
	defer cancel()

	st, err := p.store()
	if err != nil {
		return err
	}

	err = tx.DoReadCommitted(ctx, st.Trm, func(ctx context.Context) error {
		state, err := st.Q.LockLoginState(ctx)
		if err != nil {
			return fmt.Errorf("login state: %w", err)
		}

		clock, err := st.Q.LoginNow(ctx)
		if err != nil {
			return fmt.Errorf("login state: %w", err)
		}

		failures := state.Failures
		if !state.ForgetAt.After(clock.Now) {
			failures = 0
		}
		// More failures cannot increase the capped delay further.
		failures = min(failures+1, int64(p.limits.Threshold)+maxLoginBackoffShift)
		blocked := state.BlockedUntil

		if over := failures - int64(p.limits.Threshold); over >= 0 {
			factor := int64(1) << uint(min(over, maxLoginBackoffShift))

			delay := p.limits.Max
			if int64(p.limits.Base) <= int64(p.limits.Max)/factor {
				delay = time.Duration(int64(p.limits.Base) * factor)
			}

			blocked = clock.Now.Add(delay)
		}

		return st.Q.SetLoginFailure(ctx, db.SetLoginFailureParams{
			Failures: failures, ForgetAt: clock.Now.Add(p.limits.Forget), BlockedUntil: blocked,
		})
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAttempts, err)
	}

	return nil
}

func (p PGAttempts) Succeeded(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, loginOperationTimeout)
	defer cancel()

	st, err := p.store()
	if err != nil {
		return err
	}

	if err := st.Q.ResetLoginFailures(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrAttempts, err)
	}

	return nil
}

// Prune removes expired IP counters in bounded batches, skipping rows used
// by concurrent logins. Every replica may run it safely.
func (p PGAttempts) Prune(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, loginOperationTimeout)
	defer cancel()

	st, err := p.store()
	if err != nil {
		return err
	}

	for range loginCleanupBatches {
		n, err := st.Q.DeleteExpiredLoginAttempts(ctx)
		if err != nil {
			return fmt.Errorf("%w: prune: %w", ErrAttempts, err)
		}

		if n < loginCleanupBatchSize {
			return nil
		}
	}

	return nil
}
