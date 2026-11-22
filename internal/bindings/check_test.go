package bindings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/internal/bindings"
)

func TestCheckDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	text := "bindings:\n" + indent(sendEmail) + "\nrules:\n  - name: broken\n    event: iam.UserRegistered\n    steps:\n      send: {activity: smtp.Send, input: {to: email}}\n"

	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := bindings.CheckDir(dir, manifests(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range got {
		if v.Kind != "rule" || v.Name != "broken" || v.File != "a.yaml" {
			t.Fatalf("violation outside the broken rule: %+v", v)
		}
	}

	if len(got) == 0 {
		t.Fatal("the broken rule passed")
	}
}

// indent makes a binding document an item of bindings.
func indent(doc string) string {
	var out strings.Builder

	for line := range strings.Lines(strings.TrimSpace(doc)) {
		if out.Len() == 0 {
			out.WriteString("  - " + line)
		} else {
			out.WriteString("    " + line)
		}
	}

	return out.String()
}
