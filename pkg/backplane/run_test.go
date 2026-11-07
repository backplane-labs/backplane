package backplane_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	helloconsolev1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/console/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
)

// TestDescriptorsServed: the descriptor set the manifest names by hash is
// served on the platform port, behind the internal secret.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestDescriptorsServed(t *testing.T) {
	p := setup(t, "")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("hello")...)
	if err != nil {
		t.Fatal(err)
	}

	svc.Internal(func(r grpc.ServiceRegistrar) { helloconsolev1.RegisterAdminServiceServer(r, operatorAdmin{}) })

	// The set of the same services, as the manifest names it.
	b := manifest.New("hello", "0.0.0")
	b.Internal([]string{helloconsolev1.AdminService_ServiceDesc.ServiceName})

	want, ok, err := b.DescriptorFiles()
	if err != nil || !ok {
		t.Fatalf("expected set: %v %v", ok, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	get := func(secretValue string) (int, []byte) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
			p.platformURL()+"/_backplane/api/"+want.Hash()+"/"+manifest.DescriptorFile, http.NoBody)
		req.Header.Set(guard.HTTPHeader, secretValue)

		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()

		body, _ := io.ReadAll(res.Body)

		return res.StatusCode, body
	}

	code, body := get(secret)

	var set descriptorpb.FileDescriptorSet
	if code != http.StatusOK || proto.Unmarshal(body, &set) != nil {
		t.Fatalf("descriptors: %d", code)
	}

	found := false

	for _, f := range set.GetFile() {
		for _, s := range f.GetService() {
			found = found || f.GetPackage()+"."+s.GetName() == helloconsolev1.AdminService_ServiceDesc.ServiceName
		}
	}

	if !found {
		t.Fatal("served set lacks the internal service")
	}

	if denied, _ := get("wrong"); denied != http.StatusForbidden {
		t.Fatalf("without the secret: %d", denied)
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}
