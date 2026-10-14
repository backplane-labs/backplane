package temporal

import "github.com/backplane-labs/backplane/pkg/backplane/internal/decl"

// CheckName validates a declaration name.
func CheckName(kind, name string) { decl.CheckName(kind, name) }
