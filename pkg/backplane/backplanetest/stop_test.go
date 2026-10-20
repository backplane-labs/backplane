package backplanetest_test

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// projector counts ticks until its context ends, as a projection
// consuming a stream would; stopped records its OnStop.
type projector struct {
	deps.Component

	ticks   atomic.Int64
	stopped atomic.Bool
	killed  chan struct{}
}

func newProjector(parent deps.Scope, name string) *projector {
	p := &projector{Component: deps.NewComponent(parent, name), killed: make(chan struct{})}
	p.Go(func(ctx context.Context) error {
		for {
			select {
			case <-ctx.Done():
				close(p.killed)

				return nil
			case <-time.After(time.Millisecond):
				p.ticks.Add(1)
			}
		}
	})
	p.OnStop(func(context.Context) error { p.stopped.Store(true); return nil })

	return p
}

func waitTicks(t *testing.T, p *projector, n int64) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for p.ticks.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not tick", p.Name())
		}

		time.Sleep(time.Millisecond)
	}
}

func TestStopKillsOneComponentMidTest(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	orders := newProjector(h.Root(), "orders")
	users := newProjector(h.Root(), "users")
	nested := newProjector(orders, "orders-index")

	h.Start()

	waitTicks(t, orders, 5)
	backplanetest.Stop(h, orders)

	select {
	case <-orders.killed:
	default:
		t.Fatal("the stopped projection's goroutine still runs")
	}

	if !orders.stopped.Load() || !nested.stopped.Load() {
		t.Fatal("OnStop of the subtree did not run")
	}

	if users.stopped.Load() {
		t.Fatal("a sibling was stopped")
	}

	// The rest of the tree keeps working; the dead one stays dead.
	frozen, before := orders.ticks.Load(), users.ticks.Load()
	waitTicks(t, users, before+5)

	if orders.ticks.Load() != frozen {
		t.Fatal("the stopped projection ticked again")
	}
}

func TestHarnessStop(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	p := newProjector(h.Root(), "orders")
	h.Start()
	waitTicks(t, p, 1)

	h.Stop()

	if !p.stopped.Load() {
		t.Fatal("Stop did not stop the tree")
	}

	h.Stop() // and again, as the cleanup does: nothing left
}

func TestStopRefusesAForeignComponent(t *testing.T) {
	t.Parallel()

	other := backplanetest.New(t)
	foreign := newProjector(other.Root(), "foreign")

	fake := &fatalRecorder{TB: t}
	h := backplanetest.New(fake)
	h.Start()

	done := make(chan struct{})

	go func() {
		defer close(done)

		backplanetest.Stop(h, foreign)
	}()

	<-done

	if !fake.fatal {
		t.Fatal("stopping another harness's component did not fail")
	}
}

// fatalRecorder records Fatalf instead of failing (Fatalf must stop its
// goroutine: runtime.Goexit, as testing does).
type fatalRecorder struct {
	testing.TB

	fatal bool
}

func (f *fatalRecorder) Fatalf(string, ...any) { f.fatal = true; runtime.Goexit() }
func (f *fatalRecorder) Helper()               {}

func TestFindByNameOrPath(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	sync := deps.NewComponent(h.Root(), "sync")
	fanout := newProjector(sync, "fanout")
	newProjector(h.Root(), "a")
	newProjector(deps.NewComponent(h.Root(), "b"), "a")

	h.Start()
	waitTicks(t, fanout, 1)

	found := backplanetest.Find(h, "fanout")
	if found.Path() != fanout.Path() {
		t.Fatalf("found %s, want %s", found.Path(), fanout.Path())
	}

	backplanetest.Stop(h, found)

	if !fanout.stopped.Load() {
		t.Fatal("Stop of the found node did not stop the component")
	}

	if got := backplanetest.Find(h, "b/a"); got.Path() != "b/a" {
		t.Fatalf("by path: %s", got.Path())
	}

	for _, name := range []string{"a", "nope"} {
		fake := &fatalRecorder{TB: t}
		other := backplanetest.New(fake)
		newProjector(other.Root(), "a")
		newProjector(deps.NewComponent(other.Root(), "b"), "a")

		done := make(chan struct{})

		go func() {
			defer close(done)

			backplanetest.Find(other, name)
		}()

		<-done

		if !fake.fatal {
			t.Fatalf("Find(%q) did not fail", name)
		}
	}
}
