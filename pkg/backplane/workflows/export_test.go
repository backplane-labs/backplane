package workflows

import (
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
)

func Seal(scope deps.Scope) { e, _ := decl.Env(scope, "test"); e.Manifest.Seal() }

func ManifestError(scope deps.Scope) error {
	e, _ := decl.Env(scope, "test")
	_, err := e.Manifest.Build()

	return err
}
