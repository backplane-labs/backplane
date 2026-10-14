package direct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/redact"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Bounds of console test runs.
const (
	defaultTestTimeout = 30 * time.Second
	maxTestTimeout     = 10 * time.Minute
	// maxVersion bounds a requested version so it converts to int64.
	maxVersion = 1 << 62
)

// Versions is what the console's test needs of bindings besides
// Bindings: *bindings.Manager.
type Versions interface {
	Version(ctx context.Context, hook string, version int64) (bindings.BindingVersion, error)
}

// TemporalBindings is the Temporal side of BindingService's run RPCs: the
// executor's. Listings put the direct runs first, then these; a run id
// tells which side a run is on.
type TemporalBindings struct {
	// Test runs a test of a Temporal-runtime definition.
	Test func(ctx context.Context, req *consolev1.TestBindingRequest, b bindings.Binding, version int64,
		prog *bindings.Program) (*consolev1.TestBindingResponse, error)
	List   func(ctx context.Context, req *consolev1.ListBindingRunsRequest) (*consolev1.ListBindingRunsResponse, error)
	Get    func(ctx context.Context, req *consolev1.GetBindingRunRequest) (*consolev1.GetBindingRunResponse, error)
	Cancel func(ctx context.Context, req *consolev1.CancelBindingRunRequest) (*consolev1.CancelBindingRunResponse, error)
}

// BindingAPI is BindingService: the bindings Manager's RPCs (base) with
// the run RPCs over the direct runtime and, when the installation has
// Temporal, the executor's.
type BindingAPI struct {
	consolev1.BindingServiceServer

	s        *Service
	versions Versions
	t        *TemporalBindings
}

var _ consolev1.BindingServiceServer = BindingAPI{}

// BindingService is base with the run RPCs; t is nil without Temporal.
func (s *Service) BindingService(base consolev1.BindingServiceServer, v Versions, t *TemporalBindings) BindingAPI {
	return BindingAPI{BindingServiceServer: base, s: s, versions: v, t: t}
}

// RegisterBindings registers BindingService on r.
func (s *Service) RegisterBindings(
	base consolev1.BindingServiceServer, v Versions, t *TemporalBindings,
) func(r grpc.ServiceRegistrar) {
	return func(r grpc.ServiceRegistrar) { consolev1.RegisterBindingServiceServer(r, s.BindingService(base, v, t)) }
}

func invalid(format string, args ...any) error {
	return status.Errorf(codes.InvalidArgument, format, args...)
}

// Status maps errors to gRPC codes; gRPC statuses pass; unexpected ones
// are logged.
func (s *Service) status(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}

	code := codes.Internal
	if s == nil {
		return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
	}

	switch {
	case errors.Is(err, ErrNotSynced), errors.Is(err, bindings.ErrNotSynced), errors.Is(err, deps.ErrNotReady):
		code = codes.Unavailable
	case errors.Is(err, ErrNoRun), errors.Is(err, bindings.ErrNoBinding), errors.Is(err, bindings.ErrNoVersion):
		code = codes.NotFound
	case errors.Is(err, ErrTemporalRuntime):
		code = codes.FailedPrecondition
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	default:
		s.Log().Ctx().Error(ctx, "direct api", xlog.Err(err))
	}

	return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
}

// TestBinding implements BindingService.
func (a BindingAPI) TestBinding(
	ctx context.Context, req *consolev1.TestBindingRequest,
) (*consolev1.TestBindingResponse, error) {
	b, version, err := a.testBinding(ctx, req)
	if err != nil {
		return nil, a.s.status(ctx, err)
	}

	cat, err := a.catalog()
	if err != nil {
		return nil, a.s.status(ctx, err)
	}

	prog, err := bindings.CompileBinding(b, cat)
	if err != nil {
		var bad *bindings.InvalidError
		if errors.As(err, &bad) {
			return &consolev1.TestBindingResponse{
				Violations: bindings.ViolationsPB(bad.Violations), Version: uint64(version), //nolint:gosec // positive
			}, nil
		}

		return nil, a.s.status(ctx, err)
	}

	if prog.Runtime() != consolev1.Runtime_RUNTIME_DIRECT {
		if a.t == nil || a.t.Test == nil {
			return nil, a.s.status(ctx, ErrTemporalRuntime)
		}

		return a.t.Test(ctx, req, b, version, prog)
	}

	if a.s == nil {
		return nil, status.Error(codes.FailedPrecondition, //nolint:wrapcheck // a gRPC status
			"the direct runtime is not served by this backplane")
	}

	input := []byte(req.GetInput())
	if len(input) == 0 {
		input = []byte("{}")
	}

	if !json.Valid(input) {
		return nil, invalid("input is not valid JSON")
	}

	timeout := req.GetTimeout().AsDuration()
	if timeout <= 0 {
		timeout = defaultTestTimeout
	}

	timeout = min(timeout, maxTestTimeout)

	res := a.s.Test(ctx, b.Hook, prog, version, input, timeout, a.s.author(ctx))

	a.s.Log().Ctx().Info(ctx, "console action", xlog.String("actor", a.s.author(ctx)),
		xlog.String("action", "binding.test"), xlog.String("subject", b.Hook),
		xlog.String("workflow_id", res.GetWorkflowId()), xlog.Int64("version", version),
		xlog.String("error", res.GetError()))

	return &consolev1.TestBindingResponse{Result: res, Version: uint64(version)}, nil //nolint:gosec // positive
}

// catalog is the manager's catalog (with the installation's defaults):
// the service's bindings, else the versions' when they are a manager.
func (a BindingAPI) catalog() (bindings.Catalog, error) {
	if a.s != nil {
		return a.s.bindings.Catalog() //nolint:wrapcheck // the manager's ErrNotSynced as is
	}

	if c, ok := a.versions.(interface {
		Catalog() (bindings.Catalog, error)
	}); ok {
		return c.Catalog() //nolint:wrapcheck // the manager's ErrNotSynced as is
	}

	return nil, ErrNotSynced
}

// Test runs a test of hook's binding on the direct runtime within timeout
// and is its result as the console shows it, the answer masked by the
// program.
func (s *Service) Test(
	ctx context.Context, hook string, prog *bindings.Program, version int64, input []byte, timeout time.Duration,
	caller string,
) *consolev1.CallResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	id := wire.DirectTestRunID(hook, uuid.NewString())
	start := time.Now()

	out, err := s.Execute(ctx, Start{
		ID: id, Kind: KindBinding, Source: hook, Version: version, Test: true, Caller: caller, Input: input,
	}, Run{
		ID: id, Kind: KindBinding, Program: prog, Payload: input, Identity: identity(hook, version), Trace: carrier,
	}, false)

	res := &consolev1.CallResult{WorkflowId: id, RunId: id, Took: durationpb.New(time.Since(start))}

	if err != nil {
		res.Error, res.ErrorType = Readable(err)

		return res
	}

	program, _ := json.Marshal(prog) //nolint:errchkjson // marshaled once already by Execute
	masked := redact.ParseProgram(program).MaskResult(out)

	if json.Valid(masked) {
		res.Output = string(masked)
	} else {
		res.Raw = masked
	}

	return res
}

// testBinding is what TestBinding runs: an unsaved definition (version
// 0), or a saved version.
func (a BindingAPI) testBinding(
	ctx context.Context, req *consolev1.TestBindingRequest,
) (bindings.Binding, int64, error) {
	hook, def := req.GetHook(), req.GetDefinition()

	if def != nil {
		if req.GetVersion() != 0 {
			return bindings.Binding{}, 0, invalid("version and definition are exclusive")
		}

		switch {
		case hook == "":
			hook = def.GetHook()
		case def.GetHook() != "" && def.GetHook() != hook:
			return bindings.Binding{}, 0, invalid("the definition is of %s, not %s", def.GetHook(), hook)
		}

		b := bindings.BindingFromPB(def)
		b.Hook = hook

		return b, 0, nil
	}

	if !bindings.IsFullName(hook) {
		return bindings.Binding{}, 0, invalid("hook must be <service>.<Hook>, got %q", hook)
	}

	v, err := a.versions.Version(ctx, hook, int64(min(req.GetVersion(), maxVersion))) //nolint:gosec // bounded
	if err != nil {
		return bindings.Binding{}, 0, err //nolint:wrapcheck // mapped by status
	}

	if v.Deleted {
		return bindings.Binding{}, 0, status.Errorf(codes.FailedPrecondition,
			"%s: version %d is a delete: no binding to test", hook, v.Version)
	}

	return v.Binding, v.Version, nil
}

// Phases of a listing over the two sources of runs.
const (
	phaseDirect   = 1
	phaseTemporal = 2
)

// Token is the page token of a listing over the two sources of runs: the
// direct runs first (phase 1, their own cursor), then Temporal's (phase
// 2, its token).
type Token struct {
	Phase    int    `json:"p"`
	Direct   []byte `json:"d,omitempty"`
	Temporal []byte `json:"t,omitempty"`
}

// ParseToken reads a token; empty is the first page.
func ParseToken(data []byte) (Token, error) {
	if len(data) == 0 {
		return Token{Phase: phaseDirect}, nil
	}

	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return Token{}, invalid("page token: %v", err)
	}

	return t, nil
}

// Bytes is the token's wire form.
func (t Token) Bytes() []byte {
	out, _ := json.Marshal(t) //nolint:errchkjson // plain struct

	return out
}

// listRuns is a page of a source's runs over both phases: the direct runs
// until they end, then the Temporal ones through next (nil: no second
// phase).
func (s *Service) listRuns(
	ctx context.Context, kind Kind, source string, test bool, st consolev1.RunStatus, size uint32, token []byte,
	next func(token []byte) ([]*consolev1.Run, []byte, string, error),
) ([]*consolev1.Run, []byte, string, error) {
	tok, err := ParseToken(token)
	if err != nil {
		return nil, nil, "", err
	}

	if s == nil {
		tok = Token{Phase: phaseTemporal}
	}

	if tok.Phase <= phaseDirect {
		page, err := s.runs.List(ctx, kind, source, test, st, size, tok.Direct)
		if err != nil {
			return nil, nil, "", err
		}

		switch {
		case page.Next != nil:
			return page.Runs, Token{Phase: phaseDirect, Direct: page.Next}.Bytes(), "", nil
		case next == nil:
			return page.Runs, nil, "", nil
		case len(page.Runs) > 0:
			return page.Runs, Token{Phase: phaseTemporal}.Bytes(), "", nil
		}

		tok = Token{Phase: phaseTemporal}
	}

	if next == nil {
		return nil, nil, "", nil
	}

	runs, after, query, err := next(tok.Temporal)
	if err != nil {
		return nil, nil, "", err
	}

	if after == nil {
		return runs, nil, query, nil
	}

	return runs, Token{Phase: phaseTemporal, Temporal: after}.Bytes(), query, nil
}

// ListBindingRuns implements BindingService.
func (a BindingAPI) ListBindingRuns(
	ctx context.Context, req *consolev1.ListBindingRunsRequest,
) (*consolev1.ListBindingRunsResponse, error) {
	if !bindings.IsFullName(req.GetHook()) {
		return nil, invalid("hook must be <service>.<Hook>, got %q", req.GetHook())
	}

	var next func(token []byte) ([]*consolev1.Run, []byte, string, error)
	if a.t != nil && a.t.List != nil {
		next = func(token []byte) ([]*consolev1.Run, []byte, string, error) {
			res, err := a.t.List(ctx, &consolev1.ListBindingRunsRequest{
				Hook: req.GetHook(), Status: req.GetStatus(), Tests: req.GetTests(), PageSize: req.GetPageSize(),
				PageToken: token,
			})
			if err != nil {
				return nil, nil, "", err
			}

			return res.GetRuns(), res.GetNextPageToken(), res.GetQuery(), nil
		}
	}

	runs, token, query, err := a.s.listRuns(ctx, KindBinding, req.GetHook(), req.GetTests(), req.GetStatus(),
		req.GetPageSize(), req.GetPageToken(), next)
	if err != nil {
		return nil, a.s.status(ctx, err)
	}

	return &consolev1.ListBindingRunsResponse{Runs: runs, NextPageToken: token, Query: query}, nil
}

// GetBindingRun implements BindingService.
func (a BindingAPI) GetBindingRun(
	ctx context.Context, req *consolev1.GetBindingRunRequest,
) (*consolev1.GetBindingRunResponse, error) {
	if !wire.IsDirectRun(req.GetWorkflowId()) || a.s == nil {
		if a.t == nil || a.t.Get == nil {
			return nil, status.Errorf(codes.NotFound, "%s is not a direct run and this backplane has no temporal",
				req.GetWorkflowId())
		}

		return a.t.Get(ctx, req)
	}

	run, steps, info, err := a.s.runs.Get(ctx, req.GetWorkflowId())
	if err != nil {
		return nil, a.s.status(ctx, err)
	}

	if info.Kind != KindBinding {
		return nil, status.Errorf(codes.NotFound, "%s is a %s run, not a binding run", req.GetWorkflowId(), info.Kind)
	}

	return &consolev1.GetBindingRunResponse{
		Run: run, Hook: info.Source, Test: info.Test, Steps: steps,
		Version: uint64(max(info.Version, 0)),
	}, nil
}

// CancelBindingRun implements BindingService.
func (a BindingAPI) CancelBindingRun(
	ctx context.Context, req *consolev1.CancelBindingRunRequest,
) (*consolev1.CancelBindingRunResponse, error) {
	if !wire.IsDirectRun(req.GetWorkflowId()) || a.s == nil {
		if a.t == nil || a.t.Cancel == nil {
			return nil, status.Errorf(codes.NotFound, "%s is not a direct run and this backplane has no temporal",
				req.GetWorkflowId())
		}

		return a.t.Cancel(ctx, req)
	}

	if err := a.s.CancelRun(req.GetWorkflowId()); err != nil {
		return nil, a.s.status(ctx, err)
	}

	a.s.Log().Ctx().Info(ctx, "console action", xlog.String("actor", a.s.author(ctx)),
		xlog.String("action", "binding.run.cancel"), xlog.String("subject", req.GetWorkflowId()))

	return &consolev1.CancelBindingRunResponse{}, nil
}

// CancelRun cancels a direct run in flight on this replica; one not
// running here is FAILED_PRECONDITION: a direct run can only be canceled
// where it runs.
func (s *Service) CancelRun(id string) error {
	if s.Cancel(id) {
		return nil
	}

	return status.Errorf(codes.FailedPrecondition,
		"%s is not running on this replica: a direct run is canceled on the replica that runs it", id)
}

// ListRuleRuns is the rules engine's listing over both sources.
func (s *Service) ListRuleRuns(
	ctx context.Context, rule string, test bool, st consolev1.RunStatus, size uint32, token []byte,
	next func(token []byte) ([]*consolev1.Run, []byte, string, error),
) ([]*consolev1.Run, []byte, string, error) {
	return s.listRuns(ctx, KindRule, "rule/"+rule, test, st, size, token, next)
}

// GetRuleRun is one direct rule run for the rules engine's GetRuleRun.
func (s *Service) GetRuleRun(ctx context.Context, id string) (*consolev1.GetRuleRunResponse, error) {
	run, steps, info, err := s.runs.Get(ctx, id)
	if err != nil {
		return nil, s.status(ctx, err)
	}

	if info.Kind != KindRule {
		return nil, status.Errorf(codes.NotFound, "%s is a %s run, not a rule run", id, info.Kind)
	}

	return &consolev1.GetRuleRunResponse{
		Run: run, Version: uint64(max(info.Version, 0)), Test: info.Test, Steps: steps,
	}, nil
}

// String names the service in logs.
func (s *Service) String() string { return fmt.Sprintf("direct runtime (retention %s)", s.retention) }
