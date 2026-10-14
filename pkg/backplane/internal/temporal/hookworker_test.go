package temporal_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

// connect starts the client of service with only the workers asked for.
func connect(t *testing.T, name string, hooks, main bool) *temporal.Client {
	t.Helper()

	e := env.New(name, manifest.New(name, "0.0.0"))
	withHook(e)

	c := temporal.New(temporal.Params{
		Conn: inftemporal.Config{Addr: temporaltest.Addr(t)}, Service: name, Instance: name + "-1", Log: testlog.Discard(), Env: e,
	})
	c.UseFastRetry()

	g := temporaltest.NewGroup(t)

	if err := c.Connect(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	if hooks {
		if err := c.StartHookWorker(t.Context(), g); err != nil {
			t.Fatal(err)
		}
	}

	if main {
		if err := c.StartWorker(t.Context(), g); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		_ = c.StopWorker(context.Background())
		_ = c.StopHookWorker(context.Background())
		_ = c.Close(context.Background())
	})

	return c
}

// The hooks worker alone carries calls: they work before the main worker
// starts (the author's OnStart) and after it stops (OnStop).
func TestHookWorkerAlone(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("hw")
	c := connect(t, svc, true, false)
	temporaltest.Hooks(t, temporaltest.Dial(t), svc, greet(nil))

	ctx, cancel := context.WithTimeout(t.Context(), callLimit)
	defer cancel()

	if out, err := c.Call(ctx, svc+".Greet", []byte(`{"name":"start"}`)); err != nil ||
		string(out) != `{"text":"Hello, start"}` {
		t.Fatalf("call: %s %v", out, err)
	}

	// The main worker stopped: calls go on.
	if err := c.StopWorker(t.Context()); err != nil {
		t.Fatal(err)
	}

	if out, err := c.Call(ctx, svc+".Greet", []byte(`{"name":"stop"}`)); err != nil ||
		string(out) != `{"text":"Hello, stop"}` {
		t.Fatalf("call after the main worker stopped: %s %v", out, err)
	}
}

// The main worker does not serve CallHook: without the hooks worker a call
// waits out its deadline.
func TestMainWorkerHasNoCallHook(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("mw")
	c := connect(t, svc, false, true)
	temporaltest.Hooks(t, temporaltest.Dial(t), svc, greet(nil))

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	if _, err := c.Call(ctx, svc+".Greet", []byte(`{"name":"x"}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call without hooks worker: %v", err)
	}
}

// counting plays the binding of <svc>.Greet counting its executions; the
// name "slowly" takes a while, "rude" fails.
func counting(n *atomic.Int32) nexus.Operation[*backplanev1.HookCall, *backplanev1.HookResult] {
	return nexus.NewSyncOperation("Greet", func(
		_ context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
	) (*backplanev1.HookResult, error) {
		k := n.Add(1)

		var in text

		_ = json.Unmarshal(call.GetPayload(), &in)

		switch in.Name {
		case "slowly":
			time.Sleep(500 * time.Millisecond)
		case "rude":
			return nil, nexus.NewOperationFailedErrorf("%w", errRude)
		}

		out, _ := json.Marshal(text{Text: "Hello, " + in.Name, Name: strconv.Itoa(int(k))})

		return &backplanev1.HookResult{Payload: out}, nil
	})
}

func TestCallKey(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("key")
	c := connect(t, svc, true, false)

	var runs atomic.Int32

	temporaltest.Hooks(t, temporaltest.Dial(t), svc, counting(&runs))

	ctx, cancel := context.WithTimeout(t.Context(), callLimit)
	defer cancel()

	keyed := env.WithCallKey(ctx, "order-1")

	// Two at once: one execution, the same result.
	var (
		group sync.WaitGroup
		outs  [2]string
		errs  [2]error
	)

	for i := range 2 {
		group.Add(1)

		go func() {
			defer group.Done()

			out, err := c.Call(keyed, svc+".Greet", []byte(`{"name":"slowly"}`))
			outs[i], errs[i] = string(out), err
		}()
	}

	group.Wait()

	if errs[0] != nil || errs[1] != nil || outs[0] != outs[1] || runs.Load() != 1 {
		t.Fatalf("concurrent: %q %q %v %v, %d runs", outs[0], outs[1], errs[0], errs[1], runs.Load())
	}

	// After it completed: its result, no execution, whatever the input.
	out, err := c.Call(keyed, svc+".Greet", []byte(`{"name":"other"}`))
	if err != nil || string(out) != outs[0] || runs.Load() != 1 {
		t.Fatalf("after completion: %s %v, %d runs", out, err, runs.Load())
	}

	// Another key runs.
	if _, err := c.Call(env.WithCallKey(ctx, "order-2"), svc+".Greet", []byte(`{"name":"b"}`)); err != nil ||
		runs.Load() != 2 {
		t.Fatalf("other key: %v, %d runs", err, runs.Load())
	}

	// After a failure the key runs again.
	failed := env.WithCallKey(ctx, "order-3")
	if _, err := c.Call(failed, svc+".Greet", []byte(`{"name":"rude"}`)); err == nil || runs.Load() != 3 {
		t.Fatalf("failure: %v, %d runs", err, runs.Load())
	}

	if out, err = c.Call(failed, svc+".Greet", []byte(`{"name":"again"}`)); err != nil ||
		!strings.Contains(string(out), "Hello, again") || runs.Load() != 4 {
		t.Fatalf("after failure: %s %v, %d runs", out, err, runs.Load())
	}
}

// ensureAttributes registers the search attributes of hook calls on the
// dev server's namespace.
func ensureAttributes(t *testing.T, tc client.Client) {
	t.Helper()

	_, err := tc.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		Namespace: temporaltest.Namespace,
		SearchAttributes: map[string]enumspb.IndexedValueType{
			temporal.AttrService: enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			temporal.AttrHook:    enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		},
	})

	var exists *serviceerror.AlreadyExists
	if err != nil && !errors.As(err, &exists) {
		t.Fatalf("search attributes: %v", err)
	}
}

// A call records who raised it: memo source, and the search attributes
// once the namespace has them.
func TestCallMemoAndAttributes(t *testing.T) {
	t.Parallel()

	tc := temporaltest.Dial(t)
	ensureAttributes(t, tc)

	svc := temporaltest.Name("memo")
	c := connect(t, svc, true, false)
	temporaltest.Hooks(t, tc, svc, greet(nil))

	ctx, cancel := context.WithTimeout(t.Context(), callLimit)
	defer cancel()

	if _, err := c.Call(env.WithCallKey(ctx, "m1"), svc+".Greet", []byte(`{"name":"memo"}`)); err != nil {
		t.Fatal(err)
	}

	d, err := tc.DescribeWorkflowExecution(ctx, temporal.HookWorkflowID(svc, "Greet", "m1"), "")
	if err != nil {
		t.Fatal(err)
	}

	info := d.GetWorkflowExecutionInfo()

	var source string
	if err := converter.GetDefaultDataConverter().FromPayload(info.GetMemo().GetFields()[temporal.MemoSource], &source); err != nil ||
		source != svc+"/"+svc+"-1" {
		t.Fatalf("memo source %q: %v", source, err)
	}

	if info.GetTaskQueue() != temporal.HooksQueue(svc) || info.GetType().GetName() != temporal.CallHookWorkflow {
		t.Fatalf("queue %q type %q", info.GetTaskQueue(), info.GetType().GetName())
	}

	attrs := info.GetSearchAttributes().GetIndexedFields()

	var service, hookName string

	_ = converter.GetDefaultDataConverter().FromPayload(attrs[temporal.AttrService], &service)
	_ = converter.GetDefaultDataConverter().FromPayload(attrs[temporal.AttrHook], &hookName)

	if service != svc || hookName != svc+".Greet" {
		t.Fatalf("search attributes: %q %q", service, hookName)
	}
}

func TestConnected(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("conn")
	c := connect(t, svc, false, false)

	if !c.Connected() {
		t.Fatal("not connected after a successful dial")
	}

	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	if c.Connected() {
		t.Fatal("connected after Close")
	}
}
