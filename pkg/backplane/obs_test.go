package backplane_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/obs"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
)

//nolint:paralleltest // SDK configuration reads process environment
func TestObsInternalSecret(t *testing.T) {
	ports := setup(t, "")

	reader, err := obs.New(obs.Config{LogsURL: "http://not-contacted.invalid"})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(reader.Close)

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) { return &testState{}, nil }, baseOptions("backplane")...)
	if err != nil {
		t.Fatal(err)
	}

	svc.Internal(reader.Register)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(ports))
	client := consolev1.NewObsServiceClient(dial(t, ports.platform))

	for _, credential := range []string{"", "wrong", secret} {
		callCtx := metadata.AppendToOutgoingContext(ctx, guard.Header, credential)

		response, callErr := client.GetObsCapabilities(callCtx, &consolev1.GetObsCapabilitiesRequest{})
		if credential == secret {
			if callErr != nil || len(response.GetSignals()) != 1 {
				t.Fatal(response, callErr)
			}
		} else if status.Code(callErr) != codes.PermissionDenied {
			t.Fatal("internal read accepted bad credential", callErr)
		}
	}

	cancel()

	if err = wait(t, done); err != nil {
		t.Fatal(err)
	}
}
