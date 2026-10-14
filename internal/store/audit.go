package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/backplane-labs/backplane/internal/store/db"
)

// AuditDetail permits control metadata, never arbitrary request bodies, secrets,
// configuration values, workflow arguments, revision comments or backend error
// messages. Signal, Reason and Note are the operator's words on a run or
// schedule command (a signal's name, never its argument), bounded by the
// caller.
type AuditDetail struct {
	Revision   int64    `json:"revision,omitempty"`
	RollbackOf int64    `json:"rollback_of,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Paused     *bool    `json:"paused,omitempty"`
	Code       string   `json:"code,omitempty"`
	WorkflowID string   `json:"workflow_id,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
	Affected   int64    `json:"affected,omitempty"`
	Signal     string   `json:"signal,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// AuditDraft is one semantic action or one phase of an external command. Actor
// uses the established console:<session UUID> identity, or a named system actor.
type AuditDraft struct {
	Actor       string
	Action      string
	Subject     string
	Outcome     string
	OperationID uuid.UUID
	Detail      AuditDetail
	// Service the entry is about; empty for installation-wide entries.
	Service string
}

// AppendAudit joins the caller's control transaction or opens its own. The
// clock's row lock survives until commit, preventing a later visible sequence
// from overtaking this entry. Outbox work is committed with the entry.
func (s *Store) AppendAudit(ctx context.Context, draft AuditDraft) (db.InsertAuditEntryRow, error) {
	id := uuid.New()
	if draft.OperationID == uuid.Nil {
		draft.OperationID = id
	}

	detail, err := json.Marshal(draft.Detail)
	if err != nil {
		return db.InsertAuditEntryRow{}, fmt.Errorf("audit detail: %w", err)
	}

	var entry db.InsertAuditEntryRow

	err = s.InTx(ctx, func(ctx context.Context) error {
		sequence, seqErr := s.Q.NextAuditSequence(ctx)
		if seqErr != nil {
			return fmt.Errorf("audit sequence: %w", seqErr)
		}

		var insertErr error

		entry, insertErr = s.Q.InsertAuditEntry(ctx, db.InsertAuditEntryParams{
			Sequence: sequence.Sequence, ID: id, Actor: draft.Actor, Action: draft.Action, Subject: draft.Subject,
			Outcome: draft.Outcome, OperationID: draft.OperationID, Detail: detail, Service: draft.Service,
		})
		if insertErr != nil {
			return fmt.Errorf("audit entry: %w", insertErr)
		}

		if !s.skipAuditExport {
			if outboxErr := s.Q.InsertAuditOutbox(ctx, sequence.Sequence); outboxErr != nil {
				return fmt.Errorf("audit outbox: %w", outboxErr)
			}
		}

		return nil
	})

	return entry, err
}

// AuditControl is the transaction participant for a completed database mutation.
// service is the service the change is about; empty for installation-wide ones.
func (s *Store) AuditControl(ctx context.Context, actor, action, subject, service string, detail AuditDetail) error {
	_, err := s.AppendAudit(ctx, AuditDraft{
		Actor:   actor,
		Action:  action,
		Subject: subject,
		Outcome: "succeeded",
		Detail:  detail,
		Service: service,
	})

	return err
}

// ServiceOf is the service of a qualified name "<service>.<Name>".
func ServiceOf(name string) string {
	service, _, _ := strings.Cut(name, ".")

	return service
}
