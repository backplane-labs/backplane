package backplanetest_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

func TestOperator(t *testing.T) {
	t.Parallel()

	if _, ok := backplane.OperatorOf(t.Context()); ok {
		t.Fatal("an operator without one")
	}

	session := uuid.New()

	op, ok := backplane.OperatorOf(backplanetest.Operator(t.Context(), session))
	if !ok || op.Session != session.String() || op.Actor() != "console:"+session.String() {
		t.Fatalf("operator: %+v, %v", op, ok)
	}
}
