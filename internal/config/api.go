package config

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// invalid is the InvalidArgument status of a request.
func invalid(msg string) error {
	return status.Error(codes.InvalidArgument, msg) //nolint:wrapcheck // a gRPC status travels as is
}

// Page sizes of ListRevisions.
const (
	defaultPage = 50
	maxPage     = 500
)

// API is the Manager as backplane.console.v1.ConfigService.
type API struct {
	consolev1.UnimplementedConfigServiceServer

	m *Manager
}

var _ consolev1.ConfigServiceServer = API{}

// API serves m to the console.
func (m *Manager) API() API { return API{m: m} }

// Register registers the API on r.
func (m *Manager) Register(r grpc.ServiceRegistrar) {
	consolev1.RegisterConfigServiceServer(r, m.API())
}

// GetConfig implements ConfigService.
func (a API) GetConfig(ctx context.Context, req *consolev1.GetConfigRequest) (*consolev1.GetConfigResponse, error) {
	if req.GetService() == "" {
		return nil, invalid("service is required")
	}

	cfg, err := a.m.View(ctx, req.GetService())
	if err != nil {
		return nil, a.status(err)
	}

	return &consolev1.GetConfigResponse{Config: cfg}, nil
}

// ListRevisions implements ConfigService.
func (a API) ListRevisions(
	ctx context.Context, req *consolev1.ListRevisionsRequest,
) (*consolev1.ListRevisionsResponse, error) {
	if req.GetService() == "" {
		return nil, invalid("service is required")
	}

	size := int(req.GetPageSize())
	if size <= 0 {
		size = defaultPage
	}

	size = min(size, maxPage)

	before := int64(min(req.GetBefore(), math.MaxInt64)) //nolint:gosec // bounded

	revs, err := a.m.List(ctx, req.GetService(), before, size+1)
	if err != nil {
		return nil, a.status(err)
	}

	out := &consolev1.ListRevisionsResponse{}

	if len(revs) > size {
		revs = revs[:size]
		out.NextBefore = uint64(revs[size-1].Revision) //nolint:gosec // revisions are positive
	}

	for _, r := range revs {
		out.Revisions = append(out.Revisions, revisionPB(r))
	}

	return out, nil
}

// ValidateOverride implements ConfigService.
func (a API) ValidateOverride(
	_ context.Context, req *consolev1.ValidateOverrideRequest,
) (*consolev1.ValidateOverrideResponse, error) {
	if req.GetService() == "" {
		return nil, invalid("service is required")
	}

	v, err := a.m.Validate(req.GetService(), req.GetValues())
	if err != nil {
		return nil, a.status(err)
	}

	return &consolev1.ValidateOverrideResponse{Violations: violationsPB(v)}, nil
}

// SaveRevision implements ConfigService.
func (a API) SaveRevision(
	ctx context.Context, req *consolev1.SaveRevisionRequest,
) (*consolev1.SaveRevisionResponse, error) {
	if req.GetService() == "" {
		return nil, invalid("service is required")
	}

	saved, err := a.m.Save(ctx, req.GetService(), req.GetValues(), req.GetComment())
	if err != nil {
		return nil, a.status(err)
	}

	out := &consolev1.SaveRevisionResponse{Violations: violationsPB(saved.Violations)}
	if saved.Violations == nil {
		out.Revision = revisionPB(saved.Revision)
	}

	if saved.DeliveryErr != nil {
		out.DeliveryError = saved.DeliveryErr.Error()
	}

	return out, nil
}

// Rollback implements ConfigService.
func (a API) Rollback(ctx context.Context, req *consolev1.RollbackRequest) (*consolev1.RollbackResponse, error) {
	if req.GetService() == "" || req.GetRevision() == 0 {
		return nil, invalid("service and revision are required")
	}

	revision := int64(min(req.GetRevision(), math.MaxInt64)) //nolint:gosec // bounded

	saved, err := a.m.Rollback(ctx, req.GetService(), revision, req.GetComment())
	if err != nil {
		return nil, a.status(err)
	}

	out := &consolev1.RollbackResponse{Violations: violationsPB(saved.Violations)}
	if saved.Violations == nil {
		out.Revision = revisionPB(saved.Revision)
	}

	if saved.DeliveryErr != nil {
		out.DeliveryError = saved.DeliveryErr.Error()
	}

	return out, nil
}

// WatchConfig implements ConfigService: the configuration now, then again
// whenever it differs — the registry changed (instances, manifests,
// applied revisions) or a revision was saved here or on another replica.
func (a API) WatchConfig(
	req *consolev1.WatchConfigRequest, stream grpc.ServerStreamingServer[consolev1.WatchConfigResponse],
) error {
	if req.GetService() == "" {
		return invalid("service is required")
	}

	ctx := stream.Context()
	registry := a.m.src.Changes(ctx)
	saves := a.m.changes.subscribe(ctx)

	var last *consolev1.ServiceConfig

	for {
		cfg, err := a.m.View(ctx, req.GetService())
		if err != nil && !errors.Is(err, ErrNotSynced) {
			return a.status(err)
		}

		if err == nil && !proto.Equal(cfg, last) {
			if err := stream.Send(&consolev1.WatchConfigResponse{Config: cfg}); err != nil {
				return err //nolint:wrapcheck // the stream's own error
			}

			last = cfg
		}

		select {
		case _, ok := <-registry:
			if !ok {
				return nil
			}
		case _, ok := <-saves:
			if !ok {
				return nil
			}
		}
	}
}

// status maps errors to gRPC codes; unexpected ones are logged.
func (a API) status(err error) error {
	code := codes.Internal

	switch {
	case errors.Is(err, ErrNotSynced):
		code = codes.Unavailable
	case errors.Is(err, ErrNoService), errors.Is(err, ErrNoRevision):
		code = codes.NotFound
	case errors.Is(err, ErrNoManifest):
		code = codes.FailedPrecondition
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	default:
		a.m.Log().Error("config api", xlog.Err(err))
	}

	return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
}

// View is the service's configuration as the console shows it: the
// latest manifest's schema and Live paths, the current revision and every
// instance with a state.
func (m *Manager) View(ctx context.Context, service string) (*consolev1.ServiceConfig, error) {
	cat := m.src.Current()
	if cat.Index == 0 {
		return nil, ErrNotSynced
	}

	svc, known := cat.Services[service]

	cur, has, err := m.Current(ctx, service)
	if err != nil {
		return nil, err
	}

	if !known && !has {
		return nil, ErrNoService
	}

	out := &consolev1.ServiceConfig{Service: service}

	latest := svc.Latest()
	if latest != nil {
		out.ManifestVersion = latest.GetVersion()
		out.Schema = latest.GetConfig().GetSchema()
		out.Keys = latest.GetConfig().GetKeys()
		out.Live = latest.GetConfig().GetLive()
	}

	if has {
		out.Current = revisionPB(cur)
	}

	for _, in := range svc.Instances {
		if in.State == nil {
			continue
		}

		out.Instances = append(out.Instances, instancePB(in.ID, in.State, out.GetLive()))
	}

	return out, nil
}

func instancePB(id string, st *backplanev1.InstanceState, live []string) *consolev1.InstanceConfig {
	out := &consolev1.InstanceConfig{
		Id: id, Version: st.GetVersion(), Phase: st.GetPhase(), Config: st.GetConfig(),
		AppliedRevision: st.GetConfigRevision(), RejectedRevision: st.GetConfigRejectedRevision(),
		Error: st.GetConfigError(),
	}

	var values map[string]any
	if v, err := decodeJSON(st.GetConfig()); err == nil {
		values, _ = v.(map[string]any)
	}

	for _, path := range live {
		value := &consolev1.LiveValue{Path: path, Source: sourceOf(st.GetSources(), path)}

		if v, ok := lookup(values, strings.Split(path, ".")); ok {
			if raw, err := json.Marshal(v); err == nil {
				value.Value = string(raw)
			}
		}

		out.Live = append(out.Live, value)
	}

	return out
}

// sourceOf is the layer path came from: its own source, else the highest
// of the paths below it (a Live object's fields).
func sourceOf(sources map[string]backplanev1.ConfigSource, path string) backplanev1.ConfigSource {
	if s, ok := sources[path]; ok {
		return s
	}

	best := backplanev1.ConfigSource_CONFIG_SOURCE_UNSPECIFIED

	for p, s := range sources {
		if strings.HasPrefix(p, path+".") {
			best = max(best, s)
		}
	}

	return best
}

func lookup(m map[string]any, path []string) (any, bool) {
	var cur any = m

	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}

		if cur, ok = obj[key]; !ok {
			return nil, false
		}
	}

	return cur, true
}

func revisionPB(r Revision) *consolev1.Revision {
	out := &consolev1.Revision{
		Service: r.Service, Revision: uint64(max(r.Revision, 0)), Author: r.Author, Comment: r.Comment,
		CreatedAt: timestamppb.New(r.CreatedAt), RollbackOf: uint64(max(r.RollbackOf, 0)),
		Values: make(map[string]string, len(r.Values)),
	}

	for p, v := range r.Values {
		out.Values[p] = string(v)
	}

	return out
}

func violationsPB(vs []Violation) []*consolev1.ConfigViolation {
	out := make([]*consolev1.ConfigViolation, 0, len(vs))
	for _, v := range vs {
		out = append(out, &consolev1.ConfigViolation{Path: v.Path, Instance: v.Instance, Code: v.Code, Message: v.Message})
	}

	return out
}
