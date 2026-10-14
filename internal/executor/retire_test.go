package executor_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/backplane-labs/backplane/internal/executor"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

type endpointAPI struct {
	operatorservice.OperatorServiceClient
	mu        sync.Mutex
	items     []*nexuspb.Endpoint
	deleted   []string
	deleteErr error
}

func (a *endpointAPI) ListNexusEndpoints(context.Context, *operatorservice.ListNexusEndpointsRequest, ...grpc.CallOption) (*operatorservice.ListNexusEndpointsResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return &operatorservice.ListNexusEndpointsResponse{Endpoints: append([]*nexuspb.Endpoint(nil), a.items...)}, nil
}

func (a *endpointAPI) DeleteNexusEndpoint(_ context.Context, req *operatorservice.DeleteNexusEndpointRequest, _ ...grpc.CallOption) (*operatorservice.DeleteNexusEndpointResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i, e := range a.items {
		if e.GetId() != req.GetId() {
			continue
		}

		if e.GetVersion() != req.GetVersion() {
			return nil, status.Error(codes.FailedPrecondition, "version")
		}

		if a.deleteErr != nil {
			return nil, a.deleteErr
		}

		a.deleted = append(a.deleted, e.GetSpec().GetName())
		a.items = append(a.items[:i], a.items[i+1:]...)

		return &operatorservice.DeleteNexusEndpointResponse{}, nil
	}

	return nil, status.Error(codes.NotFound, "gone")
}

type endpointClient struct {
	client.Client
	api *endpointAPI
}

//nolint:ireturn // Temporal client interface
func (c endpointClient) OperatorService() operatorservice.OperatorServiceClient { return c.api }

type verifiedCatalog struct {
	*registry.Hub
	absent bool
	err    error
}

func (v *verifiedCatalog) Absent(context.Context, string) (bool, error) { return v.absent, v.err }

func endpointOf(name, ns, queue string) *nexuspb.Endpoint {
	return &nexuspb.Endpoint{Id: name, Version: 1, Spec: &nexuspb.EndpointSpec{
		Name:   name,
		Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{Worker: &nexuspb.EndpointTarget_Worker{Namespace: ns, TaskQueue: queue}}},
	}}
}

func cleaner(t *testing.T, src registry.Source, api *endpointAPI, grace time.Duration) *executor.Executor {
	t.Helper()
	tree := backplanetest.New(t)

	return executor.New(tree.Root(), nil, src, executor.AbsenceGrace(grace), executor.Settle(0),
		executor.WithTemporal(func() (client.Client, error) { return endpointClient{api: api}, nil }, "backplane"))
}

func TestRetireEndpointsAcrossReplicas(t *testing.T) {
	t.Parallel()

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{"unhealthy": {Instances: []registry.Instance{{ID: "one", Registered: true}}}})

	api := &endpointAPI{items: []*nexuspb.Endpoint{
		endpointOf("gone", "default", "backplane"), endpointOf("unhealthy", "default", "backplane"),
		endpointOf("foreign", "other", "backplane"), endpointOf("otherqueue", "default", "someone-else"),
	}}
	a, b := cleaner(t, hub, api, 0), cleaner(t, hub, api, 0)

	var group sync.WaitGroup
	for _, x := range []*executor.Executor{a, b} {
		group.Go(func() {
			if err := x.Sync(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}

	group.Wait()

	if len(api.deleted) != 1 || api.deleted[0] != "gone" {
		t.Fatalf("deleted: %v", api.deleted)
	}
}

func TestRetirementGraceAndFreshDiscovery(t *testing.T) {
	t.Parallel()

	src := &verifiedCatalog{Hub: registry.NewHub(), absent: true}
	api := &endpointAPI{items: []*nexuspb.Endpoint{endpointOf("gone", "default", "backplane")}}

	x := cleaner(t, src, api, time.Hour)
	if err := x.Sync(t.Context()); !errors.Is(err, executor.ErrNotSynced) {
		t.Fatalf("unsynced: %v", err)
	}

	src.Publish(map[string]registry.Service{})

	if err := x.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(api.deleted) != 0 {
		t.Fatal("deleted during grace")
	}

	x = cleaner(t, src, api, 0)

	src.err = errors.New("consul offline")
	if err := x.Sync(t.Context()); err == nil || len(api.deleted) != 0 {
		t.Fatalf("outage cleanup: %v", err)
	}

	src.err, src.absent = nil, false
	if err := x.Sync(t.Context()); err != nil || len(api.deleted) != 0 {
		t.Fatalf("stale snapshot cleanup: %v", err)
	}

	src.absent = true
	api.deleteErr = status.Error(codes.FailedPrecondition, "changed by another replica")

	if err := x.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	api.deleteErr = nil

	if err := x.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(api.deleted) != 1 {
		t.Fatalf("deleted: %v", api.deleted)
	}
}
