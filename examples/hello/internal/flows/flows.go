// Package flows is hello's side of Temporal: the hook it raises (Greet),
// the activities it offers to bindings (Echo, and Welcome backed by a
// workflow), its own workflows (GreetMany, declared for the console;
// Report, available for an administrative schedule) and the activities those run.
package flows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/examples/hello/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

// Registered activity names of the service's own workflows.
const (
	Compose = "Compose"
	Total   = "Total"
	Record  = "RecordGreeting"
)

const (
	greetTimeout = 5 * time.Second
	stepTimeout  = 10 * time.Second
	echoAttempts = 3
	maxEcho      = 1 << 10
)

// ErrTooLong: Echo refuses a text over 1 KiB; retrying cannot help.
var ErrTooLong = errors.New("echo: text over 1 KiB")

// Payloads of the hook, the activities and the workflows.
type (
	GreetIn struct {
		Name string `json:"name"`
	}
	GreetOut struct {
		Text string `json:"text"`
	}
	EchoIn struct {
		Text string `json:"text"`
	}
	EchoOut struct {
		Text string `json:"text"`
		// Key is the execution's idempotency key (activity.InfoOf).
		Key string `json:"key"`
	}
	ManyIn struct {
		Names []string `json:"names"`
	}
	ManyOut struct {
		Texts []string `json:"texts"`
	}
)

// Flows is a component: hook, activities and workflows of the service.
type Flows struct {
	deps.Component

	// Greet is answered by whatever backplane binds to hello.Greet.
	Greet hook.Ref[GreetIn, GreetOut]

	greeter *greeter.Greeter
	db      deps.Dependency[*store.DB]
}

// New declares everything under parent.
func New(parent deps.Scope, g *greeter.Greeter, db deps.Dependency[*store.DB]) *Flows {
	f := &Flows{Component: deps.NewComponent(parent, "flows"), greeter: g, db: db}

	f.Greet = hook.Declare[GreetIn, GreetOut](f, "Greet", hook.Required(),
		hook.DefaultTimeout(greetTimeout), hook.Describe("greets a name; the local greeter is the fallback"))

	// What the service can do for bindings and rules.
	activity.Handle(f, "Echo", f.Echo,
		activity.StartToClose(stepTimeout), activity.HeartbeatTimeout(stepTimeout),
		activity.Retry(activity.RetryHint{Attempts: echoAttempts}), activity.Describe("returns its input"))
	workflows.Activity(f, "Welcome", f.Welcome,
		activity.StartToClose(time.Minute), activity.Describe("greets through the Greet hook, locally without a binding"))

	// The service's own workflows: one declared (the console starts it with
	// a form), the rest registered with their activities.
	workflows.Declare(f, "GreetMany", f.GreetMany, workflows.Describe("greets every name"))
	workflows.Declare(f, "Report", f.Report, workflows.Describe("reports greetings so far"))
	workflows.Register(f, func(r worker.Registry) {
		r.RegisterActivityWithOptions(f.compose, tactivity.RegisterOptions{Name: Compose})
		r.RegisterActivityWithOptions(f.total, tactivity.RegisterOptions{Name: Total})
		r.RegisterActivityWithOptions(f.record, tactivity.RegisterOptions{Name: Record})
	})

	return f
}

// Echo is the Echo activity.
func (f *Flows) Echo(ctx context.Context, in EchoIn) (EchoOut, error) {
	if len(in.Text) > maxEcho {
		return EchoOut{}, activity.NonRetryable(ErrTooLong) //nolint:wrapcheck // marks ErrTooLong, keeps it
	}

	info, _ := activity.InfoOf(ctx)
	activity.Heartbeat(ctx, "echoing")

	return EchoOut{Text: in.Text, Key: info.Key}, nil
}

// Welcome is the workflow-backed activity: it may wait and call hooks.
// Without a binding for Greet it greets locally.
func (f *Flows) Welcome(ctx workflow.Context, in GreetIn) (GreetOut, error) {
	out, err := workflows.CallHook(ctx, f.Greet, in, hook.Timeout(greetTimeout))
	if err == nil {
		if err := workflow.ExecuteActivity(steps(ctx), Record, in.Name, out.Text).Get(ctx, nil); err != nil {
			return GreetOut{}, fmt.Errorf("record greeting: %w", err)
		}

		return out, nil
	}

	if !errors.Is(err, hook.ErrNoBinding) {
		return GreetOut{}, fmt.Errorf("welcome: %w", err)
	}

	var text string
	if err := workflow.ExecuteActivity(steps(ctx), Compose, in.Name).Get(ctx, &text); err != nil {
		return GreetOut{}, fmt.Errorf("compose: %w", err)
	}

	return GreetOut{Text: text}, nil
}

// GreetMany greets every name, in order.
func (f *Flows) GreetMany(ctx workflow.Context, in ManyIn) (ManyOut, error) {
	out := ManyOut{Texts: make([]string, 0, len(in.Names))}

	for _, name := range in.Names {
		var text string
		if err := workflow.ExecuteActivity(steps(ctx), Compose, name).Get(ctx, &text); err != nil {
			return ManyOut{}, fmt.Errorf("compose %s: %w", name, err)
		}

		out.Texts = append(out.Texts, text)
	}

	return out, nil
}

// Report logs the greetings so far; an administrator may schedule it.
func (f *Flows) Report(ctx workflow.Context, _ struct{}) (uint64, error) {
	var total uint64
	if err := workflow.ExecuteActivity(steps(ctx), Total).Get(ctx, &total); err != nil {
		return 0, fmt.Errorf("total: %w", err)
	}

	workflow.GetLogger(ctx).Info("greetings so far", "total", total)

	return total, nil
}

func (f *Flows) compose(ctx context.Context, name string) (string, error) {
	text, err := f.greeter.Text(ctx, strings.TrimSpace(name))
	if err != nil {
		return "", fmt.Errorf("compose: %w", err)
	}

	return text, nil
}

func (f *Flows) total(ctx context.Context) (uint64, error) {
	f.Log().Ctx().Debug(ctx, "report requested", xlog.String("workflow", tactivity.GetInfo(ctx).WorkflowType.Name))

	return f.db.Get().Total(), nil
}

// steps bounds the activities a workflow runs.
func steps(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: stepTimeout})
}

func (f *Flows) record(ctx context.Context, name, text string) error {
	f.greeter.Record(ctx, name, text)
	return nil
}
