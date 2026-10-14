// Package hellotest builds hello's components under a backplanetest
// harness, as NewState wires them, for the component tests.
package hellotest

import (
	"testing"

	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/examples/hello/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Tree is a harness named hello with a store and a greeter under its root.
type Tree struct {
	H        *backplanetest.Harness
	StoreCfg *store.Config
	Store    deps.Dependency[*store.DB]
	GreetCfg *greeter.Config
	Greeter  *greeter.Greeter
}

// New builds the tree; greeter configuration comes from vars (names as in
// HELLO_GREETER_* without the prefix). Start it with t.H.Start().
func New(t *testing.T, vars map[string]string) *Tree {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("hello"))
	storeCfg := backplanetest.Config[store.Config](t, nil)
	greetCfg := backplanetest.Config[greeter.Config](t, vars)
	db := deps.NewDependency(h.Root(), store.New(&storeCfg))

	g, err := greeter.New(h.Root(), &greetCfg, db)
	if err != nil {
		t.Fatal(err)
	}

	return &Tree{H: h, StoreCfg: &storeCfg, Store: db, GreetCfg: &greetCfg, Greeter: g}
}
