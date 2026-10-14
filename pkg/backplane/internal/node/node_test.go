package node_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

type journal struct {
	mu  sync.Mutex
	log []string
}

func (j *journal) add(s string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.log = append(j.log, s)
}

func (j *journal) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()

	return strings.Join(j.log, " ")
}

func track(j *journal, n *node.Node) *node.Node {
	n.OnStart(func(context.Context) error { j.add("+" + n.Name()); return nil })
	n.OnStop(func(context.Context) error { j.add("-" + n.Name()); return nil })

	return n
}

func TestOrder(t *testing.T) {
	t.Parallel()

	j := &journal{}
	svc := node.New("svc", testlog.Discard(), nil)
	app := track(j, svc.Child("app", node.Root, false))
	db := track(j, app.Child("db", node.Dependency, false))
	api := track(j, app.Child("api", node.Component, false))
	track(j, api.Child("cache", node.Singleton, false))
	track(j, svc.Child("public", node.System, false))

	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := "+app +db +api +cache +public -public -cache -api -db -app"
	if j.String() != want {
		t.Fatalf("order\n got %s\nwant %s", j, want)
	}

	if db.Path() != "db" || api.Path() != "api" {
		t.Fatalf("paths %q %q", db.Path(), api.Path())
	}
}

func TestStartFailureUnwindsStarted(t *testing.T) {
	t.Parallel()

	j := &journal{}
	svc := node.New("svc", testlog.Discard(), nil)
	app := svc.Child("app", node.Root, false)
	track(j, app.Child("a", node.Dependency, false))

	b := app.Child("b", node.Dependency, false)
	b.OnStart(func(context.Context) error { return errors.New("boom") })
	b.OnStop(func(context.Context) error { j.add("-b"); return nil })
	track(j, app.Child("c", node.Component, false))

	err := svc.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "b: boom") {
		t.Fatalf("start: %v", err)
	}

	if err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if want := "+a -b -a"; j.String() != want {
		t.Fatalf("got %s want %s", j, want)
	}
}

// A component's goroutine is gone before the dependency created ahead of it
// closes.
func TestGoroutinesStopBeforeEarlierNodes(t *testing.T) {
	t.Parallel()

	svc := node.New("svc", testlog.Discard(), nil)
	app := svc.Child("app", node.Root, false)

	var closed atomic.Bool

	db := app.Child("db", node.Dependency, false)
	db.OnStop(func(context.Context) error { closed.Store(true); return nil })

	var usedAfterClose atomic.Bool

	api := app.Child("api", node.Component, false)
	api.Go(func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond) // still finishing a request

		usedAfterClose.Store(closed.Load())

		return nil
	})

	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if usedAfterClose.Load() {
		t.Fatal("dependency closed while a later node's goroutine ran")
	}
}

func TestChildDuringOwnStart(t *testing.T) {
	t.Parallel()

	j := &journal{}
	svc := node.New("svc", testlog.Discard(), nil)
	app := svc.Child("app", node.Root, false)
	pool := app.Child("pool", node.Dependency, false)
	pool.OnStart(func(context.Context) error {
		track(j, pool.Child("conn", node.Component, false))

		return nil
	})
	track(j, app.Child("api", node.Component, false))

	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if want := "+conn +api -api -conn"; j.String() != want {
		t.Fatalf("got %s want %s", j, want)
	}
}

func TestChildAfterStartPanics(t *testing.T) {
	t.Parallel()

	svc := node.New("svc", testlog.Discard(), nil)
	app := svc.Child("app", node.Root, false)

	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	defer func() { _ = svc.Stop(context.Background()) }()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()

	app.Child("late", node.Component, false)
}

func TestGoroutineFailureAndPanicReported(t *testing.T) {
	t.Parallel()

	for name, fn := range map[string]func(context.Context) error{
		"error": func(context.Context) error { return errors.New("broken") },
		"panic": func(context.Context) error { panic("broken") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc := node.New("svc", testlog.Discard(), nil)
			app := svc.Child("app", node.Root, false)
			app.Child("worker", node.Component, false).Go(fn)

			if err := svc.Start(context.Background()); err != nil {
				t.Fatal(err)
			}

			defer func() { _ = svc.Stop(context.Background()) }()

			select {
			case err := <-svc.Failed():
				if !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), "broken") {
					t.Fatalf("failure: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("no failure reported")
			}
		})
	}
}

func TestGoroutineIgnoringCancelTimesOut(t *testing.T) {
	t.Parallel()

	svc := node.New("svc", testlog.Discard(), nil)

	block := make(chan struct{})
	defer close(block)

	svc.Child("app", node.Root, false).Go(func(context.Context) error { <-block; return nil })

	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := svc.Stop(ctx); !errors.Is(err, node.ErrStopTimeout) {
		t.Fatalf("want ErrStopTimeout, got %v", err)
	}
}

func TestGoAfterStopIsDropped(t *testing.T) {
	t.Parallel()

	svc := node.New("svc", testlog.Discard(), nil)
	app := svc.Child("app", node.Root, false)

	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	var ran atomic.Bool

	app.Go(func(context.Context) error { ran.Store(true); return nil })
	time.Sleep(10 * time.Millisecond)

	if ran.Load() {
		t.Fatal("goroutine ran after stop")
	}
}

func TestReadinessIncludesLateNodes(t *testing.T) {
	t.Parallel()

	svc := node.New("svc", testlog.Discard(), nil)
	app := svc.Child("app", node.Root, false)
	pool := app.Child("pool", node.Dependency, false)
	pool.OnStart(func(context.Context) error {
		pool.Child("conn", node.Component, false).Ready(probe.Func(func(context.Context) probe.Status {
			return probe.StatusDown
		}))

		return nil
	})

	ready := app.Readiness()
	if ready.Check(context.Background()) != probe.StatusUp {
		t.Fatal("empty tree must be ready")
	}

	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	defer func() { _ = svc.Stop(context.Background()) }()

	if ready.Check(context.Background()) != probe.StatusDown {
		t.Fatal("late node's probe must count")
	}
}

func TestPathsUniqueWithinRoot(t *testing.T) {
	t.Parallel()

	svc := node.New("svc", testlog.Discard(), nil)
	svc.Child("config", node.System, false)

	app := svc.Child("app", node.Root, false)
	a := app.Child("config", node.Component, false)
	b := app.Child("config", node.Component, false)

	if a.Path() != "config" || b.Path() != "config-2" {
		t.Fatalf("paths %q %q", a.Path(), b.Path())
	}
}

// Budget narrows one node's stop (hooks and goroutine wait) without
// touching the nodes stopped after it.
func TestBudget(t *testing.T) {
	t.Parallel()

	svc := node.New("svc", testlog.Discard(), nil)
	later := svc.Child("later", node.System, false)
	narrow := svc.Child("narrow", node.System, false)

	var laterLeft, narrowLeft time.Duration

	later.OnStop(func(ctx context.Context) error {
		dl, _ := ctx.Deadline()
		laterLeft = time.Until(dl)

		return nil
	})
	narrow.OnStop(func(ctx context.Context) error {
		dl, _ := ctx.Deadline()
		narrowLeft = time.Until(dl)

		return nil
	})
	narrow.Budget(func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, 50*time.Millisecond)
	})

	// A goroutine that ignores cancellation holds the narrowed node only
	// for its budget.
	release := make(chan struct{})

	narrow.Go(func(context.Context) error { <-release; return nil })

	if err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	err := svc.Stop(ctx)

	close(release)

	if !errors.Is(err, node.ErrStopTimeout) {
		t.Fatalf("want the narrowed node's timeout, got %v", err)
	}

	if narrowLeft > 50*time.Millisecond || laterLeft < 5*time.Second {
		t.Fatalf("budgets: narrow %v, later %v", narrowLeft, laterLeft)
	}
}

func TestProbeInterval(t *testing.T) {
	t.Parallel()

	svc := node.New("svc", testlog.Discard(), nil)
	child := svc.Child("c", node.System, false)

	if child.ProbeInterval() != node.DefaultProbeInterval {
		t.Fatalf("default: %v", child.ProbeInterval())
	}

	svc.SetProbeInterval(time.Second)

	if child.ProbeInterval() != time.Second {
		t.Fatalf("set: %v", child.ProbeInterval())
	}
}
