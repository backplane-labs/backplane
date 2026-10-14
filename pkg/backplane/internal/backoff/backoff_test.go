package backoff_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		p  backoff.Policy
		ok bool
	}{
		"zero min":     {backoff.Policy{Min: 0, Max: time.Second}, false},
		"negative min": {backoff.Policy{Min: -time.Second, Max: time.Second}, false},
		"max below":    {backoff.Policy{Min: 2 * time.Second, Max: time.Second}, false},
		"equal":        {backoff.Policy{Min: time.Second, Max: time.Second}, true},
		"range":        {backoff.Policy{Min: time.Millisecond, Max: time.Second}, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := c.p.Validate(); (err == nil) != c.ok {
				t.Fatalf("validate %v: %v", c.p, err)
			}
		})
	}
}

func TestRetryUntilSuccess(t *testing.T) {
	t.Parallel()

	p := backoff.Policy{Min: time.Millisecond, Max: 2 * time.Millisecond}
	boom := errors.New("boom")

	var (
		calls   int
		retries []error
	)

	err := backoff.Retry(t.Context(), p, func(context.Context) error {
		calls++
		if calls < 3 {
			return boom
		}

		return nil
	}, func(err error, in time.Duration) {
		if in <= 0 {
			t.Errorf("non-positive delay %v", in)
		}

		retries = append(retries, err)
	})
	if err != nil {
		t.Fatal(err)
	}

	if calls != 3 || len(retries) != 2 || !errors.Is(retries[0], boom) {
		t.Fatalf("calls %d retries %v", calls, retries)
	}
}

func TestRetryStopsOnContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	boom := errors.New("boom")
	p := backoff.Policy{Min: time.Hour, Max: time.Hour}

	start := time.Now()
	err := backoff.Retry(ctx, p, func(context.Context) error { return boom }, func(error, time.Duration) { cancel() })

	if !errors.Is(err, context.Canceled) || !errors.Is(err, boom) {
		t.Fatalf("want canceled wrapping boom, got %v", err)
	}

	if time.Since(start) > time.Second {
		t.Fatal("retry did not stop on cancel")
	}
}
