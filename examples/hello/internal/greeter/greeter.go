// Package greeter is the service's domain component: it serves HelloService
// over every public protocol with one implementation and raises Greeted
// after every greeting.
package greeter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/examples/hello/internal/store"
	hellov1 "github.com/gopherex/backplane/examples/hello/proto/hello/v1"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/event"
)

const (
	countdownTick = 100 * time.Millisecond
	reportEvery   = time.Minute
	// maxSuffix bounds the Live suffix (Validate).
	maxSuffix = 8
)

// Errors of Validate.
var (
	ErrSalute = errors.New("salute must not be empty")
	ErrSuffix = errors.New("suffix must be at most 8 characters on one line")
	// ErrStalled: the report loop has not ticked for three periods.
	ErrStalled = errors.New("greeter: report loop stalled")
)

// Config of the greeter.
type Config struct {
	Salute string `json:"salute" schemapb:"default=Hello;description=Word before the name"`
	// Live: editable from the console, applied without restart.
	Suffix  config.Live[string] `json:"suffix"  schemapb:"default=!;description=Appended to every greeting"`
	Excited config.Live[bool]   `json:"excited" schemapb:"default=false"`
}

// Validate checks what the schema cannot: Open fails on it, and a live
// update it rejects (a suffix of nine characters) is not applied.
func (c *Config) Validate() error {
	var errs []error

	if strings.TrimSpace(c.Salute) == "" {
		errs = append(errs, ErrSalute)
	}

	if s := c.Suffix.Get(); len([]rune(s)) > maxSuffix || strings.ContainsAny(s, "\r\n") {
		errs = append(errs, fmt.Errorf("%w: %q", ErrSuffix, s))
	}

	return errors.Join(errs...)
}

// Greeted is published after every greeting. Payloads evolve additively:
// Text was added after Name and Count, readers that predate it ignore it.
type Greeted struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
	Text  string `json:"text,omitempty"`
}

// Greeter is a component: a named node with its own logger, tracer and meter.
type Greeter struct {
	hellov1.UnimplementedHelloServiceServer
	deps.Component

	cfg       *Config
	db        deps.Dependency[*store.DB]
	greeted   event.Ref[Greeted]
	templates deps.Singleton[*template.Template]
	greetings metric.Int64Counter
	// boot makes event ids unique per process: a greeting published twice
	// (a retried Publish) is stored once, a restart counts afresh.
	boot string
	// beat is the unix time of the report loop's last tick (0: not running).
	beat atomic.Int64
}

// New creates the greeter under parent; everything it needs is passed in,
// the events it raises it declares itself.
func New(parent deps.Scope, cfg *Config, db deps.Dependency[*store.DB]) (*Greeter, error) {
	g := &Greeter{Component: deps.NewComponent(parent, "greeter"), cfg: cfg, db: db, boot: uuid.NewString()[:8]}
	g.greeted = event.Declare[Greeted](g, "Greeted", event.Describe("a name was greeted"))

	greetings, err := g.Meter().Int64Counter("hello.greetings", metric.WithDescription("Greetings served"))
	if err != nil {
		return nil, fmt.Errorf("greeter metrics: %w", err)
	}

	g.greetings = greetings

	// Built on first use, closed at stop; not part of readiness.
	g.templates = deps.NewSingleton(g, deps.Func(parseTemplates), deps.Name("templates"))

	// Background work under the service lifecycle, started with the tree.
	g.Go(g.report)
	cfg.Suffix.Watch(func(s string) { g.Log().Info("suffix changed", xlog.String("suffix", s)) })

	return g, nil
}

// Greeted is the event the greeter raises.
func (g *Greeter) Greeted() event.Ref[Greeted] { return g.greeted }

// Register is the one registration for native gRPC and ws-proto.
func (g *Greeter) Register(r grpc.ServiceRegistrar) { hellov1.RegisterHelloServiceServer(r, g) }

// Text greets name and publishes Greeted; a publish that fails (no NATS)
// does not fail the greeting.
func (g *Greeter) Text(ctx context.Context, name string) (string, error) {
	text, err := deps.SpanValue(ctx, g, "greet", func(ctx context.Context) (string, error) {
		tmpl, err := g.templates.Get(ctx)
		if err != nil {
			return "", fmt.Errorf("templates: %w", err)
		}

		var b strings.Builder
		if err := tmpl.Execute(&b, g.view(name)); err != nil {
			return "", fmt.Errorf("render: %w", err)
		}

		g.Record(ctx, name, b.String())

		return b.String(), nil
	})
	if err != nil {
		return "", fmt.Errorf("greeter: %w", err)
	}

	return text, nil
}

// Record records the greeting actually delivered, whether rendered locally
// or returned by a bound hook. The same metric and event path serves both.
// Publication failure (for example, running without NATS) is logged without
// failing the greeting, just like Text.
func (g *Greeter) Record(ctx context.Context, name, text string) {
	count := g.db.Get().Inc(ctx, name)
	g.greetings.Add(ctx, 1)
	id := g.boot + "-" + name + "-" + strconv.FormatUint(count, 10)

	// Application audit: the Collector forwards it to backplane (§14).
	g.Log().Ctx().Info(ctx, "greeting delivered", backplane.Audit(),
		xlog.String("event.name", "greeting.delivered"), xlog.String(backplane.AuditID, id),
		xlog.String(backplane.AuditSubject, "name/"+name), xlog.String(backplane.AuditOutcome, "succeeded"),
		xlog.Uint64("count", count))

	err := g.greeted.Publish(ctx, Greeted{Name: name, Count: count, Text: text},
		event.Key(name), event.ID(id),
		event.Header("excited", strconv.FormatBool(g.cfg.Excited.Get())))
	if err != nil {
		g.Log().Ctx().Debug(ctx, "greeted not published", xlog.Err(err))
	}
}

// Greet implements HelloService.
func (g *Greeter) Greet(ctx context.Context, req *hellov1.GreetRequest) (*hellov1.GreetResponse, error) {
	text, err := g.Text(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	return &hellov1.GreetResponse{Greeting: text}, nil
}

// Countdown implements HelloService.
func (g *Greeter) Countdown(
	req *hellov1.CountdownRequest, stream grpc.ServerStreamingServer[hellov1.CountdownResponse],
) error {
	for left := req.GetFrom(); ; left-- {
		if err := stream.Send(&hellov1.CountdownResponse{Left: left}); err != nil || left == 0 {
			return err //nolint:wrapcheck // gRPC status passes through
		}

		select {
		case <-stream.Context().Done():
			return stream.Context().Err() //nolint:wrapcheck // cancellation passes through
		case <-time.After(countdownTick):
		}
	}
}

// Ready is the greeter's readiness check: its templates build.
func (g *Greeter) Ready(ctx context.Context) error {
	if _, err := g.templates.Get(ctx); err != nil {
		return fmt.Errorf("greeter templates: %w", err)
	}

	return nil
}

// Alive is the greeter's liveness check: the report loop, once started,
// keeps ticking.
func (g *Greeter) Alive(context.Context) error {
	last := g.beat.Load()
	if last != 0 && time.Since(time.Unix(last, 0)) > 3*reportEvery {
		return ErrStalled
	}

	return nil
}

type view struct {
	Salute, Name, Suffix string
	Excited              bool
}

func (g *Greeter) view(name string) view {
	return view{Salute: g.cfg.Salute, Name: name, Suffix: g.cfg.Suffix.Get(), Excited: g.cfg.Excited.Get()}
}

func (g *Greeter) report(ctx context.Context) error {
	t := time.NewTicker(reportEvery)
	defer t.Stop()

	defer g.beat.Store(0)

	for {
		g.beat.Store(time.Now().Unix())

		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}

		// A span of the component's own tracer around one report.
		_ = g.Span(ctx, "report", func(ctx context.Context) error {
			g.Log().Ctx().Info(ctx, "greetings so far", xlog.Uint64("total", g.db.Get().Total()))

			return nil
		})
	}
}

func parseTemplates(context.Context) (*template.Template, error) {
	tmpl, err := template.New("greeting").Parse(`{{.Salute}}, {{.Name}}{{.Suffix}}{{if .Excited}}!!{{end}}`)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return tmpl, nil
}
