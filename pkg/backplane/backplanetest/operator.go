package backplanetest

import (
	"context"

	"github.com/google/uuid"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
)

// Operator is ctx as a call the console relays for session carries it:
// backplane.OperatorOf reports that session, so a test calls an internal API
// handler directly as an operator would.
//
//	ctx := backplanetest.Operator(t.Context(), session)
//	_, err := admin.CloseRoom(ctx, req) // audits actor console:<session>
func Operator(ctx context.Context, session uuid.UUID) context.Context {
	return guard.WithSession(ctx, session)
}
