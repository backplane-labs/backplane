package meta_test

import (
	"context"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/meta"
)

func TestMeta(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if meta.From(ctx) != nil || meta.Get(ctx, "user") != "" {
		t.Fatal("empty context has metadata")
	}

	ctx = meta.With(ctx, "user", "u1", "tenant", "t1")
	ctx2 := meta.Merge(ctx, map[string]string{"user": "u2"})

	if meta.Get(ctx, "user") != "u1" || meta.Get(ctx2, "user") != "u2" || meta.Get(ctx2, "tenant") != "t1" {
		t.Fatalf("merge: %v %v", meta.From(ctx), meta.From(ctx2))
	}

	md := meta.From(ctx2)
	md["user"] = "mutated"

	if meta.Get(ctx2, "user") != "u2" {
		t.Fatal("From is not a copy")
	}

	if got := meta.From(meta.Replace(ctx2, map[string]string{"x": "1"})); len(got) != 1 || got["x"] != "1" {
		t.Fatalf("replace: %v", got)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("odd pairs did not panic")
		}
	}()

	meta.With(ctx, "k") //nolint:staticcheck // the odd count is the point
}
