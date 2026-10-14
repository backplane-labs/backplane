package direct_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// fakeBindings holds the active binding of every hook, by text.
type fakeBindings struct {
	active map[string]bindings.Binding
	cat    bindings.Catalog
}

func (f *fakeBindings) Active(_ context.Context, hook string) (bindings.Binding, int64, error) {
	b, ok := f.active[hook]
	if !ok {
		return bindings.Binding{}, 0, bindings.ErrNoBinding
	}

	return b, 3, nil
}

func (f *fakeBindings) Catalog() (bindings.Catalog, error) { return f.cat, nil }

func (f *fakeBindings) Version(_ context.Context, hook string, version int64) (bindings.BindingVersion, error) {
	b, ok := f.active[hook]
	if !ok || (version != 0 && version != 3) {
		return bindings.BindingVersion{}, bindings.ErrNoVersion
	}

	return bindings.BindingVersion{Hook: hook, Version: 3, Binding: b}, nil
}

// fakeTemporal is a TemporalRunner answering a fixed result.
type fakeTemporal struct{ hooks []string }

func (f *fakeTemporal) RunBinding(
	_ context.Context, hook string, _ *bindings.Program, _ int64, _ *backplanev1.HookCall,
) (*backplanev1.HookResult, error) {
	f.hooks = append(f.hooks, hook)

	return &backplanev1.HookResult{Payload: []byte(`{"by":"temporal"}`)}, nil
}

func binding(t *testing.T, text string) bindings.Binding {
	t.Helper()

	b, err := bindings.BindingYAML(text)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// service is a direct runtime over a fake activity transport, without a
// store (runs are not recorded) and with the fixture installation
// registered: a dummy serving instance per service so Required reads
// manifests.
func service(t *testing.T, acts map[string]handler, b *fakeBindings, opts ...direct.Option) (*direct.Service, *fake) {
	t.Helper()

	hub := registry.NewHub()
	publish(hub)

	f := newFake(acts)

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	s := direct.New(h.Root(), b, hub, deps.Dependency[*store.Store]{}, "secret",
		append([]direct.Option{direct.WithCaller(f)}, opts...)...)
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return s, f
}

// A hook call runs the active binding on the direct runtime and answers
// its result; a required hook without a binding is a NoBinding failure,
// an optional one answers {}; a binding on the Temporal runtime goes to
// the Temporal runner, or fails without one; bad input is a failure.
func TestCallHook(t *testing.T) {
	t.Parallel()

	fakeB := &fakeBindings{cat: catalog(), active: map[string]bindings.Binding{
		"iam.SendEmail": binding(t, `
hook: iam.SendEmail
steps:
  send: {activity: smtp.Send, input: {to: req.to}}
result: {id: send.id}
`),
		"iam.Audit": binding(t, "hook: iam.Audit\nruntime: temporal\nsteps: {log: {activity: billing.Charge, input: {x: 1}}}\n"),
	}}

	s, f := service(t, map[string]handler{
		"smtp.Send": func(_ context.Context, in map[string]any) (any, error) {
			return map[string]any{"id": "m-" + in["to"].(string)}, nil
		},
	}, fakeB)

	res, err := s.CallHook(t.Context(), &backplanev1.HookCall{
		Hook: "iam.SendEmail", Instance: "iam-1", Payload: []byte(`{"to":"ann"}`), Deadline: durationpb.New(5 * time.Second),
	})
	if err != nil || string(res.GetPayload()) != `{"id":"m-ann"}` {
		t.Fatalf("%s %v", res.GetPayload(), err)
	}

	call := f.of("send")
	if call.GetBinding() != "iam.SendEmail@3" || !strings.HasPrefix(call.GetKey(), wire.DirectRunPrefix+"binding/iam.SendEmail/") {
		t.Fatalf("call %v", call)
	}

	// Temporal runtime without a runner.
	_, err = s.CallHook(t.Context(), &backplanev1.HookCall{Hook: "iam.Audit", Payload: []byte(`{}`)})
	if !errors.Is(err, direct.ErrTemporalRuntime) {
		t.Fatalf("temporal without runner: %v", err)
	}

	// Required hook without a binding; optional answers {}.
	delete(fakeB.active, "iam.SendEmail")

	_, err = s.CallHook(t.Context(), &backplanev1.HookCall{Hook: "iam.SendEmail", Instance: "iam-1"})
	if msg, typ := direct.Readable(err); typ != wire.NoBindingType || msg != "no binding for iam.SendEmail" {
		t.Fatalf("no binding: %s / %s", msg, typ)
	}

	delete(fakeB.active, "iam.Audit")

	res, err = s.CallHook(t.Context(), &backplanev1.HookCall{Hook: "iam.Audit"})
	if err != nil || string(res.GetPayload()) != "{}" {
		t.Fatalf("optional: %s %v", res.GetPayload(), err)
	}

	// Bad input.
	_, err = s.CallHook(t.Context(), &backplanev1.HookCall{Hook: "iam.Audit", Payload: []byte("nope")})
	if _, typ := direct.Readable(err); typ != wire.HookFailedType {
		t.Fatalf("bad input: %v", err)
	}

	// A Temporal runner takes Temporal-runtime bindings.
	fakeT := &fakeTemporal{}
	fakeB.active["iam.Audit"] = binding(t, "hook: iam.Audit\nruntime: temporal\nsteps: {log: {activity: billing.Charge}}\n")

	direct.WithTemporal(fakeT)(s)

	res, err = s.CallHook(t.Context(), &backplanev1.HookCall{Hook: "iam.Audit", Payload: []byte(`{}`)})
	if err != nil || string(res.GetPayload()) != `{"by":"temporal"}` || len(fakeT.hooks) != 1 {
		t.Fatalf("temporal runner: %s %v %v", res.GetPayload(), err, fakeT.hooks)
	}
}

// HookService answers the call's status: the run's failure as ABORTED with
// its type, no binding as FAILED_PRECONDITION, a run past its deadline as
// DEADLINE_EXCEEDED, an empty hook as INVALID_ARGUMENT.
func TestHookService(t *testing.T) {
	t.Parallel()

	fakeB := &fakeBindings{cat: catalog(), active: map[string]bindings.Binding{
		"iam.SendEmail": binding(t, "hook: iam.SendEmail\nsteps: {send: {activity: smtp.Send, input: {to: req.to}}}\n"),
	}}

	s, _ := service(t, map[string]handler{
		"smtp.Send": func(ctx context.Context, in map[string]any) (any, error) {
			if in["to"] == "slow" {
				<-ctx.Done()

				return nil, ctx.Err()
			}

			return nil, nonRetryable("smtp.Send: refused")
		},
	}, fakeB)

	api := s.HookService()
	raise := func(call *backplanev1.HookCall) error {
		_, err := api.Raise(t.Context(), &consolev1.RaiseRequest{Call: call})

		return err
	}

	err := raise(&backplanev1.HookCall{Hook: "iam.SendEmail", Payload: []byte(`{"to":"a"}`)})

	st, _ := status.FromError(err)
	if st.Code() != codes.Aborted || st.Message() != "step send: smtp.Send: refused" {
		t.Fatalf("failed run: %v", err)
	}

	var detail *backplanev1.CallFailure

	for _, d := range st.Details() {
		detail, _ = d.(*backplanev1.CallFailure)
	}

	if detail == nil || detail.GetType() != wire.StepFailedType || detail.GetRetryable() {
		t.Fatalf("detail %v", detail)
	}

	err = raise(&backplanev1.HookCall{
		Hook: "iam.SendEmail", Payload: []byte(`{"to":"slow"}`), Deadline: durationpb.New(50 * time.Millisecond),
	})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("deadline: %v", err)
	}

	if err = raise(&backplanev1.HookCall{Hook: "iam.Nope", Instance: "iam-1"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no binding: %v", err)
	}

	if err = raise(&backplanev1.HookCall{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty: %v", err)
	}
}

// The console's TestBinding runs a direct-runtime definition — unsaved or
// a saved version — and answers its result; an invalid definition its
// violations; a Temporal-runtime one goes to the Temporal side or fails
// without one. A run that is not here cannot be canceled.
func TestBindingAPI(t *testing.T) {
	t.Parallel()

	fakeB := &fakeBindings{cat: catalog(), active: map[string]bindings.Binding{
		"iam.SendEmail": binding(t, "hook: iam.SendEmail\nsteps: {send: {activity: smtp.Send, input: {to: req.to}}}\nresult: {id: send.id}\n"),
	}}

	s, _ := service(t, map[string]handler{
		"smtp.Send": func(_ context.Context, in map[string]any) (any, error) {
			return map[string]any{"id": fmt.Sprint("m-", in["to"])}, nil
		},
	}, fakeB)

	api := s.BindingService(nil, fakeB, nil)

	saved, err := api.TestBinding(t.Context(), &consolev1.TestBindingRequest{Hook: "iam.SendEmail", Input: `{"to":"a"}`})
	if err != nil || saved.GetVersion() != 3 || saved.GetResult().GetOutput() != `{"id":"m-a"}` ||
		!strings.HasPrefix(saved.GetResult().GetWorkflowId(), wire.DirectRunPrefix+wire.TestRunPrefix+"iam.SendEmail/") {
		t.Fatalf("saved: %v %v", saved, err)
	}

	def := &consolev1.BindingDefinition{Hook: "iam.SendEmail", Steps: map[string]*consolev1.Step{
		"send": {Activity: "smtp.Send", Input: bindings.Expr("req").PB()},
	}}

	unsaved, err := api.TestBinding(t.Context(), &consolev1.TestBindingRequest{Definition: def, Input: `{"to":"b"}`})
	if err != nil || unsaved.GetVersion() != 0 || unsaved.GetResult().GetOutput() != "{}" {
		t.Fatalf("unsaved: %v %v", unsaved, err)
	}

	bad := &consolev1.BindingDefinition{Hook: "iam.SendEmail", Steps: map[string]*consolev1.Step{
		"send": {Activity: "smtp.Send", Undo: "smtp.Recall"},
	}}

	invalid, err := api.TestBinding(t.Context(), &consolev1.TestBindingRequest{Definition: bad})
	if err != nil || len(invalid.GetViolations()) != 1 || invalid.GetViolations()[0].GetCode() != bindings.CodeRuntimeUnsupported {
		t.Fatalf("invalid: %v %v", invalid, err)
	}

	temporal := &consolev1.BindingDefinition{
		Hook: "iam.SendEmail", Runtime: consolev1.Runtime_RUNTIME_TEMPORAL,
		Steps: map[string]*consolev1.Step{"send": {Activity: "smtp.Send"}},
	}

	_, err = api.TestBinding(t.Context(), &consolev1.TestBindingRequest{Definition: temporal})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("temporal without side: %v", err)
	}

	called := false
	withTemporal := s.BindingService(nil, fakeB, &direct.TemporalBindings{
		Test: func(context.Context, *consolev1.TestBindingRequest, bindings.Binding, int64, *bindings.Program,
		) (*consolev1.TestBindingResponse, error) {
			called = true

			return &consolev1.TestBindingResponse{}, nil
		},
	})

	if _, err = withTemporal.TestBinding(t.Context(), &consolev1.TestBindingRequest{Definition: temporal}); err != nil || !called {
		t.Fatalf("temporal side: %v %v", err, called)
	}

	_, err = api.CancelBindingRun(t.Context(), &consolev1.CancelBindingRunRequest{WorkflowId: "direct/binding/iam.SendEmail/x"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("cancel: %v", err)
	}

	_, err = api.GetBindingRun(t.Context(), &consolev1.GetBindingRunRequest{WorkflowId: "binding/iam.SendEmail/x"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("temporal run without side: %v", err)
	}
}

// Page tokens round-trip through both phases.
func TestToken(t *testing.T) {
	t.Parallel()

	first, err := direct.ParseToken(nil)
	if err != nil || first.Phase != 1 {
		t.Fatalf("%v %v", first, err)
	}

	tok := direct.Token{Phase: 2, Temporal: []byte("abc")}

	back, err := direct.ParseToken(tok.Bytes())
	if err != nil || back.Phase != 2 || string(back.Temporal) != "abc" {
		t.Fatalf("%v %v", back, err)
	}

	if _, err := direct.ParseToken([]byte("{")); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad token: %v", err)
	}
}
