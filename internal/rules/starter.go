package rules

import (
	"context"
	"errors"
	"fmt"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/executor"
	"github.com/backplane-labs/backplane/internal/wire"
)

// Workflow is the workflow type that runs bindings and rules on
// backplane's task queue: the executor's.
const Workflow = wire.BindingWorkflow

// ErrAlreadyStarted: a run with the workflow id exists already (running
// or closed): the event was handled before — a redelivery or a
// republish. A Starter returns it, or Temporal's
// *serviceerror.WorkflowExecutionAlreadyStarted, which counts the same.
var ErrAlreadyStarted = errors.New("rules: run already started")

// Starter starts one run of a rule on an event: the executor's binding
// workflow with the compiled program, the event's payload and attributes,
// the rule's identity and the event's trace context, under workflowID
// (rule/<id>/<ce-id>) with the reuse policy "reject duplicate". A run that
// exists already is ErrAlreadyStarted. Start only: the call does not wait
// for the run.
type Starter interface {
	StartRule(ctx context.Context, id string, version int64, prog *bindings.Program, event []byte,
		meta bindings.Meta, trace map[string]string, workflowID string) error
}

// RunID is the workflow id of the rule's run on the event with ce-id:
// rule/<id>/<ce-id>. The same event never starts a second run.
func RunID(id, ceID string) string { return runPrefix(id) + ceID }

// TestRunID is the workflow id of a test run: test/rule/<id>/<uuid>,
// test/rule/draft/<uuid> for an unsaved definition.
func TestRunID(id, unique string) string { return testPrefix(id) + unique }

func runPrefix(id string) string { return "rule/" + id + "/" }

func testPrefix(id string) string {
	if id == "" {
		id = draftID
	}

	return wire.TestRunPrefix + "rule/" + id + "/"
}

// draftID stands for the id of an unsaved definition in a test run's id.
const draftID = "draft"

// TemporalStarter starts rule runs with ExecuteWorkflow on a Temporal
// client: the workflow type and the task queue given, the executor's
// Input (executor.RuleInput) as the argument, memo source = rule:<id> and
// backplane.binding = rule:<id>@<version>. It is the engine's Starter
// unless WithStarter gives another.
type TemporalStarter struct {
	client   TemporalFunc
	queue    string
	workflow string
}

// NewTemporalStarter starts workflow on queue through the client fn gives.
func NewTemporalStarter(fn TemporalFunc, queue, workflow string) TemporalStarter {
	return TemporalStarter{client: fn, queue: queue, workflow: workflow}
}

var _ Starter = TemporalStarter{}

// StartRule implements Starter.
func (s TemporalStarter) StartRule(
	ctx context.Context, id string, version int64, prog *bindings.Program, event []byte,
	meta bindings.Meta, trace map[string]string, workflowID string,
) error {
	c, err := s.client()
	if err != nil {
		return fmt.Errorf("rules: temporal: %w", err)
	}

	identity := executor.Identity{Rule: id, Version: version, Test: strings.HasPrefix(workflowID, wire.TestRunPrefix)}

	in, err := executor.RuleInput(prog, identity, event, executor.EventMeta{
		ID: meta.ID, Source: meta.Source, Subject: meta.Subject, Type: meta.Type, Time: meta.Time,
	}, trace)
	if err != nil {
		return fmt.Errorf("rules: input of %s: %w", workflowID, err)
	}

	_, err = c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                s.queue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		Memo: map[string]any{
			wire.MemoSource: "rule:" + id, wire.MemoBinding: identity.String(),
		},
	}, s.workflow, in)
	if alreadyStarted(err) {
		return fmt.Errorf("%w: %s", ErrAlreadyStarted, workflowID)
	}

	if err != nil {
		return fmt.Errorf("rules: start %s: %w", workflowID, err)
	}

	return nil
}

// alreadyStarted reports whether err says the run exists already.
func alreadyStarted(err error) bool {
	if err == nil {
		return false
	}

	var dup *serviceerror.WorkflowExecutionAlreadyStarted

	return errors.Is(err, ErrAlreadyStarted) || errors.Is(err, direct.ErrAlreadyStarted) || errors.As(err, &dup)
}
