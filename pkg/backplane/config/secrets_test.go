package config_test

import (
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	infranats "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
)

type natsSecretsConfig struct {
	config.Backplane `json:"backplane"`
	Realtime         infranats.Config `json:"realtime"`
}

func TestNATSCredentialsMaskedInEffectiveConfig(t *testing.T) {
	t.Parallel()
	path := write(t, "backplane:\n  nats:\n    url: nats://review:test-nats-password@127.0.0.1:4222\nrealtime:\n  url: nats://review:test-nats-password@127.0.0.1:4223\n")

	rt, err := config.Open[natsSecretsConfig](t.Context(), config.Service("review"), config.File(path), config.WithoutEnv(), config.WithoutConsul())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.Value().NATS.URL.Reveal() != "nats://review:test-nats-password@127.0.0.1:4222" || rt.Value().Realtime.URL.Reveal() != "nats://review:test-nats-password@127.0.0.1:4223" {
		t.Fatal("transport credentials were lost")
	}

	eff := link.ConfigState(rt).Effective()
	if eff.Err != nil {
		t.Fatal(eff.Err)
	}

	if strings.Contains(string(eff.Values), "test-nats-password") {
		t.Fatalf("effective config published credentials: %s", eff.Values)
	}
}
