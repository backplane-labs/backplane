package hook_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/meta"
)

// probe is a transport recording what a call brought.
type probe struct {
	deadline time.Duration // left at the call; 0 without one
	key      string
	meta     map[string]string
}

func (p *probe) Call(ctx context.Context, _ string, _ []byte) ([]byte, error) {
	p.deadline = 0
	if d, ok := ctx.Deadline(); ok {
		p.deadline = time.Until(d)
	}

	p.key = env.CallKeyOf(ctx)
	p.meta = meta.From(ctx)

	return []byte(`{"total":1}`), nil
}

func near(got, want time.Duration) bool { return got > want-time.Second && got <= want }

func TestCallOptions(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	p := &probe{}
	e.SetCaller(p)

	plain := hook.Declare[Quote, Price](root, "Plain")
	bounded := hook.Declare[Quote, Price](root, "Bounded", hook.DefaultTimeout(5*time.Second))

	// No deadline anywhere: the transport applies the platform default.
	if _, err := plain.Call(t.Context(), Quote{}); err != nil || p.deadline != 0 || p.key != "" {
		t.Fatalf("plain: %v %v %q", err, p.deadline, p.key)
	}

	// Declared default.
	if _, err := bounded.Call(t.Context(), Quote{}); err != nil || !near(p.deadline, 5*time.Second) {
		t.Fatalf("declared: %v %v", err, p.deadline)
	}

	// Per call, over the declared default.
	if _, err := bounded.Call(t.Context(), Quote{}, hook.Timeout(2*time.Second), hook.Key("k1")); err != nil ||
		!near(p.deadline, 2*time.Second) || p.key != "k1" {
		t.Fatalf("per call: %v %v %q", err, p.deadline, p.key)
	}

	// An earlier deadline of ctx wins.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	if _, err := bounded.Call(ctx, Quote{}, hook.Timeout(time.Minute)); err != nil || !near(p.deadline, time.Second) {
		t.Fatalf("ctx earlier: %v %v", err, p.deadline)
	}

	// Metadata: the context's, with the call's over it.
	mctx := meta.With(t.Context(), "user", "u-1", "tenant", "t-1")
	if _, err := plain.Call(mctx, Quote{}, hook.Metadata("tenant", "t-2")); err != nil ||
		p.meta["user"] != "u-1" || p.meta["tenant"] != "t-2" {
		t.Fatalf("metadata: %v %v", err, p.meta)
	}

	if got := plain.CallMetadata(hook.Metadata("a", "1"), hook.Metadata("b", "2")); len(got) != 2 || got["b"] != "2" {
		t.Fatalf("call metadata: %v", got)
	}

	// Ignored non-positive values.
	if _, err := bounded.Call(t.Context(), Quote{}, hook.Timeout(-1), hook.Key("")); err != nil ||
		!near(p.deadline, 5*time.Second) || p.key != "" {
		t.Fatalf("ignored: %v %v %q", err, p.deadline, p.key)
	}
}

// slow never answers before its ctx ends.
type slow struct{}

func (slow) Call(ctx context.Context, _ string, _ []byte) ([]byte, error) {
	<-ctx.Done()

	return nil, ctx.Err()
}

func TestCallTimeoutEnds(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	e.SetCaller(slow{})

	ref := hook.Declare[Quote, Price](root, "Slow")

	start := time.Now()

	if _, err := ref.Call(t.Context(), Quote{}, hook.Timeout(100*time.Millisecond)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}

	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v", took)
	}
}

func TestDeclareOptionsRecorded(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	hook.Declare[Quote, Price](root, "Price", hook.DefaultTimeout(5*time.Second), hook.Describe("price of a quote"))
	hook.Declare[Quote, Price](root, "Plain", hook.DefaultTimeout(0))

	m, err := e.Manifest.Build()
	if err != nil {
		t.Fatal(err)
	}

	price, plain := m.GetHooks()[0], m.GetHooks()[1]
	if price.GetTimeout().AsDuration() != 5*time.Second || price.GetDescription() != "price of a quote" {
		t.Fatalf("price: %v", price)
	}

	if plain.GetTimeout() != nil || plain.GetDescription() != "" {
		t.Fatalf("plain: %v", plain)
	}
}

func TestDeclareBadNamePanics(t *testing.T) {
	t.Parallel()

	root, _ := bare(t)

	defer func() {
		if r, _ := recover().(string); !strings.Contains(r, `hook name "send_email" is not CamelCase`) {
			t.Fatalf("panic %q", r)
		}
	}()

	hook.Declare[Quote, Price](root, "send_email")
}
