// Package platform exposes deployment capabilities and bounded, cached probes.
package platform

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

const (
	probeInterval = 5 * time.Second
	probeTimeout  = 3 * time.Second
)

// Probe checks one configured dependency. Run must respect its context.
type Probe struct {
	Name     string
	Required bool
	Run      func(context.Context) error
}

// Service keeps per-replica health; requests never probe external systems.
type Service struct {
	consolev1.UnimplementedPlatformServiceServer
	deps.Component
	capabilities *consolev1.GetCapabilitiesResponse
	mu           sync.RWMutex
	health       *consolev1.GetInfrastructureResponse
}

// New starts at most one bounded probe per dependency every five seconds.
func New(parent deps.Scope, instance string, capabilities []*consolev1.Capability, probes []Probe) *Service {
	s := &Service{
		Component: deps.NewComponent(parent,
			"platform"),
		capabilities: &consolev1.GetCapabilitiesResponse{Capabilities: capabilities},
		health:       &consolev1.GetInfrastructureResponse{InstanceId: instance},
	}
	for _, p := range probes {
		s.health.Dependencies = append(s.health.Dependencies,
			&consolev1.InfrastructureStatus{
				Name:     p.Name,
				Required: p.Required,
				Reason:   "checking",
			})
	}

	for i, p := range probes {
		s.Go(func(ctx context.Context) error {
			ticker := time.NewTicker(probeInterval)
			defer ticker.Stop()

			for {
				probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
				err := p.Run(probeCtx)

				reason := ""
				if err != nil {
					reason = "unavailable"
					if errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
						reason = "timeout"
					}
				}

				cancel()

				result := &consolev1.InfrastructureStatus{
					Name:      p.Name,
					Required:  p.Required,
					Ok:        err == nil,
					Reason:    reason,
					CheckedAt: timestamppb.Now(),
				}

				s.mu.Lock()
				s.health.Dependencies[i] = result
				s.mu.Unlock()

				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
				}
			}
		})
	}

	return s
}

// Register serves the authenticated platform API.
func (s *Service) Register(r grpc.ServiceRegistrar) { consolev1.RegisterPlatformServiceServer(r, s) }

// GetCapabilities returns immutable deployment configuration.
func (s *Service) GetCapabilities(context.Context,
	*consolev1.GetCapabilitiesRequest) (*consolev1.GetCapabilitiesResponse,
	error,
) {
	return proto.CloneOf(s.capabilities), nil
}

// GetInfrastructure returns the most recent completed checks.
func (s *Service) GetInfrastructure(context.Context,
	*consolev1.GetInfrastructureRequest) (*consolev1.GetInfrastructureResponse,
	error,
) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return proto.CloneOf(s.health), nil
}
