package direct_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	sp "github.com/gopherex/schemapb/go/schemapb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

type otpIn struct {
	To   string `json:"to"`
	Code string `json:"code" schemapb:"secret=true"`
}

// secretInstallation adds iam.SendOTP, whose code is secret, to the
// installation.
func secretInstallation(t *testing.T) bindings.Manifests {
	t.Helper()

	s, err := sp.ReflectType[otpIn](sp.ID(sp.Namespace("iam"), sp.SchemaName("otp"), sp.Ver(1, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}

	ms := installation()
	ms[0].Hooks = append(ms[0].Hooks, &backplanev1.Hook{Name: "SendOTP", Input: s})

	return ms
}

// live is a direct runtime over a scratch PostgreSQL database
// (BACKPLANE_TEST_PG, skipped without) and a fake activity transport.
func live(t *testing.T, acts map[string]handler, fakeB *fakeBindings) (*direct.Service, *fake) {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	ctx := t.Context()

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	name := "backplane_direct_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if _, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}

		admin.Close(cleanup)
	})

	scratch := strings.Replace(dsn, "/backplane?", "/"+name+"?", 1)

	hub := registry.NewHub()
	publish(hub)

	f := newFake(acts)

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(scratch)}))
	s := direct.New(h.Root(), fakeB, hub, st, "secret", direct.WithCaller(f), direct.Retention(time.Hour))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return s, f
}

// awaitRun polls the store for the run until it has the status.
func awaitRun(t *testing.T, s *direct.Service, id string, status consolev1.RunStatus) *consolev1.GetRunResponse {
	t.Helper()

	var last string

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		run, _, _, err := s.Runs().Get(t.Context(), id)
		if err == nil && run.GetRun().GetStatus() == status {
			return run
		}

		last = run.String() + " " + errText(err)
	}

	t.Fatalf("run %s not %v: %s", id, status, last)

	return nil
}

// awaitList polls the listing of a hook's runs until it has n of them.
func awaitList(t *testing.T, s *direct.Service, hook string, test bool, n int) direct.Page {
	t.Helper()

	var last string

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		page, err := s.Runs().List(t.Context(), direct.KindBinding, hook, test, 0, 10, nil)
		if err == nil && len(page.Runs) == n {
			return page
		}

		last = errText(err)
	}

	t.Fatalf("%d runs of %s not listed: %s", n, hook, last)

	return direct.Page{}
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// A hook call's run is recorded with its steps, payloads masked by the
// program; the console reads it back and lists it newest first with
// paging; a test run lands among the tests.
func TestRunsRecorded(t *testing.T) {
	t.Parallel()

	ms := secretInstallation(t)
	fakeB := &fakeBindings{
		cat: bindings.WithDefaults(ms, bindings.Defaults{Runtime: consolev1.Runtime_RUNTIME_DIRECT}),
		active: map[string]bindings.Binding{
			"iam.SendOTP": binding(t, "hook: iam.SendOTP\nsteps: {send: {activity: smtp.Send, input: {to: req.to, text: req.code}}}\nresult: {code: req.code, id: send.id}\n"),
		},
	}

	s, _ := live(t, map[string]handler{
		"smtp.Send": func(_ context.Context, in map[string]any) (any, error) {
			return map[string]any{"id": "m-" + in["to"].(string)}, nil
		},
	}, fakeB)

	recipients := []string{"a", "b", "c"}
	ids := make([]string, 0, len(recipients))

	for i, to := range recipients {
		res, err := s.CallHook(t.Context(), &backplanev1.HookCall{
			Hook: "iam.SendOTP", Instance: "iam-1", Payload: []byte(`{"to":"` + to + `","code":"1234"}`),
		})
		if err != nil || string(res.GetPayload()) != `{"code":"1234","id":"m-`+to+`"}` {
			t.Fatalf("%s: %s %v", to, res.GetPayload(), err)
		}

		// Records are written behind the run: wait for this one.
		page := awaitList(t, s, "iam.SendOTP", false, i+1)
		ids = append(ids, page.Runs[0].GetWorkflowId())
	}

	// The newest run: input, result and the step masked; the result's id
	// not.
	run := awaitRun(t, s, ids[2], consolev1.RunStatus_RUN_STATUS_COMPLETED)
	if run.GetInput() != `{"code":"***","to":"c"}` || run.GetResult() != `{"code":"***","id":"m-c"}` {
		t.Fatalf("masked run: input %s result %s", run.GetInput(), run.GetResult())
	}

	_, steps, info, err := s.Runs().Get(t.Context(), ids[2])
	if err != nil || len(steps) != 1 || steps[0].GetStep() != "send" || steps[0].GetInput() != `{"text":"***","to":"c"}` ||
		steps[0].GetOutput() != `{"id":"m-c"}` || steps[0].GetStatus() != consolev1.StepRunStatus_STEP_RUN_STATUS_COMPLETED {
		t.Fatalf("steps %v %v", steps, err)
	}

	if info.Kind != direct.KindBinding || info.Source != "iam.SendOTP" || info.Version != 3 || info.Test {
		t.Fatalf("info %+v", info)
	}

	if run.GetRun().GetWorkflowType() != wire.DirectWorkflowType || run.GetRun().GetMemo()[wire.MemoBinding] != `"iam.SendOTP@3"` ||
		run.GetRun().GetMemo()[wire.MemoSource] != `"iam-1"` {
		t.Fatalf("run %v", run.GetRun())
	}

	// Paging: two pages of two and one, newest first.
	first, err := s.Runs().List(t.Context(), direct.KindBinding, "iam.SendOTP", false, 0, 2, nil)
	if err != nil || len(first.Runs) != 2 || first.Next == nil || first.Runs[0].GetWorkflowId() != ids[2] ||
		first.Runs[1].GetWorkflowId() != ids[1] {
		t.Fatalf("first page %v %v", first, err)
	}

	second, err := s.Runs().List(t.Context(), direct.KindBinding, "iam.SendOTP", false, 0, 2, first.Next)
	if err != nil || len(second.Runs) != 1 || second.Next != nil || second.Runs[0].GetWorkflowId() != ids[0] {
		t.Fatalf("second page %v %v", second, err)
	}

	// Only completed ones; none failed.
	failed, err := s.Runs().List(t.Context(), direct.KindBinding, "iam.SendOTP", false,
		consolev1.RunStatus_RUN_STATUS_FAILED, 10, nil)
	if err != nil || len(failed.Runs) != 0 {
		t.Fatalf("failed %v %v", failed, err)
	}

	// A test run is listed among the tests, not the calls.
	prog, err := bindings.CompileBinding(fakeB.active["iam.SendOTP"], fakeB.cat)
	if err != nil {
		t.Fatal(err)
	}

	res := s.Test(t.Context(), "iam.SendOTP", prog, 3, []byte(`{"to":"t","code":"9"}`), 5*time.Second, "console:x")
	if res.GetError() != "" || res.GetOutput() != `{"code":"***","id":"m-t"}` {
		t.Fatalf("test %v", res)
	}

	awaitRun(t, s, res.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED)

	if tests := awaitList(t, s, "iam.SendOTP", true, 1); tests.Runs[0].GetWorkflowId() != res.GetWorkflowId() {
		t.Fatalf("tests %v", tests)
	}

	// Retention: runs older than the cutoff go, with their steps.
	if err := s.Runs().Expire(t.Context(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := s.Runs().Get(t.Context(), ids[0]); !errors.Is(err, direct.ErrNoRun) {
		t.Fatalf("expired: %v", err)
	}
}

// A rule's event runs once: a second claim of the id while it runs or
// after it completed is ErrAlreadyStarted; a failed run is claimed again
// and counts its attempt; a failed run is recorded with its error.
func TestRuleDedup(t *testing.T) {
	t.Parallel()

	fakeB := &fakeBindings{cat: catalog()}

	var fail bool

	s, f := live(t, map[string]handler{
		"billing.Charge": func(_ context.Context, in map[string]any) (any, error) {
			if fail {
				return nil, nonRetryable("billing.Charge: declined")
			}

			return in, nil
		},
	}, fakeB)

	prog := rule(t, "event: iam.UserRegistered\nsteps: {charge: {activity: billing.Charge, input: {who: event.email}}}\n")
	starter := s.Rules()
	wid := "rule/" + uuid.NewString() + "/ce-1"

	call := func() error {
		return starter.StartRule(t.Context(), "r1", 2, prog, []byte(`{"email":"a@b"}`),
			bindings.Meta{ID: "ce-1", Type: "iam.UserRegistered"}, nil, wid)
	}

	if err := call(); err != nil {
		t.Fatal(err)
	}

	if err := call(); !errors.Is(err, direct.ErrAlreadyStarted) {
		t.Fatalf("second claim: %v", err)
	}

	if len(f.steps()) != 1 {
		t.Fatalf("calls %v", f.steps())
	}

	run := awaitRun(t, s, direct.RunID(wid), consolev1.RunStatus_RUN_STATUS_COMPLETED)
	if run.GetRun().GetMemo()[wire.MemoBinding] != `"rule:r1@2"` || run.GetInput() != `{"email":"a@b"}` {
		t.Fatalf("run %v", run)
	}

	// A failed run: recorded failed, claimed again by the next delivery.
	fail = true
	wid2 := "rule/" + uuid.NewString() + "/ce-2"

	err := starter.StartRule(t.Context(), "r1", 2, prog, []byte(`{"email":"c@d"}`), bindings.Meta{ID: "ce-2"}, nil, wid2)
	if msg, typ := direct.Readable(err); typ != wire.StepFailedType || msg != "step charge: billing.Charge: declined" {
		t.Fatalf("failed run: %v", err)
	}

	failed := awaitRun(t, s, direct.RunID(wid2), consolev1.RunStatus_RUN_STATUS_FAILED)
	if failed.GetFailure() != "step charge: billing.Charge: declined" || failed.GetFailureType() != wire.StepFailedType {
		t.Fatalf("failure %v", failed)
	}

	fail = false

	if err := starter.StartRule(t.Context(), "r1", 2, prog, []byte(`{"email":"c@d"}`), bindings.Meta{ID: "ce-2"}, nil, wid2); err != nil {
		t.Fatalf("claim again: %v", err)
	}

	awaitRun(t, s, direct.RunID(wid2), consolev1.RunStatus_RUN_STATUS_COMPLETED)

	// The rule's runs through the console side, newest first; the test
	// helper's result for a test run.
	runs, _, _, err := s.ListRuleRuns(t.Context(), "r1", false, 0, 10, nil, nil)
	if err != nil || len(runs) != 2 || runs[0].GetWorkflowId() != direct.RunID(wid2) {
		t.Fatalf("rule runs %v %v", runs, err)
	}

	got, err := s.GetRuleRun(t.Context(), direct.RunID(wid))
	if err != nil || got.GetVersion() != 2 || got.GetTest() || len(got.GetSteps()) != 1 {
		t.Fatalf("rule run %v %v", got, err)
	}

	res, err := starter.Test(t.Context(), "r1", 2, prog, []byte(`{"email":"t"}`), bindings.Meta{ID: "t-1"}, nil,
		"test/rule/r1/x", 5*time.Second)
	if err != nil || res.GetError() != "" || res.GetOutput() != "{}" || res.GetWorkflowId() != "direct/test/rule/r1/x" {
		t.Fatalf("test %v %v", res, err)
	}
}
