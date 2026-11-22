package config_test

import (
	"errors"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

var errNotRunnable = errors.New("needs a key to run")

// keyed requires a credential and refuses to run without it.
type keyed struct {
	config.Backplane `json:"backplane"`

	Key  config.Secret `json:"key"`
	Name string        `json:"name"`
}

func (k keyed) Validate() error {
	if k.Key.Reveal() == "" {
		return errNotRunnable
	}

	return nil
}

func TestDescribing(t *testing.T) {
	t.Setenv("DESCRIBED_NAME", "n")

	if _, err := config.Open[keyed](t.Context(), config.Service("described"), config.WithoutFile(),
		config.WithoutConsul()); err == nil {
		t.Fatal("a missing secret opened a configuration to run")
	}

	rt, err := config.Open[keyed](t.Context(), config.Service("described"), config.WithoutFile(), config.Describing())
	if err != nil {
		t.Fatalf("describing without the secret: %v", err)
	}

	t.Cleanup(func() { _ = rt.Close() })

	if got := (*rt.Value()).Name; got != "n" {
		t.Fatalf("name = %q", got)
	}

	// Required non-secret fields stay required: declarations may depend on them.
	if _, err := config.Open[keyed](t.Context(), config.Service("undescribed"), config.WithoutFile(),
		config.Describing()); err == nil {
		t.Fatal("a missing non-secret field passed while describing")
	}
}
