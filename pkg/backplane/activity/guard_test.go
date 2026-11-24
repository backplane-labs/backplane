package activity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

var errNewInput = errors.New("input is newer than this worker")

func TestInputGuard(t *testing.T) {
	t.Parallel()

	for _, own := range []bool{false, true} {
		name := "declared"
		if own {
			name = "own"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, tc := range []struct {
				name, raw string
				guard     bool
				want      string
				final     bool
				retry     bool
			}{
				{"no opt-in remains lenient", `{"activity":"call","new_field":true}`, false, "call", false, false},
				{"accepted input", `{"activity":"call"}`, true, "call", false, false},
				{"new input reaches guard intact", `{"activity":"call","new_field":true}`, true, "", false, true},
				{"strict after guard", `{"activity":"call","missed_field":true}`, true, "", true, false},
				{"wrong protobuf type is final", `{"activity":17}`, true, "", true, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					h := backplanetest.New(t)
					if tc.guard {
						activity.GuardInputs(h.Root(), func(ctx context.Context, name string, payload []byte) error {
							info, ok := activity.InfoOf(ctx)
							if !ok || info.Attempt != 1 || name != "Probe" || !bytes.Equal(payload, []byte(tc.raw)) {
								t.Errorf("guard got %q, %s and %+v", name, payload, info)
							}

							if bytes.Contains(payload, []byte(`"new_field"`)) {
								return activity.RetryAfter(errNewInput, time.Second)
							}

							return nil
						})
					}

					called := false
					handler := func(_ context.Context, req *backplanev1.ActivityCall) (string, error) {
						called = true

						return req.GetActivity(), nil
					}
					if own {
						workflows.HandleActivity(h.Root(), "Probe", handler)
					} else {
						activity.Handle(h.Root(), "Probe", handler)
					}

					h.Start()

					got, err := backplanetest.Activity[json.RawMessage, string](t.Context(), h, "Probe", json.RawMessage(tc.raw))
					if tc.retry {
						delay, ok := activity.RetryAfterOf(err)
						if called || !errors.Is(err, errNewInput) || !ok || delay != time.Second || activity.IsNonRetryable(err) {
							t.Fatalf("retry: called=%v, delay=%v, error=%v", called, delay, err)
						}

						return
					}

					if tc.final {
						if called || !activity.IsNonRetryable(err) {
							t.Fatalf("strict decode: called=%v, error=%v", called, err)
						}

						return
					}

					if !called || err != nil || got != tc.want {
						t.Fatalf("handled=%v, value=%q, error=%v", called, got, err)
					}
				})
			}
		})
	}
}

func BenchmarkInputGuard(b *testing.B) {
	h := backplanetest.New(b)
	activity.GuardInputs(h.Root(), func(context.Context, string, []byte) error { return nil })
	activity.Handle(h.Root(), "Probe", func(_ context.Context, in *backplanev1.ActivityCall) (string, error) {
		return in.GetActivity(), nil
	})
	h.Start()

	for b.Loop() {
		if _, err := backplanetest.Activity[json.RawMessage, string](b.Context(), h, "Probe", json.RawMessage(`{"activity":"call"}`)); err != nil {
			b.Fatal(err)
		}
	}
}
