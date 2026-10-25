package direct_test

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/seal"
)

// A keyed hook call runs once: a repeat, concurrent or later, answers the
// first one's unmasked result; a failed run runs again; the key comes in
// the envelope or the SDK's header; other keys and unkeyed calls run.
func TestKeyedHookCall(t *testing.T) {
	t.Parallel()

	ms := secretInstallation(t)
	fakeB := &fakeBindings{
		cat: bindings.WithDefaults(ms, bindings.Defaults{Runtime: consolev1.Runtime_RUNTIME_DIRECT}),
		active: map[string]bindings.Binding{
			"iam.SendOTP": binding(t, "hook: iam.SendOTP\nsteps: {send: {activity: smtp.Send, input: {to: req.to}}}\n"+
				"result: {code: req.code, id: send.id}\n"),
		},
	}

	var (
		sent    atomic.Int32
		fail    atomic.Bool
		release = make(chan struct{})
	)

	s, _ := live(t, map[string]handler{
		"smtp.Send": func(_ context.Context, in map[string]any) (any, error) {
			if in["to"] == "wait" {
				<-release
			}

			if fail.Load() {
				return nil, nonRetryable("smtp.Send: refused")
			}

			return map[string]any{"id": "m-" + in["to"].(string) + "-" + strconv.Itoa(int(sent.Add(1)))}, nil
		},
	}, fakeB)

	raise := func(ctx context.Context, key, to, code string) (string, error) {
		res, err := s.HookService().Raise(ctx, &consolev1.RaiseRequest{Call: &backplanev1.HookCall{
			Hook: "iam.SendOTP", Instance: "iam-1", Key: key, Payload: []byte(`{"to":"` + to + `","code":"` + code + `"}`),
		}})

		return string(res.GetResult().GetPayload()), err
	}

	// Concurrent: the second call waits for the first one's run.
	var (
		calls   sync.WaitGroup
		results [2]string
		errs    [2]error
	)

	for i := range 2 {
		calls.Go(func() { results[i], errs[i] = raise(t.Context(), "k1", "wait", "1234") })
	}

	awaitRun(t, s, wire.DirectRunID("iam.SendOTP", "key/k1"), consolev1.RunStatus_RUN_STATUS_RUNNING)
	close(release)
	calls.Wait()

	want := `{"code":"1234","id":"m-wait-1"}`
	if errs[0] != nil || errs[1] != nil || results[0] != want || results[1] != want {
		t.Fatalf("concurrent: %q %v, %q %v", results[0], errs[0], results[1], errs[1])
	}

	// Later, through the header, with another payload: the first result,
	// unmasked, and no second send.
	header := metadata.NewIncomingContext(t.Context(), metadata.Pairs("bp-call-key", "k1"))
	if got, err := raise(header, "", "other", "9"); err != nil || got != want {
		t.Fatalf("repeat: %q %v", got, err)
	}

	if n := sent.Load(); n != 1 {
		t.Fatalf("sent %d times", n)
	}

	// Another key and an unkeyed call run.
	if got, err := raise(t.Context(), "k2", "b", "5"); err != nil || got != `{"code":"5","id":"m-b-2"}` {
		t.Fatalf("other key: %q %v", got, err)
	}

	if got, err := raise(t.Context(), "", "b", "5"); err != nil || got != `{"code":"5","id":"m-b-3"}` {
		t.Fatalf("unkeyed: %q %v", got, err)
	}

	// A failed run: its repeat runs it again.
	fail.Store(true)

	if _, err := raise(t.Context(), "k3", "c", "7"); err == nil {
		t.Fatal("failing run succeeded")
	}

	awaitRun(t, s, wire.DirectRunID("iam.SendOTP", "key/k3"), consolev1.RunStatus_RUN_STATUS_FAILED)
	fail.Store(false)

	if got, err := raise(t.Context(), "k3", "c", "7"); err != nil || got != `{"code":"7","id":"m-c-4"}` {
		t.Fatalf("after failure: %q %v", got, err)
	}

	// The console shows the run masked.
	run := awaitRun(t, s, wire.DirectRunID("iam.SendOTP", "key/k1"), consolev1.RunStatus_RUN_STATUS_COMPLETED)
	if run.GetResult() != `{"code":"***","id":"m-wait-1"}` {
		t.Fatalf("console result %s", run.GetResult())
	}
}

// A program expired with its last run is written again by the next run
// that claims it.
func TestClaimAfterProgramExpired(t *testing.T) {
	t.Parallel()

	s, _ := live(t, map[string]handler{
		"billing.Charge": func(_ context.Context, in map[string]any) (any, error) { return in, nil },
	}, &fakeBindings{cat: catalog()})

	prog := rule(t, "event: iam.UserRegistered\nsteps: {charge: {activity: billing.Charge, input: {who: event.email}}}\n")
	start := func(ce string) error {
		return s.Rules().StartRule(t.Context(), "r1", 1, prog, []byte(`{"email":"a@b"}`),
			bindings.Meta{ID: ce}, nil, "rule/r1/"+ce)
	}

	if err := start("ce-1"); err != nil {
		t.Fatal(err)
	}

	awaitRun(t, s, direct.RunID("rule/r1/ce-1"), consolev1.RunStatus_RUN_STATUS_COMPLETED)

	if err := s.Runs().Expire(t.Context(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if err := start("ce-2"); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

// With history encryption a kept reply is sealed to its run: a repeat
// answers it, and without the key it does not open.
func TestKeyedReplySealed(t *testing.T) {
	t.Parallel()

	ms := secretInstallation(t)
	fakeB := &fakeBindings{
		cat: bindings.WithDefaults(ms, bindings.Defaults{Runtime: consolev1.Runtime_RUNTIME_DIRECT}),
		active: map[string]bindings.Binding{
			"iam.SendOTP": binding(t, "hook: iam.SendOTP\nsteps: {send: {activity: smtp.Send, input: {to: req.to}}}\n"+
				"result: {code: req.code, id: send.id}\n"),
		},
	}

	var sent atomic.Int32

	s, _ := live(t, map[string]handler{
		"smtp.Send": func(context.Context, map[string]any) (any, error) {
			return map[string]any{"id": "m-" + strconv.Itoa(int(sent.Add(1)))}, nil
		},
	}, fakeB)

	sealer, err := seal.Open(config.Seal{Key: config.Secret(seal.GenerateKey())})
	if err != nil {
		t.Fatal(err)
	}

	var keyed atomic.Bool

	keyed.Store(true)
	s.Runs().SealReplies(func() seal.Sealer {
		if keyed.Load() {
			return sealer
		}

		return nil
	})

	raise := func(key string) (string, error) {
		res, err := s.HookService().Raise(t.Context(), &consolev1.RaiseRequest{Call: &backplanev1.HookCall{
			Hook: "iam.SendOTP", Instance: "iam-1", Key: key, Payload: []byte(`{"to":"a","code":"1234"}`),
		}})

		return string(res.GetResult().GetPayload()), err
	}

	first, err := raise("k1")
	if err != nil || first != `{"code":"1234","id":"m-1"}` {
		t.Fatalf("first: %q %v", first, err)
	}

	awaitRun(t, s, wire.DirectRunID("iam.SendOTP", "key/k1"), consolev1.RunStatus_RUN_STATUS_COMPLETED)

	if again, err := raise("k1"); err != nil || again != first || sent.Load() != 1 {
		t.Fatalf("repeat: %q %v (sent %d)", again, err, sent.Load())
	}

	keyed.Store(false)

	if _, err := raise("k1"); err == nil {
		t.Fatal("a sealed reply answered without the key")
	}
}
