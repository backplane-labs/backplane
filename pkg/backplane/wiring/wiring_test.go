package wiring_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/wiring"
)

type (
	alertIn struct {
		Alert string `json:"alert"`
		To    string `json:"to"`
	}
	alertOut struct {
		ID string `json:"id"`
	}
	sendIn struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	sendOut struct {
		ID string `json:"id"`
	}
)

// installation is a relay raising Deliver and a courier sending, as their
// processes declare them.
func installation(t *testing.T) []*backplanev1.Manifest {
	t.Helper()

	relay := backplanetest.New(t, backplanetest.Name("relay"))
	hook.Declare[alertIn, alertOut](relay.Root(), "Deliver")

	courier := backplanetest.New(t, backplanetest.Name("courier"))
	activity.Handle(courier.Root(), "Send", func(context.Context, sendIn) (sendOut, error) { return sendOut{}, nil })

	return []*backplanev1.Manifest{relay.Manifest(), courier.Manifest()}
}

func dir(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for name, text := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

const good = `
bindings:
  - hook: relay.Deliver
    steps:
      send: {activity: courier.Send, input: {to: req.to, text: req.alert}}
    result: {id: send.id}
`

func TestCheck(t *testing.T) {
	t.Parallel()

	manifests := installation(t)

	for _, tc := range []struct {
		name  string
		files map[string]string
		opts  []wiring.Option
		want  []string // "<file> <kind> <name> <path>"
	}{
		{name: "valid", files: map[string]string{"relay.yaml": good}},
		{
			name:  "a bare word in input",
			files: map[string]string{"relay.yaml": strings.Replace(good, "text: req.alert", "text: alert", 1)},
			want:  []string{"relay.yaml binding relay.Deliver /steps/send/input/text"},
		},
		{
			name: "an undeclared service",
			files: map[string]string{"rules.yaml": `
rules:
  - name: ghost
    event: ghost.Happened
    steps:
      send: {activity: courier.Send, input: {to: "'a@b'", text: "'t'"}}
`},
			want: []string{"rules.yaml rule ghost /event"},
		},
		{
			name: "undo on the direct runtime",
			files: map[string]string{"relay.yaml": strings.Replace(good,
				"input: {to: req.to, text: req.alert}", "input: {to: req.to, text: req.alert}, undo: courier.Send", 1)},
			opts: []wiring.Option{wiring.DirectRuntime()},
			want: []string{"relay.yaml binding relay.Deliver /steps/send/undo"},
		},
		{
			name:  "a ConfigMap's own files are skipped",
			files: map[string]string{"relay.yaml": good, "..data/broken.yaml": "{"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := wiring.Check(dir(t, tc.files), manifests, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}

			places := make([]string, 0, len(got))
			for _, v := range got {
				if v.Code == "" || v.Message == "" {
					t.Errorf("violation without code or message: %s", v)
				}

				places = append(places, strings.Join([]string{v.File, v.Kind, v.Name, v.Path}, " "))
			}

			for _, want := range tc.want {
				if !slices.Contains(places, want) {
					t.Errorf("want %q in %v", want, got)
				}
			}

			if len(tc.want) == 0 && len(got) != 0 {
				t.Errorf("violations of a valid directory: %v", got)
			}
		})
	}
}

func TestCheckUnreadable(t *testing.T) {
	t.Parallel()

	manifests := installation(t)

	for name, files := range map[string]map[string]string{
		"yaml":           {"a.yaml": "bindings: [{"},
		"unknown field":  {"a.yaml": "bindings: [{hook: relay.Deliver, stepz: {}}]"},
		"duplicate hook": {"a.yaml": good, "b.yaml": good},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := wiring.Check(dir(t, files), manifests); err == nil {
				t.Fatal("an unreadable directory checked")
			}
		})
	}

	if _, err := wiring.Check(filepath.Join(t.TempDir(), "missing"), manifests); err == nil {
		t.Fatal("a missing directory checked")
	}
}

func BenchmarkCheck(b *testing.B) {
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "relay.yaml"), []byte(good), 0o600); err != nil {
		b.Fatal(err)
	}

	relay := backplanetest.New(b, backplanetest.Name("relay"))
	hook.Declare[alertIn, alertOut](relay.Root(), "Deliver")

	courier := backplanetest.New(b, backplanetest.Name("courier"))
	activity.Handle(courier.Root(), "Send", func(context.Context, sendIn) (sendOut, error) { return sendOut{}, nil })

	manifests := []*backplanev1.Manifest{relay.Manifest(), courier.Manifest()}

	for b.Loop() {
		if _, err := wiring.Check(root, manifests); err != nil {
			b.Fatal(err)
		}
	}
}
