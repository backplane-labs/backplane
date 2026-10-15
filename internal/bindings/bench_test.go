package bindings_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/internal/bindings"
)

// The cost of the language on its own: reading a definition, compiling
// it, carrying the program to a workflow and evaluating a run.

// chainBinding is a binding of n steps, each reading the one before.
func chainBinding(hook string, n int) string {
	var text strings.Builder

	text.WriteString("hook: " + hook + "\nsteps:\n")
	text.WriteString("  s0: {activity: billing.Charge, input: {v: req.to}}\n")

	for i := 1; i < n; i++ {
		fmt.Fprintf(&text, "  s%d: {activity: billing.Charge, input: {v: s%d.v}}\n", i, i-1)
	}

	fmt.Fprintf(&text, "result: {message_id: s%d.v}\n", n-1)

	return text.String()
}

func BenchmarkBindingYAML(b *testing.B) {
	for b.Loop() {
		if _, err := bindings.BindingYAML(sendEmail); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompileBinding(b *testing.B) {
	installation := manifests(b)

	for _, bench := range []struct{ name, text string }{
		{"send-email", sendEmail},
		{"for-each", forEachBinding},
		{"chain-20", chainBinding("iam.SendEmail", 20)},
	} {
		def := mustBinding(b, bench.text)

		b.Run(bench.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := bindings.CompileBinding(def, installation); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkProgramJSON is a program's trip to a workflow: marshaled into
// its input, rebuilt (every expression compiled again) on its side.
func BenchmarkProgramJSON(b *testing.B) {
	for _, bench := range []struct{ name, text string }{
		{"send-email", sendEmail},
		{"for-each", forEachBinding},
		{"chain-20", chainBinding("iam.SendEmail", 20)},
	} {
		p := mustCompile(b, mustBinding(b, bench.text))

		data, err := json.Marshal(p)
		if err != nil {
			b.Fatal(err)
		}

		b.Run(bench.name+"/marshal", func(b *testing.B) {
			b.SetBytes(int64(len(data)))

			for b.Loop() {
				if _, err := json.Marshal(p); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(bench.name+"/unmarshal", func(b *testing.B) {
			b.SetBytes(int64(len(data)))

			for b.Loop() {
				var out bindings.Program
				if err := json.Unmarshal(data, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkEvalSendEmail is every evaluation of one run of §7.1's example:
// the scope, two inputs, two outputs bound, the result.
func BenchmarkEvalSendEmail(b *testing.B) {
	p := mustCompile(b, mustBinding(b, sendEmail))
	input := []byte(`{"to":"a@b.c","template":"welcome","data":{"name":"Ann"}}`)

	run := func() error {
		s, err := p.Start(input)
		if err != nil {
			return err
		}

		for _, st := range []struct {
			name string
			out  []byte
		}{
			{"render", []byte(`{"subject":"Hi","text":"Hello Ann"}`)},
			{"send", []byte(`{"id":"m-1","size":"42"}`)},
		} {
			if _, err := p.When(st.name, s); err != nil {
				return err
			}

			if _, err := p.Input(st.name, s); err != nil {
				return err
			}

			if s, err = p.Bind(s, st.name, st.out); err != nil {
				return err
			}
		}

		_, err = p.Result(s)

		return err
	}

	b.Run("serial", func(b *testing.B) {
		for b.Loop() {
			if err := run(); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("parallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if err := run(); err != nil {
					b.Error(err)

					return
				}
			}
		})
	})
}

// BenchmarkEvalChain binds n steps one after another: every Bind copies
// the scope, so a run's binding cost grows with its steps squared.
func BenchmarkEvalChain(b *testing.B) {
	for _, n := range []int{5, 20, 50} {
		p := mustCompile(b, mustBinding(b, chainBinding("iam.SendEmail", n)))
		out := []byte(`{"v":"x"}`)

		b.Run(fmt.Sprintf("steps-%d", n), func(b *testing.B) {
			for b.Loop() {
				s, err := p.Start([]byte(`{"to":"a@b.c"}`))
				if err != nil {
					b.Fatal(err)
				}

				for i := range n {
					name := fmt.Sprintf("s%d", i)
					if _, err := p.Input(name, s); err != nil {
						b.Fatal(err)
					}

					if s, err = p.Bind(s, name, out); err != nil {
						b.Fatal(err)
					}
				}

				if _, err := p.Result(s); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkEvalComprehension is an input over a map of the request: the
// ordered iteration sorts the keys on every evaluation.
func BenchmarkEvalComprehension(b *testing.B) {
	p := mustCompile(b, mustBinding(b, `
hook: iam.SendEmail
steps:
  send:
    activity: smtp.Send
    input:
      to: req.to
      subject: 'req.data.map(k, k + "=" + req.data[k]).join(",")'
      text: 'req.recipients.filter(r, r.email.endsWith("@b.c")).map(r, r.name).join(";")'
result: {message_id: send.id}
`))

	for _, n := range []int{10, 100, 1000} {
		data := map[string]string{}
		recipients := make([]map[string]string, 0, n)

		for i := range n {
			data[fmt.Sprintf("k%04d", i)] = strconv.Itoa(i)
			recipients = append(recipients, map[string]string{"email": fmt.Sprintf("u%d@b.c", i), "name": strconv.Itoa(i)})
		}

		input, err := json.Marshal(map[string]any{"to": "a@b.c", "data": data, "recipients": recipients})
		if err != nil {
			b.Fatal(err)
		}

		b.Run(fmt.Sprintf("items-%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(input)))

			for b.Loop() {
				s, err := p.Start(input)
				if err != nil {
					b.Fatal(err)
				}

				if _, err := p.Input("send", s); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkActive is the lookup every hook call makes before it runs: the
// current binding of the hook, from PostgreSQL (BACKPLANE_TEST_PG).
func BenchmarkActive(b *testing.B) {
	m, iam := live(b)
	hook := iam + ".SendEmail"

	if _, err := m.Save(b.Context(), mustBinding(b, strings.Replace(sendEmail, "iam.", iam+".", 1)),
		"", bindings.Base{}); err != nil {
		b.Fatal(err)
	}

	b.Run("serial", func(b *testing.B) {
		for b.Loop() {
			if _, _, err := m.Active(b.Context(), hook); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("parallel", func(b *testing.B) {
		ctx := b.Context()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, _, err := m.Active(ctx, hook); err != nil {
					b.Error(err)

					return
				}
			}
		})
	})
}
