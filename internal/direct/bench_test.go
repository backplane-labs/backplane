package direct_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/redact"
)

// echo is a Caller answering every activity at once with a fixed output:
// what is left is the runner's own cost.
type echo struct{ out []byte }

func (e echo) Call(context.Context, *backplanev1.ActivityCall, time.Duration) (*backplanev1.ActivityResult, error) {
	return &backplanev1.ActivityResult{Payload: e.out}, nil
}

func BenchmarkRunnerExecute(b *testing.B) {
	var fan strings.Builder

	fan.WriteString("hook: iam.SendEmail\nsteps:\n")

	for i := range 10 {
		fmt.Fprintf(&fan, "  p%d: {activity: billing.Charge, input: {v: req.to}}\n", i)
	}

	fan.WriteString("result: {id: p0.id}\n")

	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}

	forEachInput, err := json.Marshal(map[string]any{"to": "a@b.c", "items": items})
	if err != nil {
		b.Fatal(err)
	}

	for _, bc := range []struct {
		name, text string
		input      []byte
	}{
		{"sequential-2", `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.template}}
  send: {activity: smtp.Send, input: {to: req.to, text: render.id}}
result: {id: send.id}
`, []byte(`{"to":"a@b.c","template":"welcome"}`)},
		{"fan-out-10", fan.String(), []byte(`{"to":"a@b.c"}`)},
		{"for-each-100", `
hook: iam.SendEmail
steps:
  charge:
    forEach: req.items
    as: it
    concurrency: 10
    activity: billing.Charge
    input: {v: it}
result: {id: "string(charge.size())"}
`, forEachInput},
	} {
		p := program(b, bc.text)
		r := direct.NewRunner(echo{out: []byte(`{"id":"x"}`)})
		run := direct.Run{ID: "bench", Kind: direct.KindBinding, Program: p, Payload: bc.input, Identity: "iam.SendEmail@1"}

		b.Run(bc.name+"/serial", func(b *testing.B) {
			for b.Loop() {
				if _, err := r.Execute(b.Context(), run, nil); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(bc.name+"/parallel", func(b *testing.B) {
			ctx := b.Context()

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := r.Execute(ctx, run, nil); err != nil {
						b.Error(err)

						return
					}
				}
			})
		})
	}
}

// BenchmarkRecordedCalls is a burst of hook calls with their runs recorded
// in PostgreSQL (BACKPLANE_TEST_PG): how long a call takes, how fast the
// single writer stores the records behind it, and how many it drops when
// its queue fills. Run with -benchtime=1x: one iteration is one burst.
func BenchmarkRecordedCalls(b *testing.B) {
	fakeB := &fakeBindings{
		cat: catalog(),
		active: map[string]bindings.Binding{
			"iam.SendEmail": binding(b, `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.template}}
  send: {activity: smtp.Send, input: {to: req.to, text: render.text}}
result: {id: send.id}
`),
		},
	}

	s, _ := live(b, map[string]handler{
		"template.Exec": func(context.Context, map[string]any) (any, error) { return map[string]any{"text": "hi"}, nil },
		"smtp.Send":     func(context.Context, map[string]any) (any, error) { return map[string]any{"id": "m-1"}, nil },
	}, fakeB)

	call := &backplanev1.HookCall{Hook: "iam.SendEmail", Instance: "iam-1", Payload: []byte(`{"to":"a@b.c","template":"t"}`)}

	for _, burst := range []int{500, 2000, 10000} {
		b.Run(fmt.Sprintf("burst-%d", burst), func(b *testing.B) {
			for b.Loop() {
				ids := make([]string, 0, burst)
				start := time.Now()

				for range burst {
					_, id, err := s.RaiseHook(b.Context(), call)
					if err != nil {
						b.Fatal(err)
					}

					ids = append(ids, id)
				}

				called := time.Since(start)

				// Records are written in order: the last one's end stored
				// is every record of the burst handled, stored or dropped.
				last := ids[len(ids)-1]
				for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Millisecond) {
					run, _, _, err := s.Runs().Get(b.Context(), last)
					if err == nil && run.GetRun().GetStatus() == consolev1.RunStatus_RUN_STATUS_COMPLETED {
						break
					}

					if time.Now().After(deadline) {
						// The last record itself may be one of those dropped.
						break
					}
				}

				recorded := time.Since(start)

				b.StopTimer()

				lost := 0

				for _, id := range ids {
					run, steps, _, err := s.Runs().Get(b.Context(), id)
					if err != nil || run.GetRun().GetStatus() != consolev1.RunStatus_RUN_STATUS_COMPLETED || len(steps) != 2 {
						lost++
					}
				}

				b.StartTimer()

				b.ReportMetric(float64(called.Microseconds())/float64(burst), "µs/call")
				b.ReportMetric(float64(burst)/recorded.Seconds(), "recorded-runs/s")
				b.ReportMetric(100*float64(lost)/float64(burst), "%runs-incomplete")
			}
		})
	}
}

// BenchmarkRunPreamble is what Service.Execute does before every run, on
// the same program each time: marshal it for the run's record and read
// the masks back out of that JSON.
func BenchmarkRunPreamble(b *testing.B) {
	p := program(b, `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.template}}
  send: {activity: smtp.Send, input: {to: req.to, text: render.id}}
result: {id: send.id}
`)

	for b.Loop() {
		data, err := json.Marshal(p)
		if err != nil {
			b.Fatal(err)
		}

		_ = redact.ParseProgram(data)
	}
}
