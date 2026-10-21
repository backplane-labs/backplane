package backplane

import (
	"context"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
)

// Application audit (§14): a log record carrying AuditLabel=true is audit.
// The deployment's Collector forwards such records to backplane, which keeps
// them; the log itself goes wherever logs go. The rest of the keys are
// optional and filterable in the console's Audit; the action is the record's
// event.name, else its message.
const (
	AuditLabel = "backplane.audit"
	// AuditID deduplicates a record the Collector retries; without it the
	// record's content does.
	AuditID      = "backplane.audit.id"
	AuditActor   = "backplane.audit.actor"
	AuditSubject = "backplane.audit.subject"
	// AuditOutcome: succeeded, failed or rejected by convention.
	AuditOutcome = "backplane.audit.outcome"
)

// Audit marks a log record as application audit:
//
//	log.Info("identity deleted", backplane.Audit(),
//		xlog.String("event.name", "identity.deleted"),
//		xlog.String(backplane.AuditSubject, "identity/"+id))
func Audit() xlog.Field { return xlog.Bool(AuditLabel, true) }

// Operator is the console session a call through the console's relay comes
// from: a call on the internal API (Service.Internal). The console signs in
// with the installation's admin token, so a session is all there is of the
// person; the console lists sessions with their address and user agent.
type Operator struct {
	// Session is the console session's id.
	Session string
}

// Actor is the operator as an audit actor, as backplane's own audit names
// console sessions: console:<session>.
//
//	log.Info("room closed", backplane.Audit(), xlog.String(backplane.AuditActor, op.Actor()))
func (o Operator) Actor() string { return "console:" + o.Session }

// OperatorOf is the console operator a call on the internal API comes from;
// ok is false for calls the console did not relay for a session (backplane's
// own activity calls, other ports).
//
// The relay drops every bp-* key the browser sends and sets the session
// itself; the platform port admits only calls presenting the internal
// secret. So the operator is backplane's word as long as the secret is set
// (BACKPLANE_INTERNAL_SECRET) and kept from whoever reaches the platform
// port. Without a secret the port is open and the session is the caller's
// claim.
func OperatorOf(ctx context.Context) (Operator, bool) {
	id, ok := guard.Session(ctx)
	if !ok {
		return Operator{}, false
	}

	return Operator{Session: id.String()}, true
}
