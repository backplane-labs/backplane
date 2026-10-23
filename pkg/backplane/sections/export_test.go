package sections

import (
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
)

// Publish hands raw to the followers of name under scope, as the watch
// does.
func Publish(scope deps.Scope, name string, raw []byte) {
	e, _ := decl.Env(scope, "test")
	for _, fn := range e.Followed()[name] {
		fn(raw)
	}
}
