package direct

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/wire"
)

// RuleStarter runs a rule's event on the direct runtime, inline: the
// rules engine's Starter for rules compiled for it. The run is complete
// when StartRule returns, so the engine settles the event by its outcome;
// a redelivery of an event whose run is running or completed is
// ErrAlreadyStarted (the engine acks it), one whose run failed runs again.
type RuleStarter struct {
	s *Service
}

// Rules is the engine's starter for direct rules.
func (s *Service) Rules() RuleStarter { return RuleStarter{s: s} }

// StartRule runs the rule's program on the event within RunTimeout. The
// run's id is workflowID with the direct prefix.
func (st RuleStarter) StartRule(
	ctx context.Context, id string, version int64, prog *bindings.Program, event []byte,
	meta bindings.Meta, trace map[string]string, workflowID string,
) error {
	_, err := st.run(ctx, id, version, prog, event, meta, trace, workflowID)

	return err
}

func (st RuleStarter) run(
	ctx context.Context, id string, version int64, prog *bindings.Program, event []byte,
	meta bindings.Meta, trace map[string]string, workflowID string,
) ([]byte, error) {
	ctx, cancel := context.WithTimeout(continueTrace(ctx, trace), st.s.runTimeout)
	defer cancel()

	test := strings.HasPrefix(workflowID, wire.TestRunPrefix)
	runID := wire.DirectRunPrefix + workflowID

	return st.s.Execute(ctx, Start{
		ID: runID, Kind: KindRule, Source: "rule/" + id, Version: version, Test: test, Caller: "rule:" + id, Input: event,
	}, Run{
		ID: runID, Kind: KindRule, Program: prog, Payload: event, Meta: meta, Identity: identity("rule:"+id, version),
		Trace: trace,
	}, !test)
}

// Test runs a rule's test on the direct runtime and is its result as the
// console shows it; wait bounds the run.
func (st RuleStarter) Test(
	ctx context.Context, id string, version int64, prog *bindings.Program, event []byte, meta bindings.Meta,
	trace map[string]string, workflowID string, wait time.Duration,
) (*consolev1.CallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	start := time.Now()
	out, err := st.run(ctx, id, version, prog, event, meta, trace, workflowID)
	res := &consolev1.CallResult{
		WorkflowId: wire.DirectRunPrefix + workflowID, RunId: wire.DirectRunPrefix + workflowID,
		Took: durationpb.New(time.Since(start)),
	}

	if err != nil {
		res.Error, res.ErrorType = Readable(err)

		return res, nil
	}

	res.Output = string(out)

	return res, nil
}

// RunID of a rule's event on the direct runtime, for the dedup check.
func RunID(workflowID string) string { return wire.DirectRunPrefix + workflowID }

// String implements fmt.Stringer for logs.
func (st RuleStarter) String() string {
	return fmt.Sprintf("direct runtime (run timeout %s)", st.s.runTimeout)
}
