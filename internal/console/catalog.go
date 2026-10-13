package console

import (
	"context"
	"maps"
	"net"
	"slices"
	"strconv"

	"golang.org/x/mod/semver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/registry"
)

// catalogService serves CatalogService from the registry's snapshots.
type catalogService struct {
	consolev1.UnimplementedCatalogServiceServer

	c *Console
}

// ListServices implements CatalogService.
func (s catalogService) ListServices(
	context.Context, *consolev1.ListServicesRequest,
) (*consolev1.ListServicesResponse, error) {
	cat := s.c.src.Current()

	return &consolev1.ListServicesResponse{Services: summaries(cat), Index: cat.Index}, nil
}

// GetService implements CatalogService.
func (s catalogService) GetService(
	_ context.Context, req *consolev1.GetServiceRequest,
) (*consolev1.GetServiceResponse, error) {
	cat := s.c.src.Current()

	svc, ok := cat.Services[req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "service %q is not in the catalog", req.GetName())
	}

	res := &consolev1.GetServiceResponse{Summary: summary(svc), Latest: svc.Latest(), Index: cat.Index}
	for _, v := range versions(svc) {
		res.Manifests = append(res.Manifests, svc.Manifests[v])
	}

	for _, in := range svc.Instances {
		res.Instances = append(res.Instances, &consolev1.Instance{
			Id: in.ID, State: in.State, Registered: in.Registered, Healthy: in.Healthy,
			Address: in.Address, Port: in.Port, Tags: in.Tags,
		})
	}

	slices.SortFunc(res.GetInstances(), func(a, b *consolev1.Instance) int { return cmpString(a.GetId(), b.GetId()) })

	return res, nil
}

// WatchCatalog implements CatalogService: the summary now and after every
// new snapshot.
func (s catalogService) WatchCatalog(
	_ *consolev1.WatchCatalogRequest, stream grpc.ServerStreamingServer[consolev1.WatchCatalogResponse],
) error {
	ctx := stream.Context()
	changes := s.c.src.Changes(ctx)

	var (
		sent bool
		last uint64
	)

	for {
		if cat := s.c.src.Current(); !sent || cat.Index != last {
			if err := stream.Send(&consolev1.WatchCatalogResponse{Services: summaries(cat), Index: cat.Index}); err != nil {
				return err //nolint:wrapcheck // the stream's own status
			}

			sent, last = true, cat.Index
		}

		if _, ok := <-changes; !ok {
			return nil
		}
	}
}

// ListPlugins implements CatalogService.
func (s catalogService) ListPlugins(
	context.Context, *consolev1.ListPluginsRequest,
) (*consolev1.ListPluginsResponse, error) {
	cat := s.c.src.Current()
	res := &consolev1.ListPluginsResponse{}

	for _, name := range slices.Sorted(maps.Keys(cat.Services)) {
		svc := cat.Services[name]

		ui := svc.Latest().GetUi()
		if ui.GetHash() == "" {
			continue
		}

		preferred, _ := serving(svc, uiHash(ui.GetHash()))
		res.Plugins = append(res.Plugins, &consolev1.Plugin{
			Service: name, Hash: ui.GetHash(), SdkMajor: ui.GetSdkMajor(),
			Path:      s.c.Route() + "plugins/" + name + "/" + ui.GetHash() + "/",
			Available: len(preferred) > 0,
		})
	}

	return res, nil
}

func summaries(cat registry.Catalog) []*consolev1.ServiceSummary {
	out := make([]*consolev1.ServiceSummary, 0, len(cat.Services))
	for _, name := range slices.Sorted(maps.Keys(cat.Services)) {
		out = append(out, summary(cat.Services[name]))
	}

	return out
}

func summary(svc registry.Service) *consolev1.ServiceSummary {
	latest := svc.Latest()
	healthy := len(svc.Healthy())

	health := consolev1.ServiceHealth_SERVICE_HEALTH_DEGRADED

	switch {
	case healthy == 0:
		health = consolev1.ServiceHealth_SERVICE_HEALTH_DOWN
	case healthy == len(svc.Instances):
		health = consolev1.ServiceHealth_SERVICE_HEALTH_HEALTHY
	}

	return &consolev1.ServiceSummary{
		Name: svc.Name, LatestVersion: latest.GetVersion(), Versions: versions(svc),
		Instances: uint32(len(svc.Instances)), Healthy: uint32(healthy), //nolint:gosec // counts of instances
		Health: health, InternalApi: len(latest.GetInternalServices()) > 0, Ui: latest.GetUi().GetHash() != "",
	}
}

// versions of the known manifests, highest first (semver; others as
// strings).
func versions(svc registry.Service) []string {
	out := slices.Collect(maps.Keys(svc.Manifests))
	slices.SortFunc(out, func(a, b string) int {
		va, vb := "v"+a, "v"+b
		if semver.IsValid(va) && semver.IsValid(vb) {
			return semver.Compare(vb, va)
		}

		return cmpString(b, a)
	})

	return out
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

// serving lists the instances of svc whose own version's manifest match
// accepts, in phase SERVING with a platform port: registered and passing
// first (preferred), the rest as a fallback.
func serving(
	svc registry.Service, match func(m *backplanev1.Manifest) bool,
) ([]registry.Instance, []registry.Instance) {
	var preferred, fallback []registry.Instance

	for _, in := range svc.Instances {
		st := in.State
		if st.GetPhase() != backplanev1.InstancePhase_INSTANCE_PHASE_SERVING ||
			st.GetPlatformPort() == 0 || st.GetAddress() == "" || !match(svc.Manifest(st.GetVersion())) {
			continue
		}

		if in.Registered && in.Healthy {
			preferred = append(preferred, in)
		} else {
			fallback = append(fallback, in)
		}
	}

	return preferred, fallback
}

// platformAddr is host:port of the instance's platform port.
func platformAddr(in registry.Instance) string {
	return net.JoinHostPort(in.State.GetAddress(), strconv.FormatUint(uint64(in.State.GetPlatformPort()), 10))
}

// uiHash matches a manifest declaring the bundle hash.
func uiHash(hash string) func(m *backplanev1.Manifest) bool {
	return func(m *backplanev1.Manifest) bool { return m.GetUi().GetHash() == hash }
}

// internalService matches a manifest declaring the internal service.
func internalService(name string) func(m *backplanev1.Manifest) bool {
	return func(m *backplanev1.Manifest) bool { return slices.Contains(m.GetInternalServices(), name) }
}

func platformAddrs(ins []registry.Instance) []string {
	out := make([]string, 0, len(ins))
	for _, in := range ins {
		out = append(out, platformAddr(in))
	}

	return out
}
