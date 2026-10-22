package config_test

import (
	"testing"

	sp "github.com/gopherex/schemapb/go/schemapb"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/valkey"
)

// The console labels a field with its description: every field of the
// SDK's own sections has one.
func TestSectionsDescribeEveryField(t *testing.T) {
	t.Parallel()

	sections := map[string]func() (*sp.Schema, error){
		"backplane": schemaOf[config.Backplane],
		"nats":      schemaOf[nats.Config],
		"postgres":  schemaOf[postgres.Config],
		"temporal":  schemaOf[temporal.Config],
		"valkey":    schemaOf[valkey.Config],
	}

	for name, of := range sections {
		schema, err := of()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		undescribed(t, name, schema.GetFields())
	}
}

func schemaOf[T any]() (*sp.Schema, error) {
	return sp.ReflectType[T](sp.ID(sp.Namespace("test"), "config", sp.Ver(0, 0, 0)))
}

func undescribed(t *testing.T, path string, fields []*sp.Schema_Field) {
	t.Helper()

	for _, f := range fields {
		field := path + "." + f.GetName()
		if f.GetDescription() == "" {
			t.Errorf("%s has no description", field)
		}

		undescribed(t, field, f.GetObject().GetSchema().GetFields())

		for _, item := range f.GetList().GetItems() {
			undescribed(t, field+"[]", item.GetObject().GetSchema().GetFields())
		}
	}
}
