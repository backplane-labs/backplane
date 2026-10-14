package obs

import (
	"context"
	"maps"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
)

// SourceSelection is a deployment-only override. Presence of an empty entry
// explicitly disables module presets, without hiding any stored sources.
type SourceSelection struct {
	Selectors []map[string]string `json:"selectors,omitempty"`
}

// Option supplies read-only metadata; no telemetry ingestion dependency.
type Option func(*Service)

// WithRegistry resolves optional module declarations from current manifests.
func WithRegistry(source registry.Source) Option { return func(s *Service) { s.registry = source } }

// GetObsSelectors resolves deployment -> declaration -> OTel convention.
func (s *Service) GetObsSelectors(
	_ context.Context, req *consolev1.GetObsSelectorsRequest,
) (*consolev1.GetObsSelectorsResponse, error) {
	name := req.GetService()
	if strings.TrimSpace(name) == "" || len(name) > s.cfg.ExpressionBytes {
		return nil, rpcError(codes.InvalidArgument, "invalid service name")
	}

	if override, ok := s.cfg.SourceOverrides[name]; ok {
		sources := &backplanev1.TelemetrySources{}
		for _, resource := range override.Selectors {
			sources.Selectors = append(sources.Selectors, &backplanev1.TelemetrySourceSelector{Resource: maps.Clone(resource)})
		}

		return &consolev1.GetObsSelectorsResponse{
			Sources: sources, Origin: consolev1.ObsSelectorOrigin_OBS_SELECTOR_ORIGIN_DEPLOYMENT,
		}, nil
	}

	if s.registry != nil {
		service, ok := s.registry.Current().Services[name]
		if ok && service.Latest().GetTelemetry() != nil {
			return &consolev1.GetObsSelectorsResponse{
				Sources: proto.CloneOf(service.Latest().GetTelemetry()),
				Origin:  consolev1.ObsSelectorOrigin_OBS_SELECTOR_ORIGIN_MANIFEST,
			}, nil
		}
	}

	return &consolev1.GetObsSelectorsResponse{Sources: &backplanev1.TelemetrySources{
		Selectors: []*backplanev1.TelemetrySourceSelector{{Resource: map[string]string{"service.name": name}}},
	}, Origin: consolev1.ObsSelectorOrigin_OBS_SELECTOR_ORIGIN_CONVENTION}, nil
}
