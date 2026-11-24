package backplanetest_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// recordingTB keeps the cleanups and errors of a harness for the test to
// run and inspect.
type recordingTB struct {
	*testing.T

	mu       sync.Mutex
	errs     []string
	fatal    string
	cleanups []func()
}

// Fatalf records the failure and ends the calling goroutine, as FailNow.
func (r *recordingTB) Fatalf(format string, args ...any) {
	r.mu.Lock()
	r.fatal = fmt.Sprintf(format, args...)
	r.mu.Unlock()

	runtime.Goexit()
}

func (r *recordingTB) Cleanup(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cleanups = append(r.cleanups, fn)
}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recordingTB) cleanup() []string {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.errs
}

func TestNameAndStopBudget(t *testing.T) {
	t.Parallel()

	rec := &recordingTB{T: t}
	h := backplanetest.New(rec, backplanetest.Name("greeter"), backplanetest.StopBudget(20*time.Millisecond))

	if h.Service() != "greeter" {
		t.Fatal(h.Service())
	}
	release := make(chan struct{})
	defer close(release)

	// A goroutine that ignores its context outlives the budget.
	c := deps.NewComponent(h.Root(), "stuck")
	c.Go(func(context.Context) error { <-release; return nil })
	h.Start()

	if errs := rec.cleanup(); len(errs) != 1 || !strings.Contains(errs[0], "did not stop in time") {
		t.Fatalf("stop errors: %v", errs)
	}

	for _, bad := range []func(){
		func() { backplanetest.Name("Greeter") },
		func() { backplanetest.StopBudget(0) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()

			bad()
		}()
	}
}

// A start whose required dependency never comes fails within the
// StartBudget, naming that dependency and its last error, instead of
// retrying until go test's timeout.
func TestStartBudget(t *testing.T) {
	t.Parallel()

	rec := &recordingTB{T: t}
	h := backplanetest.New(rec, backplanetest.StartBudget(300*time.Millisecond))

	errDown := errors.New("db down")
	deps.NewDependency(h.Root(), deps.Func(func(context.Context) (int, error) { return 0, errDown }), deps.Name("db"))
	deps.NewOptional(h.Root(), deps.Func(func(context.Context) (int, error) { return 0, errDown }), deps.Name("cache"))

	done := make(chan struct{})
	started := time.Now()

	go func() {
		defer close(done)

		h.Start()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not end within its budget")
	}

	rec.cleanup()

	rec.mu.Lock()
	fatal := rec.fatal
	rec.mu.Unlock()

	if !strings.Contains(fatal, "300ms") || !strings.Contains(fatal, "db") || !strings.Contains(fatal, "db down") ||
		strings.Contains(fatal, "cache") || time.Since(started) > 5*time.Second {
		t.Fatalf("start failure %q after %v", fatal, time.Since(started))
	}

	defer func() {
		if recover() == nil {
			t.Error("StartBudget(0) must panic")
		}
	}()

	backplanetest.StartBudget(0)
}

func TestReady(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)

	var healthy atomic.Bool

	errDown := errors.New("db down")

	deps.NewDependency(h.Root(), deps.Func(func(context.Context) (int, error) { return 1, nil },
		deps.WithProbe(func(context.Context, int) error {
			if healthy.Load() {
				return nil
			}

			return errDown
		})), deps.Name("db"))
	// An optional dependency never counts, provided or not.
	deps.NewOptional(h.Root(), deps.Func(func(context.Context) (int, error) { return 0, errDown }), deps.Name("cache"))

	if err := backplanetest.Ready(h); !errors.Is(err, backplanetest.ErrNotReady) || !errors.Is(err, deps.ErrNotReady) {
		t.Fatalf("before start: %v", err)
	}

	h.Start()

	err := backplanetest.Ready(h)
	if !errors.Is(err, backplanetest.ErrNotReady) || !errors.Is(err, errDown) || !strings.Contains(err.Error(), "db") {
		t.Fatalf("failing probe: %v", err)
	}

	healthy.Store(true)

	if err := backplanetest.Ready(h); err != nil {
		t.Fatalf("healthy: %v", err)
	}
}

// A dependency that is down fails Ready only; one whose probe reports
// deps.ErrTerminal fails Live too, for good.
func TestLive(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)

	var state atomic.Int32 // 0 healthy, 1 down, 2 terminal

	errDown := errors.New("bus down")

	deps.NewDependency(h.Root(), deps.Func(func(context.Context) (int, error) { return 1, nil },
		deps.WithProbe(func(context.Context, int) error {
			switch state.Load() {
			case 1:
				return errDown
			case 2:
				return fmt.Errorf("bus closed: %w", deps.ErrTerminal)
			}

			return nil
		})), deps.Name("bus"))
	h.Start()

	if err := backplanetest.Live(h); err != nil {
		t.Fatalf("healthy: %v", err)
	}

	state.Store(1)

	if err := backplanetest.Live(h); err != nil {
		t.Fatalf("down but recoverable: %v", err)
	}

	if err := backplanetest.Ready(h); !errors.Is(err, errDown) {
		t.Fatalf("down: ready %v", err)
	}

	state.Store(2)

	err := backplanetest.Live(h)
	if !errors.Is(err, backplanetest.ErrNotLive) || !strings.Contains(err.Error(), "bus closed") {
		t.Fatalf("terminal: %v", err)
	}

	state.Store(0)

	if err := backplanetest.Live(h); !errors.Is(err, backplanetest.ErrNotLive) {
		t.Fatalf("terminal is for good: %v", err)
	}
}

type (
	sectionConfig struct {
		Greeting config.Live[string] `json:"greeting" schemapb:"default=Hello"`
		Limit    int                 `json:"limit"    schemapb:"default=3"`
	}
	serviceConfig struct {
		Section sectionConfig `json:"section"`
		Name    string        `json:"name"    schemapb:"default=svc"`
	}
)

func (s sectionConfig) Validate() error {
	if s.Limit > 10 {
		return errors.New("limit over 10")
	}

	return nil
}

func TestConfig(t *testing.T) {
	t.Parallel()

	sec := backplanetest.Config[sectionConfig](t, nil)
	if sec.Greeting.Get() != "Hello" || sec.Limit != 3 {
		t.Fatalf("defaults: %q %d", sec.Greeting.Get(), sec.Limit)
	}

	svc := backplanetest.Config[serviceConfig](t, map[string]string{"SECTION_GREETING": "Hi", "NAME": "x"})
	if svc.Section.Greeting.Get() != "Hi" || svc.Name != "x" || svc.Section.Limit != 3 {
		t.Fatalf("vars: %q %q", svc.Section.Greeting.Get(), svc.Name)
	}

	if _, err := backplanetest.LoadConfig[serviceConfig](t.Context(), map[string]string{"SECTION_LIMIT": "11"}); err == nil ||
		!strings.Contains(err.Error(), "section: limit over 10") {
		t.Fatalf("validate: %v", err)
	}

	if _, err := backplanetest.LoadConfig[serviceConfig](t.Context(), map[string]string{"NAEM": "typo"}); err == nil {
		t.Fatal("an unknown name must fail")
	}
}

// The process environment is not read.
func TestConfigIgnoresEnvironment(t *testing.T) {
	t.Setenv("TEST_NAME", "from-os")
	t.Setenv("BACKPLANETEST_NAME", "from-os")

	if svc := backplanetest.Config[serviceConfig](t, nil); svc.Name != "svc" {
		t.Fatalf("read the process environment: %q", svc.Name)
	}
}
