package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/executor"
	"github.com/backplane-labs/backplane/internal/wire"
)

// Waiting for a test run.
const (
	defaultTestWait = 30 * time.Second
	maxTestWait     = 10 * time.Minute
)

// API is RuleService: the rule RPCs of bindings.RuleAPI plus the run RPCs
// of the engine (TestRule, ListRuleRuns, GetRuleRun, CancelRuleRun).
type API struct {
	bindings.RuleAPI

	e *Engine
}

var _ consolev1.RuleServiceServer = API{}

// API is the engine's RuleService.
func (e *Engine) API() API { return API{RuleAPI: e.src.RuleAPI(), e: e} }

// Register registers RuleService on r — in place of the bindings
// Manager's own, which lacks the run RPCs.
func (e *Engine) Register(r grpc.ServiceRegistrar) { consolev1.RegisterRuleServiceServer(r, e.API()) }

// errInput: the request is malformed.
var errInput = errors.New("rules: invalid request")

// status maps errors to gRPC codes; unexpected ones are logged.
func (e *Engine) status(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}

	code := codes.Internal

	switch {
	case errors.Is(err, errInput):
		code = codes.InvalidArgument
	case errors.Is(err, bindings.ErrNotSynced), errors.Is(err, errNotConfigured), errors.Is(err, errNoRuns):
		code = codes.Unavailable
	case errors.Is(err, bindings.ErrNoRule), errors.Is(err, bindings.ErrNoVersion), errors.Is(err, errNotRuleRun):
		code = codes.NotFound
	case errors.Is(err, bindings.ErrDeleted), errors.Is(err, errNoTemporal):
		code = codes.FailedPrecondition
	case errors.Is(err, bindings.ErrConflict):
		code = codes.Aborted
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	default:
		if st := serviceerror.ToStatus(err); st != nil && st.Code() != codes.Unknown && st.Code() != codes.OK {
			return st.Err() //nolint:wrapcheck // Temporal's status travels as is
		}

		e.Log().Ctx().Error(ctx, "rules api", xlog.Err(err))
	}

	return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
}

var (
	errNoRuns     = errors.New("rules: runs are not served")
	errNotRuleRun = errors.New("rules: not a run of the rule")
)

func ruleID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: id is not a uuid", errInput)
	}

	return id, nil
}

// TestRule implements RuleService.
func (a API) TestRule(ctx context.Context, req *consolev1.TestRuleRequest) (*consolev1.TestRuleResponse, error) {
	out, err := a.e.Test(ctx, req)
	if err != nil {
		return nil, a.e.status(ctx, err)
	}

	return out, nil
}

// Test is TestRule: see the RPC.
func (e *Engine) Test(ctx context.Context, req *consolev1.TestRuleRequest) (*consolev1.TestRuleResponse, error) {
	rule, id, version, err := e.testDefinition(ctx, req)
	if err != nil {
		return nil, err
	}

	cat, err := e.src.Catalog()
	if err != nil {
		return nil, err //nolint:wrapcheck // ErrNotSynced as is
	}

	out := &consolev1.TestRuleResponse{}

	prog, err := bindings.CompileRule(rule, cat)

	var invalid *bindings.InvalidError

	switch {
	case errors.As(err, &invalid):
		for _, v := range invalid.Violations {
			out.Violations = append(out.Violations, &consolev1.Violation{Path: v.Path, Code: v.Code, Message: v.Message})
		}

		return out, nil
	case err != nil:
		return nil, fmt.Errorf("compile: %w", err)
	}

	payload := []byte(req.GetEvent())
	if strings.TrimSpace(req.GetEvent()) == "" {
		payload = []byte("{}")
	}

	meta := testMeta(rule.Event, req.GetMeta())
	out.Meta = metaPB(meta)

	// A failing evaluation is the test's answer, not the call's error.
	if out.Matched, out.Error = match(prog, payload, meta); out.GetError() != "" {
		return out, nil
	}

	if !out.GetMatched() || req.GetDryRun() {
		return out, nil
	}

	if e.direct != nil && prog.Runtime() == consolev1.Runtime_RUNTIME_DIRECT {
		out.Result, err = e.testDirect(ctx, id, version, prog, payload, meta, req.GetTimeout())

		return out, err
	}

	if !e.temporalEnabled {
		return nil, errNoTemporal
	}

	out.Result, err = e.testRun(ctx, id, version, prog, payload, meta, req.GetTimeout())

	return out, err
}

// testDirect runs a test on the direct runtime, inline.
func (e *Engine) testDirect(
	ctx context.Context, id string, version int64, prog *bindings.Program, payload []byte, meta bindings.Meta,
	timeout *durationpb.Duration,
) (*consolev1.CallResult, error) {
	wait := defaultTestWait
	if d := timeout.AsDuration(); timeout != nil && d > 0 {
		wait = min(d, maxTestWait)
	}

	trace := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, trace)

	wid := TestRunID(id, uuid.NewString())

	res, err := e.direct.Rules().Test(ctx, id, version, prog, payload, meta, map[string]string(trace), wid, wait)
	if err != nil {
		return nil, err //nolint:wrapcheck // the runtime wraps
	}

	e.Log().Ctx().Info(ctx, "rule test run", xlog.String("rule", id), xlog.String("workflow_id", res.GetWorkflowId()))

	return res, nil
}

// match evaluates the rule's filter on the event: whether it matches, or
// the evaluation's error text (then it does not match).
func match(prog *bindings.Program, payload []byte, meta bindings.Meta) (bool, string) {
	scope, err := prog.StartEvent(payload, meta)
	if err != nil {
		return false, err.Error()
	}

	matched, err := prog.Match(scope)
	if err != nil {
		return false, err.Error()
	}

	return matched, ""
}

// testDefinition is the definition a test runs: the saved rule's version,
// or the unsaved one; id "" for the unsaved.
func (e *Engine) testDefinition(
	ctx context.Context, req *consolev1.TestRuleRequest,
) (bindings.Rule, string, int64, error) {
	switch {
	case req.GetId() != "" && req.GetDefinition() != nil:
		return bindings.Rule{}, "", 0, fmt.Errorf("%w: id and definition are exclusive", errInput)
	case req.GetDefinition() != nil:
		return bindings.RuleFromPB(req.GetDefinition()), "", 0, nil
	case req.GetId() == "":
		return bindings.Rule{}, "", 0, fmt.Errorf("%w: id or definition is required", errInput)
	}

	id, err := ruleID(req.GetId())
	if err != nil {
		return bindings.Rule{}, "", 0, err
	}

	v, err := e.src.RuleVersion(ctx, id, toInt64(req.GetVersion()))
	if err != nil {
		return bindings.Rule{}, "", 0, err //nolint:wrapcheck // the Manager's errors as they are
	}

	if v.Deleted {
		return bindings.Rule{}, "", 0, fmt.Errorf("%w: version %d of rule %s", bindings.ErrDeleted, v.Version, id)
	}

	return v.Rule, id.String(), v.Version, nil
}

// testMeta is the sample event's attributes with the defaults: a new id,
// the event's service and name, now.
func testMeta(event string, m *consolev1.RuleEventMeta) bindings.Meta {
	svc, _ := bindings.SplitName(event)
	out := bindings.Meta{
		ID: m.GetId(), Source: m.GetSource(), Subject: m.GetSubject(), Type: m.GetType(),
	}

	if out.ID == "" {
		out.ID = uuid.NewString()
	}

	if out.Source == "" {
		out.Source = svc
	}

	if out.Type == "" {
		out.Type = event
	}

	if m.GetTime() != nil {
		out.Time = m.GetTime().AsTime()
	} else {
		out.Time = time.Now().UTC()
	}

	return out
}

func metaPB(m bindings.Meta) *consolev1.RuleEventMeta {
	return &consolev1.RuleEventMeta{
		Id: m.ID, Source: m.Source, Subject: m.Subject, Type: m.Type, Time: timestamppb.New(m.Time),
	}
}

// testRun starts the test run and waits for its end: the workflow's
// HookResult payload is the output ({} for a rule's run, which has no
// result).
func (e *Engine) testRun(
	ctx context.Context, id string, version int64, prog *bindings.Program, payload []byte, meta bindings.Meta,
	timeout *durationpb.Duration,
) (*consolev1.CallResult, error) {
	c, err := e.temporal()
	if err != nil {
		return nil, fmt.Errorf("temporal: %w: %w", errNotConfigured, err)
	}

	wait := defaultTestWait
	if d := timeout.AsDuration(); timeout != nil && d > 0 {
		wait = min(d, maxTestWait)
	}

	trace := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, trace)

	wid := TestRunID(id, uuid.NewString())
	start := time.Now()

	if err := e.starter.StartRule(ctx, id, version, prog, payload, meta, map[string]string(trace), wid); err != nil {
		return nil, err //nolint:wrapcheck // the starter wraps
	}

	e.Log().Ctx().Info(ctx, "rule test run started", xlog.String("rule", id), xlog.String("workflow_id", wid))

	run := c.GetWorkflow(ctx, wid, "")
	out := &consolev1.CallResult{WorkflowId: wid, RunId: run.GetRunID()}

	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	var res backplanev1.HookResult

	err = run.Get(wctx, &res)
	out.Took = durationpb.New(time.Since(start))

	switch {
	case err == nil:
		switch result := res.GetPayload(); {
		case len(result) == 0:
			out.Output = "{}"
		case json.Valid(result):
			out.Output = string(result)
		default:
			out.Raw = result
		}
	case wctx.Err() != nil && ctx.Err() == nil:
		return nil, status.Errorf(codes.DeadlineExceeded,
			"test run %s is still going: follow it in GetRuleRun", wid)
	default:
		out.Error, out.ErrorType = readable(err)
	}

	return out, nil
}

// readable is a run's failure: the innermost application error's message
// and type, else the error's text.
func readable(err error) (string, string) {
	var (
		app      *temporal.ApplicationError
		canceled *temporal.CanceledError
		timeout  *temporal.TimeoutError
		term     *temporal.TerminatedError
	)

	switch {
	case errors.As(err, &app):
		return app.Message(), app.Type()
	case errors.As(err, &canceled):
		return "canceled", ""
	case errors.As(err, &term):
		return "terminated", ""
	case errors.As(err, &timeout):
		return "timed out", wire.TimeoutType
	default:
		return err.Error(), ""
	}
}

// ListRuleRuns implements RuleService.
func (a API) ListRuleRuns(
	ctx context.Context, req *consolev1.ListRuleRunsRequest,
) (*consolev1.ListRuleRunsResponse, error) {
	id, err := ruleID(req.GetId())
	if err != nil {
		return nil, a.e.status(ctx, err)
	}

	if a.e.runs == nil && a.e.direct == nil {
		return nil, a.e.status(ctx, errNoRuns)
	}

	prefix := runPrefix(id.String())
	if req.GetTests() {
		prefix = testPrefix(id.String())
	}

	var viaTemporal func(token []byte) ([]*consolev1.Run, []byte, string, error)
	if a.e.runs != nil && a.e.temporalEnabled {
		viaTemporal = func(token []byte) ([]*consolev1.Run, []byte, string, error) {
			res, err := a.e.runs.ListRuns(ctx, &consolev1.ListRunsRequest{
				Service: a.e.queue, Status: req.GetStatus(), WorkflowIdPrefix: prefix,
				PageSize: req.GetPageSize(), PageToken: token,
			})
			if err != nil {
				return nil, nil, "", err //nolint:wrapcheck // a gRPC status from ops
			}

			return res.GetRuns(), res.GetNextPageToken(), res.GetQuery(), nil
		}
	}

	if a.e.direct == nil {
		runs, token, query, err := viaTemporal(req.GetPageToken())
		if err != nil {
			return nil, a.e.status(ctx, err)
		}

		return &consolev1.ListRuleRunsResponse{Runs: runs, NextPageToken: token, Query: query}, nil
	}

	runs, token, query, err := a.e.direct.ListRuleRuns(ctx, id.String(), req.GetTests(), req.GetStatus(),
		req.GetPageSize(), req.GetPageToken(), viaTemporal)
	if err != nil {
		return nil, a.e.status(ctx, err)
	}

	return &consolev1.ListRuleRunsResponse{Runs: runs, NextPageToken: token, Query: query}, nil
}

// GetRuleRun implements RuleService.
func (a API) GetRuleRun(ctx context.Context, req *consolev1.GetRuleRunRequest) (*consolev1.GetRuleRunResponse, error) {
	if err := a.e.ownRun(req.GetId(), strings.TrimPrefix(req.GetWorkflowId(), wire.DirectRunPrefix)); err != nil {
		return nil, a.e.status(ctx, err)
	}

	if wire.IsDirectRun(req.GetWorkflowId()) {
		if a.e.direct == nil {
			return nil, a.e.status(ctx, errNoRuns)
		}

		return a.e.direct.GetRuleRun(ctx, req.GetWorkflowId()) //nolint:wrapcheck // a gRPC status
	}

	if a.e.runs == nil {
		return nil, a.e.status(ctx, errNoRuns)
	}

	res, err := a.e.runs.GetRun(ctx, &consolev1.GetRunRequest{WorkflowId: req.GetWorkflowId(), RunId: req.GetRunId()})
	if err != nil {
		return nil, a.e.status(ctx, err)
	}

	c, err := a.e.temporal()
	if err != nil {
		return nil, a.e.status(ctx, err)
	}

	steps, id, err := executor.Timeline(ctx, c, a.e.codec(), res)
	if err != nil {
		return nil, a.e.status(ctx, err)
	}

	return &consolev1.GetRuleRunResponse{
		Run: res, Version: uint64(max(id.Version, 0)),
		Test: id.Test || strings.HasPrefix(req.GetWorkflowId(), wire.TestRunPrefix), Steps: steps,
	}, nil
}

// CancelRuleRun implements RuleService.
func (a API) CancelRuleRun(
	ctx context.Context, req *consolev1.CancelRuleRunRequest,
) (*consolev1.CancelRuleRunResponse, error) {
	if err := a.e.ownRun(req.GetId(), strings.TrimPrefix(req.GetWorkflowId(), wire.DirectRunPrefix)); err != nil {
		return nil, a.e.status(ctx, err)
	}

	if wire.IsDirectRun(req.GetWorkflowId()) {
		if a.e.direct == nil {
			return nil, a.e.status(ctx, errNoRuns)
		}

		if err := a.e.direct.CancelRun(req.GetWorkflowId()); err != nil {
			return nil, err //nolint:wrapcheck // a gRPC status
		}

		return &consolev1.CancelRuleRunResponse{}, nil
	}

	if a.e.runs == nil {
		return nil, a.e.status(ctx, errNoRuns)
	}

	if _, err := a.e.runs.CancelRun(ctx, &consolev1.CancelRunRequest{
		WorkflowId: req.GetWorkflowId(), RunId: req.GetRunId(),
	}); err != nil {
		return nil, a.e.status(ctx, err)
	}

	return &consolev1.CancelRuleRunResponse{}, nil
}

// ownRun checks that workflowID is a run (or test run) of rule id — for
// an empty id, a test run of an unsaved definition — and that runs are
// served.
func (e *Engine) ownRun(rule, workflowID string) error {
	if e.runs == nil && e.direct == nil {
		return errNoRuns
	}

	if rule == "" {
		if p := testPrefix(""); len(workflowID) <= len(p) || !strings.HasPrefix(workflowID, p) {
			return fmt.Errorf("%w: %q is not a test run of an unsaved rule", errNotRuleRun, workflowID)
		}

		return nil
	}

	id, err := ruleID(rule)
	if err != nil {
		return err
	}

	if !IsRunOf(id, workflowID) {
		return fmt.Errorf("%w: %q is not a run of rule %s", errNotRuleRun, workflowID, id)
	}

	return nil
}

// IsRunOf reports whether workflowID is a run or a test run of rule id.
func IsRunOf(id uuid.UUID, workflowID string) bool {
	for _, p := range []string{runPrefix(id.String()), testPrefix(id.String())} {
		if len(workflowID) > len(p) && strings.HasPrefix(workflowID, p) {
			return true
		}
	}

	return false
}

func toInt64(v uint64) int64 {
	const maxInt64 = 1<<63 - 1

	return int64(min(v, maxInt64)) //nolint:gosec // bounded
}
