package env_test

import (
	"testing"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// A changed live section tells the subscribers; an equal one does not.
func TestSetSection(t *testing.T) {
	t.Parallel()

	e := env.New("kratos", nil)

	changes := 0

	e.OnSectionChange(func() { changes++ })

	for _, v := range []string{"a", "a", "b"} {
		value, err := anypb.New(wrapperspb.String(v))
		if err != nil {
			t.Fatal(err)
		}

		e.SetSection("kratos.login", value)
	}

	if changes != 2 || len(e.LiveSections()) != 1 {
		t.Fatalf("changes %d, sections %v", changes, e.LiveSections())
	}
}
