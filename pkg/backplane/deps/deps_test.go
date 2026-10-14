package deps_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

const eventually = 2 * time.Second

var errBoom = errors.New("boom")

type Pool struct{ id int }

type Box[T any] []T

// service is one tree: svc is what Run starts and stops, root is the
// author's scope, ready is the readiness of the author's tree.
type service struct {
	svc   *node.Node
	root  deps.Component
	ready probe.Probe
}

func newService(t *testing.T) *service {
	t.Helper()

	svc := node.New("svc", testlog.Discard(), nil)
	app := svc.Child("svc", node.Root, false)

	s := &service{svc: svc, root: link.Scope(app).(deps.Component), ready: app.Readiness()}

	t.Cleanup(func() { _ = s.stop() })

	return s
}

func (s *service) start(t *testing.T) {
	t.Helper()

	if err := s.svc.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
}

func (s *service) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), eventually)
	defer cancel()

	return s.svc.Stop(ctx)
}

func (s *service) isReady(ctx context.Context) bool {
	return s.ready.Check(ctx) == probe.StatusUp
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(eventually)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(time.Millisecond)
	}
}

func expectPanic(t *testing.T, contains string, fn func()) {
	t.Helper()

	defer func() {
		t.Helper()

		r := recover()
		if r == nil {
			t.Fatalf("expected a panic containing %q", contains)
		}

		if msg, _ := r.(string); !strings.Contains(msg, contains) {
			t.Fatalf("panic %v, want it to contain %q", r, contains)
		}
	}()

	fn()
}

type journal struct {
	mu    sync.Mutex
	steps []string
}

func (j *journal) add(step string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.steps = append(j.steps, step)
}

func (j *journal) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()

	return strings.Join(j.steps, " ")
}

// closer counts what a provider released.
type closer struct {
	mu     sync.Mutex
	closed []*Pool
}

func (c *closer) close(_ context.Context, p *Pool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = append(c.closed, p)

	return nil
}

func (c *closer) list() []*Pool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]*Pool(nil), c.closed...)
}

func TestRequiredBlocksStartUntilProvided(t *testing.T) {
	t.Parallel()

	s := newService(t)
	rel := &closer{}

	var (
		attempts   atomic.Int32
		readyEarly atomic.Bool
		pool       = &Pool{id: 1}
	)

	dep := deps.NewDependency(s.root, deps.Func(func(ctx context.Context) (*Pool, error) {
		if s.isReady(ctx) {
			readyEarly.Store(true)
		}

		if attempts.Add(1) < 3 {
			return nil, errBoom
		}

		return pool, nil
	}, deps.WithClose(rel.close)), deps.Backoff(time.Millisecond, time.Millisecond))

	if dep.Ready() || !errors.Is(dep.Err(), deps.ErrNotReady) || s.isReady(t.Context()) {
		t.Fatalf("before start: ready %v err %v", dep.Ready(), dep.Err())
	}

	s.start(t)

	if attempts.Load() != 3 || readyEarly.Load() {
		t.Fatalf("attempts %d, ready while providing %v", attempts.Load(), readyEarly.Load())
	}

	if dep.Get() != pool || !dep.Ready() || dep.Err() != nil || !s.isReady(t.Context()) {
		t.Fatalf("after start: %v %v %v", dep.Get(), dep.Ready(), dep.Err())
	}

	if err := s.stop(); err != nil {
		t.Fatal(err)
	}

	if got := rel.list(); len(got) != 1 || got[0] != pool {
		t.Fatalf("closed %v", got)
	}

	if dep.Get() != nil || dep.Ready() || !errors.Is(dep.Err(), deps.ErrClosed) {
		t.Fatalf("after stop: %v %v %v", dep.Get(), dep.Ready(), dep.Err())
	}
}

func TestRequiredProbeDrivesReadiness(t *testing.T) {
	t.Parallel()

	s := newService(t)

	var healthy atomic.Bool

	healthy.Store(true)

	deps.NewDependency(s.root, deps.Func(func(context.Context) (*Pool, error) { return &Pool{}, nil },
		deps.WithProbe(func(context.Context, *Pool) error {
			if healthy.Load() {
				return nil
			}

			return errBoom
		})))

	s.start(t)

	if !s.isReady(t.Context()) {
		t.Fatal("healthy dependency must be ready")
	}

	healthy.Store(false)

	if s.isReady(t.Context()) {
		t.Fatal("failing probe must take readiness down")
	}
}

// The start fails when ctx ends before a required dependency is provided;
// the error names the dependency.
func TestRequiredStartGivesUpWithContext(t *testing.T) {
	t.Parallel()

	s := newService(t)
	deps.NewDependency(s.root, deps.Func(func(context.Context) (*Pool, error) { return nil, errBoom }),
		deps.Backoff(time.Millisecond, time.Millisecond))

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	err := s.svc.Start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "pool") {
		t.Fatalf("start: %v", err)
	}

	if err := s.stop(); err != nil {
		t.Fatalf("stop after failed start: %v", err)
	}
}

func TestOptionalDoesNotBlockStart(t *testing.T) {
	t.Parallel()

	s := newService(t)
	rel := &closer{}
	release := make(chan struct{})
	pool := &Pool{id: 2}

	opt := deps.NewOptional(s.root, deps.Func(func(ctx context.Context) (*Pool, error) {
		select {
		case <-release:
			return pool, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, deps.WithClose(rel.close)))

	s.start(t)

	if v, ok := opt.Get(); ok || v != nil || !errors.Is(opt.Err(), deps.ErrNotReady) {
		t.Fatalf("before provided: %v %v %v", v, ok, opt.Err())
	}

	if !s.isReady(t.Context()) {
		t.Fatal("an absent optional dependency must not take readiness down")
	}

	close(release)
	waitFor(t, "optional provided", func() bool { _, ok := opt.Get(); return ok })

	if v, _ := opt.Get(); v != pool || opt.Err() != nil {
		t.Fatalf("provided: %v %v", v, opt.Err())
	}

	if err := s.stop(); err != nil {
		t.Fatal(err)
	}

	if got := rel.list(); len(got) != 1 || got[0] != pool {
		t.Fatalf("closed %v", got)
	}

	if _, ok := opt.Get(); ok {
		t.Fatal("optional present after stop")
	}
}

// A provider that returns its value after the stop began: the value is
// released at once instead of leaking.
func TestOptionalProvidedAfterStopIsClosed(t *testing.T) {
	t.Parallel()

	s := newService(t)
	rel := &closer{}
	pool := &Pool{id: 3}
	providing := make(chan struct{})

	opt := deps.NewOptional(s.root, deps.Func(func(ctx context.Context) (*Pool, error) {
		close(providing)
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond) // finishing the dial the stop interrupted

		return pool, nil
	}, deps.WithClose(rel.close)))

	s.start(t)
	<-providing

	if err := s.stop(); err != nil {
		t.Fatal(err)
	}

	if got := rel.list(); len(got) != 1 || got[0] != pool {
		t.Fatalf("closed %v", got)
	}

	if _, ok := opt.Get(); ok || !errors.Is(opt.Err(), deps.ErrClosed) {
		t.Fatalf("after stop: %v", opt.Err())
	}
}

func TestCloseErrorReported(t *testing.T) {
	t.Parallel()

	s := newService(t)
	deps.NewDependency(s.root, deps.Func(func(context.Context) (*Pool, error) { return &Pool{}, nil },
		deps.WithClose(func(context.Context, *Pool) error { return errBoom })))

	s.start(t)

	if err := s.stop(); !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "close pool") {
		t.Fatalf("stop: %v", err)
	}
}

func TestSingleton(t *testing.T) {
	t.Parallel()

	s := newService(t)
	rel := &closer{}

	var builds atomic.Int32

	single := deps.NewSingleton(s.root, deps.Func(func(context.Context) (*Pool, error) {
		if builds.Add(1) == 1 {
			return nil, errBoom
		}

		return &Pool{id: 4}, nil
	}, deps.WithClose(rel.close)))

	s.start(t)

	if _, err := single.Get(t.Context()); !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "provide pool") {
		t.Fatalf("first build: %v", err)
	}

	first, err := single.Get(t.Context())
	if err != nil || first.id != 4 {
		t.Fatalf("second build: %v %v", first, err)
	}

	if again, err := single.Get(t.Context()); err != nil || again != first || builds.Load() != 2 {
		t.Fatalf("not built once: %v %v builds %d", again, err, builds.Load())
	}

	if err := s.stop(); err != nil {
		t.Fatal(err)
	}

	if got := rel.list(); len(got) != 1 || got[0] != first {
		t.Fatalf("closed %v", got)
	}

	if v, err := single.Get(t.Context()); !errors.Is(err, deps.ErrClosed) || v != nil || builds.Load() != 2 {
		t.Fatalf("after stop: %v %v builds %d", v, err, builds.Load())
	}
}

func TestSingletonNeverBuiltIsNotClosed(t *testing.T) {
	t.Parallel()

	s := newService(t)
	rel := &closer{}
	single := deps.NewSingleton(s.root, deps.Func(func(context.Context) (*Pool, error) { return &Pool{}, nil },
		deps.WithClose(rel.close)))

	s.start(t)

	if err := s.stop(); err != nil {
		t.Fatal(err)
	}

	if len(rel.list()) != 0 {
		t.Fatal("closed a singleton that was never built")
	}

	if _, err := single.Get(t.Context()); !errors.Is(err, deps.ErrClosed) {
		t.Fatalf("after stop: %v", err)
	}
}

func TestBackoffValidated(t *testing.T) {
	t.Parallel()

	expectPanic(t, "deps: backoff", func() { deps.Backoff(0, time.Second) })
	expectPanic(t, "deps: backoff", func() { deps.Backoff(2*time.Second, time.Second) })

	deps.Backoff(time.Second, time.Second)
}

func TestStatic(t *testing.T) {
	t.Parallel()

	pool := &Pool{id: 5}

	d := deps.Static(pool)
	if d.IsZero() || d.Get() != pool || !d.Ready() || d.Err() != nil {
		t.Fatalf("static: %v %v %v", d.Get(), d.Ready(), d.Err())
	}

	o := deps.StaticOptional(pool)
	if v, ok := o.Get(); o.IsZero() || v != pool || !ok || o.Err() != nil {
		t.Fatalf("static optional: %v %v %v", v, ok, o.Err())
	}
}

func TestZeroValues(t *testing.T) {
	t.Parallel()

	var d deps.Dependency[*Pool]
	if !d.IsZero() || d.Get() != nil || d.Ready() || !errors.Is(d.Err(), deps.ErrNotReady) {
		t.Fatalf("zero dependency: %v %v %v", d.Get(), d.Ready(), d.Err())
	}

	var o deps.Optional[*Pool]
	if v, ok := o.Get(); !o.IsZero() || v != nil || ok || !errors.Is(o.Err(), deps.ErrNotReady) {
		t.Fatalf("zero optional: %v %v %v", v, ok, o.Err())
	}

	var single deps.Singleton[*Pool]
	if _, err := single.Get(t.Context()); !single.IsZero() || !errors.Is(err, deps.ErrNotReady) {
		t.Fatalf("zero singleton: %v", err)
	}
}

func TestZeroComponent(t *testing.T) {
	t.Parallel()

	var c deps.Component

	if !c.IsZero() || c.Name() != "" || c.Path() != "" {
		t.Fatalf("zero component: %v %q %q", c.IsZero(), c.Name(), c.Path())
	}

	c.Log().Info("nowhere")

	_, span := c.Tracer().Start(t.Context(), "noop")
	span.End()

	if _, err := c.Meter().Int64Counter("noop"); err != nil {
		t.Fatal(err)
	}

	if err := c.Span(t.Context(), "noop", func(context.Context) error { return errBoom }); !errors.Is(err, errBoom) {
		t.Fatalf("span: %v", err)
	}

	noop := func(context.Context) error { return nil }

	expectPanic(t, "deps.NewComponent", func() { c.Go(noop) })
	expectPanic(t, "deps.NewComponent", func() { c.OnStart(noop) })
	expectPanic(t, "deps.NewComponent", func() { c.OnStop(noop) })
	expectPanic(t, "parent scope is zero", func() { deps.NewComponent(c, "child") })
	expectPanic(t, "parent scope is zero", func() {
		deps.NewDependency(c, deps.Func(func(context.Context) (*Pool, error) { return &Pool{}, nil }))
	})
}

func TestComponentLifecycle(t *testing.T) {
	t.Parallel()

	s := newService(t)
	users := deps.NewComponent(s.root, "users")

	var steps journal

	users.OnStart(func(context.Context) error { steps.add("start"); return nil })
	users.OnStop(func(context.Context) error { steps.add("stop"); return nil })
	users.Go(func(ctx context.Context) error {
		<-ctx.Done()
		steps.add("done")

		return nil
	})

	if users.IsZero() || users.Name() != "users" || users.Path() != "users" {
		t.Fatalf("component: %q %q", users.Name(), users.Path())
	}

	if tmpl := deps.NewComponent(users, "templates"); tmpl.Path() != "users/templates" {
		t.Fatalf("nested path %q", tmpl.Path())
	}

	s.start(t)

	if err := s.stop(); err != nil {
		t.Fatal(err)
	}

	if got := steps.String(); got != "start stop done" {
		t.Fatalf("lifecycle %q", got)
	}
}

// scoped records the scope a provider is given.
type scoped struct {
	name string
	path chan string
}

func (p scoped) Name() string { return p.name }

func (p scoped) Provide(_ context.Context, s deps.Scope) (*Pool, error) {
	p.path <- s.Path()

	return &Pool{}, nil
}

func (scoped) Probe(context.Context, *Pool) error { return nil }
func (scoped) Close(context.Context, *Pool) error { return nil }

func TestNames(t *testing.T) {
	t.Parallel()

	noop := func(context.Context) (*Pool, error) { return &Pool{}, nil }

	for want, got := range map[string]string{
		"pool": deps.Func(noop).Name(),
		"box":  deps.Func(func(context.Context) (Box[int], error) { return Box[int]{1}, nil }).Name(),
		"int":  deps.Func(func(context.Context) (int, error) { return 0, nil }).Name(),
	} {
		if got != want {
			t.Errorf("name %q, want %q", got, want)
		}
	}

	s := newService(t)
	path := make(chan string, 2)

	deps.NewDependency(s.root, scoped{name: "postgres", path: path})
	deps.NewDependency(s.root, scoped{name: "postgres", path: path}, deps.Name("replica"))

	s.start(t)

	if a, b := <-path, <-path; a != "postgres" || b != "replica" {
		t.Fatalf("dependency paths %q %q", a, b)
	}
}

func TestSingletonNameOption(t *testing.T) {
	t.Parallel()

	s := newService(t)
	users := deps.NewComponent(s.root, "users")
	path := make(chan string, 1)
	single := deps.NewSingleton(users, scoped{name: "cache", path: path}, deps.Name("templates"))

	s.start(t)

	if _, err := single.Get(t.Context()); err != nil {
		t.Fatal(err)
	}

	if p := <-path; p != "users/templates" {
		t.Fatalf("singleton path %q", p)
	}
}

func TestSpanValue(t *testing.T) {
	t.Parallel()

	s := newService(t)

	v, err := deps.SpanValue(t.Context(), s.root, "compute", func(context.Context) (int, error) { return 42, nil })
	if v != 42 || err != nil {
		t.Fatalf("span value: %v %v", v, err)
	}

	v, err = deps.SpanValue(t.Context(), deps.Component{}, "fail", func(context.Context) (int, error) {
		return 7, errBoom
	})
	if v != 7 || !errors.Is(err, errBoom) {
		t.Fatalf("span value error: %v %v", v, err)
	}
}
