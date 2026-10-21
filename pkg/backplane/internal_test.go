package backplane_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	helloconsolev1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/console/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
)

// TestInternalInterceptors: the internal API's own interceptors run inside
// the internal secret's check, the console operator already known.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestInternalInterceptors(t *testing.T) {
	p := setup(t, "")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("hello")...)
	if err != nil {
		t.Fatal(err)
	}

	operatorOnly := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if _, ok := backplane.OperatorOf(ctx); !ok {
			return nil, status.Error(codes.PermissionDenied, "no operator")
		}

		return next(ctx, req)
	}

	svc.Internal(func(r grpc.ServiceRegistrar) { helloconsolev1.RegisterAdminServiceServer(r, operatorAdmin{}) },
		api.Interceptors(operatorOnly))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	client := helloconsolev1.NewAdminServiceClient(dial(t, p.platform))

	out, err := client.GetStats(metadata.AppendToOutgoingContext(ctx,
		guard.Header, secret, guard.SessionHeader, operatorSession), &helloconsolev1.GetStatsRequest{})
	if err != nil || out.GetGreetings() != 1 {
		t.Fatalf("operator: %v, %d greetings", err, out.GetGreetings())
	}

	_, err = client.GetStats(metadata.AppendToOutgoingContext(ctx, guard.Header, secret), &helloconsolev1.GetStatsRequest{})
	if status.Convert(err).Message() != "no operator" {
		t.Fatalf("no operator: %v", err)
	}

	_, err = client.GetStats(ctx, &helloconsolev1.GetStatsRequest{})
	if status.Convert(err).Message() != "internal secret required" {
		t.Fatalf("no secret: %v", err)
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}
