package console

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/store/db"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Errors of Sessions.
var (
	// ErrNoSession: no such session.
	ErrNoSession = errors.New("console: no such session")
)

// Session is one login.
type Session struct {
	ID         uuid.UUID
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	Address    string
	UserAgent  string
}

// IsZero reports whether s is the zero Session (no session).
func (s Session) IsZero() bool { return s.ID == uuid.Nil }

// Sessions is where the sessions live: PostgreSQL (PG) in the server,
// memory in tests.
type Sessions interface {
	// Create stores a session under the hash of its token; s.ID is ignored
	// and the new id returned.
	Create(ctx context.Context, tokenHash []byte, s Session) (uuid.UUID, error)
	// ByToken finds a session by the hash of its token; ErrNoSession.
	ByToken(ctx context.Context, tokenHash []byte) (Session, error)
	// Get finds a session by id; ErrNoSession.
	Get(ctx context.Context, id uuid.UUID) (Session, error)
	// Touch moves last seen forward to at (never back).
	Touch(ctx context.Context, id uuid.UUID, at time.Time) error
	// List is every session, newest first.
	List(ctx context.Context) ([]Session, error)
	// Delete removes a session; false when there was none.
	Delete(ctx context.Context, id uuid.UUID) (bool, error)
	// DeleteOthers removes every session but keep and reports how many.
	DeleteOthers(ctx context.Context, keep uuid.UUID) (int64, error)
	// DeleteStale removes sessions expired at now or idle since idleSince.
	DeleteStale(ctx context.Context, now, idleSince time.Time) (int64, error)
}

// PG keeps sessions in PostgreSQL (schema backplane): console_session.
type PG struct {
	st deps.Dependency[*store.Store]
}

var _ Sessions = PG{}

// NewPG is the PostgreSQL Sessions over the store; the store is read once
// it is provided (from the console's start on).
func NewPG(st deps.Dependency[*store.Store]) PG { return PG{st: st} }

func (p PG) q() *db.Queries { return p.st.Get().Q }

// Create implements Sessions.
func (p PG) Create(ctx context.Context, tokenHash []byte, s Session) (uuid.UUID, error) {
	st := p.st.Get()

	var id uuid.UUID

	err := st.InAuditedTx(ctx, func(ctx context.Context) error {
		row, createErr := p.q().CreateSession(ctx, db.CreateSessionParams{
			TokenHash: tokenHash, CreatedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt, Address: s.Address, UserAgent: s.UserAgent,
		})
		if createErr != nil {
			return fmt.Errorf("console: create session: %w", createErr)
		}

		id = row.ID

		return st.AuditControl(ctx, "admin", "session.create", id.String(), "", store.AuditDetail{})
	})
	if err != nil {
		return id, fmt.Errorf("console session transaction: %w", err)
	}

	return id, nil
}

// ByToken implements Sessions.
func (p PG) ByToken(ctx context.Context, tokenHash []byte) (Session, error) {
	r, err := p.q().GetSessionByToken(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNoSession
	}

	if err != nil {
		return Session{}, fmt.Errorf("console: session: %w", err)
	}

	return Session(r), nil
}

// Get implements Sessions.
func (p PG) Get(ctx context.Context, id uuid.UUID) (Session, error) {
	r, err := p.q().GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNoSession
	}

	if err != nil {
		return Session{}, fmt.Errorf("console: session: %w", err)
	}

	return Session(r), nil
}

// Touch implements Sessions.
func (p PG) Touch(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := p.q().TouchSession(ctx, db.TouchSessionParams{ID: id, LastSeenAt: at}); err != nil {
		return fmt.Errorf("console: touch session: %w", err)
	}

	return nil
}

// List implements Sessions.
func (p PG) List(ctx context.Context) ([]Session, error) {
	rows, err := p.q().ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("console: sessions: %w", err)
	}

	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, Session(r))
	}

	return out, nil
}

// Delete implements Sessions.
func (p PG) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	st := p.st.Get()

	var deleted bool

	err := st.InAuditedTx(ctx, func(ctx context.Context) error {
		n, deleteErr := p.q().DeleteSession(ctx, id)
		if deleteErr != nil {
			return fmt.Errorf("console: delete session: %w", deleteErr)
		}

		deleted = n > 0
		if !deleted {
			return nil
		}

		actor := id
		if current, ok := SessionID(ctx); ok {
			actor = current
		}

		return st.AuditControl(ctx, "console:"+actor.String(), "session.revoke", id.String(), "", store.AuditDetail{})
	})
	if err != nil {
		return deleted, fmt.Errorf("console session transaction: %w", err)
	}

	return deleted, nil
}

// DeleteOthers implements Sessions.
func (p PG) DeleteOthers(ctx context.Context, keep uuid.UUID) (int64, error) {
	st := p.st.Get()

	var affected int64

	err := st.InAuditedTx(ctx, func(ctx context.Context) error {
		n, deleteErr := p.q().DeleteOtherSessions(ctx, keep)
		if deleteErr != nil {
			return fmt.Errorf("console: delete other sessions: %w", deleteErr)
		}

		affected = n
		if n == 0 {
			return nil
		}

		return st.AuditControl(ctx, "console:"+keep.String(), "session.revoke_others", keep.String(), "", store.AuditDetail{
			Affected: n,
		})
	})
	if err != nil {
		return affected, fmt.Errorf("console session transaction: %w", err)
	}

	return affected, nil
}

// DeleteStale implements Sessions.
func (p PG) DeleteStale(ctx context.Context, now, idleSince time.Time) (int64, error) {
	st := p.st.Get()

	var affected int64

	err := st.InAuditedTx(ctx, func(ctx context.Context) error {
		n, deleteErr := p.q().DeleteStaleSessions(ctx, db.DeleteStaleSessionsParams{Now: now, IdleSince: idleSince})
		if deleteErr != nil {
			return fmt.Errorf("console: delete stale sessions: %w", deleteErr)
		}

		affected = n
		if n == 0 {
			return nil
		}

		return st.AuditControl(ctx, "system:session-expiry", "session.expire", "console", "", store.AuditDetail{Affected: n})
	})
	if err != nil {
		return affected, fmt.Errorf("console session transaction: %w", err)
	}

	return affected, nil
}
