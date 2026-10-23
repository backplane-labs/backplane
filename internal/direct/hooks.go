package direct

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// HookAPI serves backplane.v1.HookService on backplane's platform port:
// the direct transport of the services' hook calls.
type HookAPI struct {
	consolev1.UnimplementedHookServiceServer

	s *Service
}

// HookService is the server.
func (s *Service) HookService() HookAPI { return HookAPI{s: s} }

// Register registers HookService on r (svc.Internal).
func (s *Service) Register(r grpc.ServiceRegistrar) {
	consolev1.RegisterHookServiceServer(r, s.HookService())
}

// keyHeader carries the call's key from the SDK's direct transport
// (pkg/backplane/internal/direct.KeyHeader).
const keyHeader = "bp-call-key"

// Raise implements HookService. A call with a key (the envelope's, else
// the bp-call-key header) answers with the outcome of the first call with
// that hook and key.
func (a HookAPI) Raise(ctx context.Context, req *consolev1.RaiseRequest) (*consolev1.RaiseResponse, error) {
	call := req.GetCall()
	if call.GetHook() == "" {
		return nil, status.Error(codes.InvalidArgument, "hook is required") //nolint:wrapcheck // a gRPC status
	}

	if call.GetKey() == "" {
		if v := metadata.ValueFromIncomingContext(ctx, keyHeader); len(v) > 0 {
			call.Key = v[0]
		}
	}

	res, err := a.s.CallHook(ctx, call)

	switch {
	case err == nil:
		return &consolev1.RaiseResponse{Result: res}, nil
	case errors.Is(err, ErrTemporalRuntime), errors.Is(err, ErrNotSynced):
		return nil, status.Error(codes.FailedPrecondition, err.Error()) //nolint:wrapcheck // a gRPC status
	case ctx.Err() == nil && !isFailure(err):
		a.s.Log().Ctx().Warn(ctx, "hook call failed", xlog.String("hook", call.GetHook()), xlog.Err(err))
	}

	return nil, Status(err)
}

func isFailure(err error) bool {
	var f *Failure

	return errors.As(err, &f)
}
