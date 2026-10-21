package backplanetest_test

import (
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

func TestCaller(t *testing.T) {
	t.Parallel()

	if _, ok := backplane.CallerOf(t.Context()); ok {
		t.Fatal("a caller without one")
	}

	want := backplane.Caller{Service: "sync", Namespace: "default", Partition: "default", Datacenter: "dc1"}

	got, ok := backplane.CallerOf(backplanetest.Caller(t.Context(), want))
	if !ok || got != want {
		t.Fatalf("caller: %+v, %v", got, ok)
	}
}
