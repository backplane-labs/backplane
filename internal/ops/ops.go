// Package ops is the console's operations on the stack behind the
// services (§8, §9, §11): events and their streams, consumers and dead
// letters in NATS JetStream; workflows, runs and schedules in Temporal;
// calling a hook or an activity by hand. It serves
// backplane.console.v1.EventService, WorkflowService, ScheduleService and
// CallService on the console's /ws.
//
// backplane is a service on its own SDK, and ops uses the SDK's clients:
// event.JetStream for NATS (BACKPLANE_NATS_URL), workflows.Client for
// Temporal (BACKPLANE_TEMPORAL_ADDR). Without one the calls it backs fail
// with UNAVAILABLE. Hook and activity calls run as short workflows on
// backplane's own task queue (RegisterWorkflows, through
// workflows.Register): the run is in Temporal like any other, with memo
// source = console:<session>.
//
// What the console declares — events, subscriptions, workflows,
// schedules, hooks, activities — comes from the latest manifest of each
// service (registry). Mutating calls are logged with their author (the
// console session): "console action".
package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/nats-io/nats.go/jetstream"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/redact"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	natsdriver "github.com/backplane-labs/backplane/pkg/backplane/drivers/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

// Errors.
var (
	// ErrNotSynced: the registry has no snapshot yet.
	ErrNotSynced = errors.New("ops: registry not synced yet")
	// ErrNoService: no service of that name.
	ErrNoService = errors.New("ops: no such service")
	// ErrNotDeclared: the latest manifest does not declare it.
	ErrNotDeclared = errors.New("ops: not declared")
	// ErrInput: the request is malformed.
	ErrInput = errors.New("ops: invalid request")
	// ErrPrecondition: the stack is not in a state the call needs.
	ErrPrecondition = errors.New("ops: failed precondition")
	// ErrExists: what the call would create exists (a running workflow id).
	ErrExists = errors.New("ops: already exists")
	// ErrUnavailable: NATS or Temporal is not configured or connected.
	ErrUnavailable = errors.New("ops: unavailable")
)

// SessionMetadata is the gRPC metadata the console puts the operator's
// session in (§11.1).
const SessionMetadata = "bp-console-session"

// Page sizes.
const (
	defaultPage = 50
	maxPage     = 500
)

// JetStreamFunc gives the NATS client; TemporalFunc the Temporal one.
type (
	JetStreamFunc func() (jetstream.JetStream, error)
	TemporalFunc  func() (client.Client, error)
)

// Option configures New.
type Option func(*Ops)

// Author names who made a call from its context; default: the console
// session in SessionMetadata as console:<id>, else "admin".
func Author(fn func(ctx context.Context) string) Option { return func(o *Ops) { o.author = fn } }

// WithJetStream replaces the SDK's NATS client (tests).
func WithJetStream(fn JetStreamFunc) Option { return func(o *Ops) { o.jet = fn } }

// WithTemporal replaces the SDK's Temporal client and backplane's task
// queue the call workflows run on (tests).
func WithTemporal(fn TemporalFunc, queue string) Option {
	return func(o *Ops) { o.temporal, o.queue = fn, queue }
}

// Namespace is the Temporal namespace of the client (default "default"):
// the calls that go to Temporal's API directly name it.
func Namespace(ns string) Option { return func(o *Ops) { o.namespace = ns } }

// defaultNamespace of Temporal.
const defaultNamespace = "default"

// Ops is the component. Every call reads the registry snapshot and asks
// NATS or Temporal; the one thing it remembers is whether Temporal's
// visibility refused ORDER BY.
type Ops struct {
	deps.Component

	src       registry.Source
	jet       JetStreamFunc
	temporal  TemporalFunc
	queue     string
	namespace string
	author    func(ctx context.Context) string
	// unordered: visibility refused ORDER BY once (SQL visibility); runs
	// are listed in its own order from then on.
	unordered atomic.Bool
}

// New creates the component under parent, reading declarations from src.
func New(parent deps.Scope, src registry.Source, opts ...Option) *Ops {
	o := &Ops{Component: deps.NewComponent(parent, "ops"), src: src, author: sessionAuthor, namespace: defaultNamespace}
	o.jet = func() (jetstream.JetStream, error) { return natsdriver.JetStream(o) }
	o.temporal = func() (client.Client, error) { return workflows.Client(o) }
	o.queue = workflows.Queue(parent)

	for _, opt := range opts {
		opt(o)
	}

	return o
}

// Detached is Ops without a node of the tree — it logs nowhere — for
// tests and tools: give it WithJetStream and WithTemporal, else the calls
// they back fail with UNAVAILABLE.
func Detached(src registry.Source, opts ...Option) *Ops {
	o := &Ops{src: src, author: sessionAuthor, namespace: defaultNamespace}
	o.jet = func() (jetstream.JetStream, error) { return nil, errDetached }
	o.temporal = func() (client.Client, error) { return nil, errDetached }

	for _, opt := range opts {
		opt(o)
	}

	return o
}

var errDetached = errors.New("not configured")

// Register registers the console services on r.
func (o *Ops) Register(r grpc.ServiceRegistrar) {
	consolev1.RegisterEventServiceServer(r, o.Events())
	consolev1.RegisterWorkflowServiceServer(r, o.Workflows())
	consolev1.RegisterScheduleServiceServer(r, o.Schedules())
	consolev1.RegisterCallServiceServer(r, o.Calls())
}

// Events is o as EventService.
func (o *Ops) Events() EventAPI { return EventAPI{o: o} }

// Workflows is o as WorkflowService.
func (o *Ops) Workflows() WorkflowAPI { return WorkflowAPI{o: o} }

// Schedules is o as ScheduleService.
func (o *Ops) Schedules() ScheduleAPI { return ScheduleAPI{o: o} }

// Calls is o as CallService.
func (o *Ops) Calls() CallAPI { return CallAPI{o: o} }

// RegisterWorkflows adds the call workflows to backplane's worker (give it
// to workflows.Register).
func RegisterWorkflows(r worker.Registry) { registerWorkflows(r) }

// sessionAuthor: the console session of the request, else "admin".
func sessionAuthor(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(SessionMetadata); len(v) > 0 && v[0] != "" {
			return "console:" + v[0]
		}
	}

	return "admin"
}

// audit logs a mutating call: who, what, on what.
func (o *Ops) audit(ctx context.Context, action, subject string, fields ...xlog.Field) {
	fs := append([]xlog.Field{
		xlog.String("actor", o.author(ctx)), xlog.String("action", action), xlog.String("subject", subject),
	}, fields...)
	o.Log().Ctx().Info(ctx, "console action", fs...)
}

// nats is the JetStream client or ErrUnavailable.
func (o *Ops) nats() (jetstream.JetStream, error) {
	j, err := o.jet()
	if err != nil {
		return nil, fmt.Errorf("%w: nats: %w", ErrUnavailable, err)
	}

	return j, nil
}

// client is the Temporal client or ErrUnavailable.
func (o *Ops) client() (client.Client, error) {
	c, err := o.temporal()
	if err != nil {
		return nil, fmt.Errorf("%w: temporal: %w", ErrUnavailable, err)
	}

	return c, nil
}

// catalog is the current snapshot, or ErrNotSynced.
func (o *Ops) catalog() (registry.Catalog, error) {
	cat := o.src.Current()
	if cat.Index == 0 {
		return cat, ErrNotSynced
	}

	return cat, nil
}

// manifests are the latest manifests of the services, sorted by name;
// only service's when it is set (ErrNoService when unknown).
func (o *Ops) manifests(service string) ([]*backplanev1.Manifest, error) {
	cat, err := o.catalog()
	if err != nil {
		return nil, err
	}

	if service != "" {
		svc, ok := cat.Services[service]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrNoService, service)
		}

		if m := svc.Latest(); m != nil {
			return []*backplanev1.Manifest{m}, nil
		}

		return nil, nil
	}

	names := make([]string, 0, len(cat.Services))
	for name := range cat.Services {
		names = append(names, name)
	}

	slices.Sort(names)

	out := make([]*backplanev1.Manifest, 0, len(names))

	for _, name := range names {
		if m := cat.Services[name].Latest(); m != nil {
			out = append(out, m)
		}
	}

	return out, nil
}

// latest is service's latest manifest.
// redactor resolves declarations for masking payloads: the latest
// manifests; a service without one masks nothing.
func (o *Ops) redactor() redact.Manifests {
	return redact.Manifests(func(service string) *backplanev1.Manifest {
		m, _ := o.latest(service)

		return m
	})
}

func (o *Ops) latest(service string) (*backplanev1.Manifest, error) {
	manifests, err := o.manifests(service)
	if err != nil {
		return nil, err
	}

	if len(manifests) == 0 {
		return nil, fmt.Errorf("%w: service %q has no manifest", ErrNotDeclared, service)
	}

	return manifests[0], nil
}

var (
	serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	camelName   = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

// splitFull splits "<service>.<Name>" and checks both parts.
func splitFull(kind, full string) (string, string, error) {
	service, name, ok := strings.Cut(full, ".")
	if !ok || !serviceName.MatchString(service) || !camelName.MatchString(name) {
		return "", "", fmt.Errorf("%w: %s must be <service>.<Name>, got %q", ErrInput, kind, full)
	}

	return service, name, nil
}

// pageSize bounds a requested page size.
func pageSize(n uint32) int {
	if n == 0 {
		return defaultPage
	}

	return min(int(n), maxPage)
}

// status maps errors to gRPC codes; unexpected ones are logged.
func (o *Ops) status(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok && !errors.Is(err, ErrInput) {
		return err
	}

	code := codes.Internal

	switch {
	case errors.Is(err, ErrInput):
		code = codes.InvalidArgument
	case errors.Is(err, ErrNotSynced), errors.Is(err, ErrUnavailable), errors.Is(err, ErrNotSettled),
		errors.Is(err, jetstream.ErrNoStreamResponse):
		code = codes.Unavailable
	case errors.Is(err, ErrNoService), errors.Is(err, ErrNotDeclared),
		errors.Is(err, jetstream.ErrStreamNotFound), errors.Is(err, jetstream.ErrConsumerNotFound),
		errors.Is(err, jetstream.ErrMsgNotFound):
		code = codes.NotFound
	case errors.Is(err, ErrPrecondition):
		code = codes.FailedPrecondition
	case errors.Is(err, ErrExists):
		code = codes.AlreadyExists
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	default:
		if st := serviceerror.ToStatus(err); st != nil && st.Code() != codes.Unknown && st.Code() != codes.OK {
			return st.Err() //nolint:wrapcheck // Temporal's status travels as is
		}

		o.Log().Ctx().Error(ctx, "console ops", xlog.Err(err))
	}

	return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
}
