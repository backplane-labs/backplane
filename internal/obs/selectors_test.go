package obs_test

import (
	"context"
	"testing"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/obs"
	"github.com/backplane-labs/backplane/internal/registry"
)

type source struct{ catalog registry.Catalog }

func (s source) Current() registry.Catalog             { return s.catalog }
func (source) Changes(context.Context) <-chan struct{} { return nil }

func TestSelectorPrecedence(t *testing.T) {
	t.Parallel()

	declared := &backplanev1.TelemetrySources{Selectors: []*backplanev1.TelemetrySourceSelector{{Resource: map[string]string{"service.namespace": "identity"}}}}
	registrySource := source{catalog: registry.Catalog{Services: map[string]registry.Service{
		"declared":   {Manifests: map[string]*backplanev1.Manifest{"1.0.0": {Telemetry: declared}}},
		"empty":      {Manifests: map[string]*backplanev1.Manifest{"1.0.0": {Telemetry: &backplanev1.TelemetrySources{}}}},
		"overridden": {Manifests: map[string]*backplanev1.Manifest{"1.0.0": {Telemetry: declared}}},
	}}}
	cfg := obs.Config{SourceOverrides: map[string]obs.SourceSelection{"overridden": {}, "deployment": {Selectors: []map[string]string{{"service.name": "kratos"}, {"service.name": "wrapper"}}}}}

	svc, err := obs.New(cfg, obs.WithRegistry(registrySource))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(svc.Close)

	for _, tc := range []struct {
		name   string
		origin consolev1.ObsSelectorOrigin
		count  int
	}{
		{"undeclared", consolev1.ObsSelectorOrigin_OBS_SELECTOR_ORIGIN_CONVENTION, 1},
		{"declared", consolev1.ObsSelectorOrigin_OBS_SELECTOR_ORIGIN_MANIFEST, 1},
		{"empty", consolev1.ObsSelectorOrigin_OBS_SELECTOR_ORIGIN_MANIFEST, 0},
		{"overridden", consolev1.ObsSelectorOrigin_OBS_SELECTOR_ORIGIN_DEPLOYMENT, 0},
		{"deployment", consolev1.ObsSelectorOrigin_OBS_SELECTOR_ORIGIN_DEPLOYMENT, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			response, callErr := svc.GetObsSelectors(t.Context(), &consolev1.GetObsSelectorsRequest{Service: tc.name})
			if callErr != nil || response.GetOrigin() != tc.origin || len(response.GetSources().GetSelectors()) != tc.count {
				t.Fatal(response, callErr)
			}

			if tc.count > 0 {
				response.Sources.Selectors[0].Resource["mutated"] = "must stay local"

				again, _ := svc.GetObsSelectors(t.Context(), &consolev1.GetObsSelectorsRequest{Service: tc.name})
				if again.GetSources().GetSelectors()[0].GetResource()["mutated"] != "" {
					t.Fatal("caller mutated shared selectors")
				}
			}
		})
	}
}
