package bindings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/internal/bindings"
)

func TestReadWiring(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		files map[string]string
		want  string // error substring; "" reads
	}{
		"bindings and rules": {files: map[string]string{
			"a.yaml": "bindings:\n  - {hook: a.H, runtime: direct}\n---\nrules:\n  - {name: r, event: a.E}\n",
			"b.yml":  "rules:\n  - {name: s, event: a.E}\n",
			"c.txt":  "not wiring",
		}},
		"unknown key":       {files: map[string]string{"a.yaml": "binding: []\n"}, want: "field binding not found"},
		"no hook":           {files: map[string]string{"a.yaml": "bindings: [{description: x}]\n"}, want: "hook is required"},
		"no rule name":      {files: map[string]string{"a.yaml": "rules: [{event: a.E}]\n"}, want: "name is required"},
		"unknown field":     {files: map[string]string{"a.yaml": "bindings: [{hook: a.H, stepz: {}}]\n"}, want: "stepz"},
		"duplicate binding": {files: map[string]string{"a.yaml": "bindings: [{hook: a.H}]\n", "b.yaml": "bindings: [{hook: a.H}]\n"}, want: "defined in a.yaml too"},
		"duplicate rule":    {files: map[string]string{"a.yaml": "rules: [{name: r, event: a.E}, {name: r, event: a.F}]\n"}, want: "defined in a.yaml too"},
		"broken yaml":       {files: map[string]string{"a.yaml": "{"}, want: "a.yaml: yaml"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			for file, text := range c.files {
				if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			read, err := bindings.ReadWiring(dir)

			switch {
			case c.want == "" && err != nil:
				t.Fatal(err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err = %v, want %q", err, c.want)
			case c.want == "":
				if len(read.Bindings) != 1 || len(read.Rules) != 2 || read.Bindings["a.H"] != "a.yaml" ||
					read.Rules[bindings.FileRuleID("s")] != "b.yml" || read.Digest == "" {
					t.Fatalf("read: %+v", read)
				}
			}
		})
	}

	if _, err := bindings.ReadWiring(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing directory read")
	}
}
