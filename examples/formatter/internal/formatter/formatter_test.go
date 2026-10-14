package formatter_test

import (
	"errors"
	"testing"

	"github.com/backplane-labs/backplane/examples/formatter/internal/formatter"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

func TestActivitiesAndLiveConfig(t *testing.T) {
	t.Parallel()
	tree := backplanetest.New(t, backplanetest.Name("formatter"))
	cfg := backplanetest.Config[formatter.Config](t, nil)
	f := formatter.New(tree.Root(), &cfg)
	tree.Start()

	out, err := backplanetest.Activity[formatter.Input, formatter.Output](t.Context(), tree, "Format", formatter.Input{Name: "ann"})
	if err != nil || out.Text != "Welcome, ann!" {
		t.Fatalf("format: %+v %v", out, err)
	}

	backplanetest.SetLive(&cfg.Prefix, "Hi")

	out, err = backplanetest.Activity[formatter.Input, formatter.Output](t.Context(), tree, "Format", formatter.Input{Name: "bob"})
	if err != nil || out.Text != "Hi, bob!" {
		t.Fatalf("live format: %+v %v", out, err)
	}

	receipt, err := backplanetest.Activity[formatter.Greeting, formatter.Receipt](t.Context(), tree, "Record", formatter.Greeting{Name: "bob", Text: out.Text})
	if err != nil || receipt.Count != 1 || receipt.Key == "" {
		t.Fatalf("record: %+v %v", receipt, err)
	}

	if _, err := f.Format(t.Context(), formatter.Input{}); !errors.Is(err, formatter.ErrName) {
		t.Fatalf("invalid: %v", err)
	}

	if got := f.Snapshot(); got.Formatted != 2 || got.Recorded != 1 || got.LastText != "Hi, bob!" {
		t.Fatalf("stats: %+v", got)
	}
}
