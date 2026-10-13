package manifest_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	_ "google.golang.org/grpc/reflection/grpc_reflection_v1" // links the reflection descriptors
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
)

const healthService = "grpc.health.v1.Health"

func registerHealth(r grpc.ServiceRegistrar) { hv1.RegisterHealthServer(r, health.NewServer()) }

func build(t *testing.T, b *manifest.Builder) *backplanev1.Manifest {
	t.Helper()

	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func files(t *testing.T, raw []byte) []string {
	t.Helper()

	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fds); err != nil {
		t.Fatalf("descriptors: %v", err)
	}

	names := make([]string, 0, len(fds.GetFile()))
	for _, f := range fds.GetFile() {
		names = append(names, f.GetName())
	}

	return names
}

func TestRegisterReportsAddedServices(t *testing.T) {
	t.Parallel()

	srv := grpc.NewServer()

	added, err := manifest.Register(srv, registerHealth)
	if err != nil || !slices.Equal(added, []string{healthService}) {
		t.Fatalf("added %v err %v", added, err)
	}

	added, err = manifest.Register(srv, func(grpc.ServiceRegistrar) {})
	if err != nil || len(added) != 0 {
		t.Fatalf("nothing registered: %v %v", added, err)
	}
}

// grpc.Server exits the process on a duplicate; Register reports it.
func TestRegisterDuplicateServiceIsError(t *testing.T) {
	t.Parallel()

	srv := grpc.NewServer()
	if _, err := manifest.Register(srv, registerHealth); err != nil {
		t.Fatal(err)
	}

	added, err := manifest.Register(srv, registerHealth)
	if !errors.Is(err, manifest.ErrDuplicate) || !strings.Contains(err.Error(), healthService) || len(added) != 0 {
		t.Fatalf("added %v err %v", added, err)
	}
}

// Managed gRPC routes and internal services share one descriptor set; the
// routes themselves carry no schema.
func TestSharedDescriptors(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.API(&backplanev1.API{
		Kind:     backplanev1.ApiKind_API_KIND_GRPC,
		Prefix:   "/" + healthService + "/",
		Port:     8080,
		Services: []string{healthService},
	})
	b.Internal([]string{"z.Last", healthService})

	_, err := b.Build()
	if err == nil || !strings.Contains(err.Error(), "z.Last") {
		t.Fatalf("unknown internal service must fail the build: %v", err)
	}

	b = manifest.New("svc", "1.0.0")
	b.API(&backplanev1.API{
		Kind:     backplanev1.ApiKind_API_KIND_GRPC,
		Prefix:   "/" + healthService + "/",
		Port:     8080,
		Services: []string{healthService},
	})
	b.Internal([]string{healthService})

	m := build(t, b)

	if r := m.GetApis()[0]; r.GetSchema() != nil || r.GetPrefix() != "/"+healthService+"/" {
		t.Fatalf("managed route: %v", r)
	}

	if got := files(t, m.GetDescriptors()); !slices.Contains(got, "grpc/health/v1/health.proto") {
		t.Fatalf("shared descriptors: %v", got)
	}
}

func TestInternalServicesSorted(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Internal([]string{"grpc.reflection.v1.ServerReflection"})
	b.Internal([]string{healthService})

	m := build(t, b)
	if want := []string{healthService, "grpc.reflection.v1.ServerReflection"}; !slices.Equal(
		m.GetInternalServices(), want) {
		t.Fatalf("internal services %v, want %v", m.GetInternalServices(), want)
	}

	if got := files(t, m.GetDescriptors()); !slices.Contains(got, "grpc/reflection/v1/reflection.proto") {
		t.Fatalf("descriptors: %v", got)
	}
}

// A declarative route's own schema is kept and adds nothing to the shared
// descriptors.
func TestDeclarativeRouteKeepsSchema(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.API(&backplanev1.API{
		Kind:     backplanev1.ApiKind_API_KIND_GRPC,
		Prefix:   "/x.v1.X/",
		Services: []string{"x.v1.X"},
		Schema:   &backplanev1.API_Descriptors{Descriptors: []byte{1}},
	})

	m := build(t, b)
	if len(m.GetDescriptors()) != 0 || len(m.GetApis()[0].GetDescriptors()) != 1 {
		t.Fatalf("manifest: %v", m)
	}
}

func TestDuplicatesListed(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	for range 2 {
		b.Hook(&backplanev1.Hook{Name: "h"})
		b.Event(&backplanev1.Event{Name: "e"})
		b.Activity(&backplanev1.Activity{Name: "a"})
		b.Subscription(&backplanev1.Subscription{Event: "other.E", Consumer: "c:other.E"})
		b.API(&backplanev1.API{Prefix: "/api/", Port: 80})
	}

	_, err := b.Build()
	if !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("expected duplicates, got %v", err)
	}

	for _, want := range []string{
		`hook "h"`, `event "e"`, `activity "a"`, `reactor "c:other.E"`,
		`route "prefix /api/ on port 80"`,
	} {
		if !strings.Contains(err.Error(), want+" declared twice") {
			t.Errorf("missing %s in:\n%v", want, err)
		}
	}
}

func TestWorkflowRecorded(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Workflow(&backplanev1.Workflow{Name: "Ship", Description: "ships an order"})

	got := build(t, b).GetWorkflows()
	if len(got) != 1 || got[0].GetName() != "Ship" || got[0].GetDescription() != "ships an order" {
		t.Fatalf("workflows: %v", got)
	}

	b.Workflow(&backplanev1.Workflow{Name: "Ship"})

	if _, err := b.Build(); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("duplicate workflow: %v", err)
	}
}

// The same match on another port, or a host match, is not a duplicate.
func TestRouteMatchScopedByPort(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.API(&backplanev1.API{Prefix: "/api/", Port: 80})
	b.API(&backplanev1.API{Prefix: "/api/", Port: 81})
	b.API(&backplanev1.API{Host: "api.example.com", Port: 80})

	if m := build(t, b); len(m.GetApis()) != 3 {
		t.Fatalf("routes: %v", m.GetApis())
	}
}

func TestSealPanics(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Hook(&backplanev1.Hook{Name: "before"})
	b.Seal()

	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "event declared after the service started") {
			t.Fatalf("panic: %v", r)
		}

		if m := build(t, b); len(m.GetHooks()) != 1 || len(m.GetEvents()) != 0 {
			t.Fatalf("sealed manifest changed: %v", m)
		}
	}()

	b.Event(&backplanev1.Event{Name: "late"})
}

func TestTooLarge(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Hook(&backplanev1.Hook{Name: strings.Repeat("x", manifest.MaxSize)})

	if _, err := b.Build(); !errors.Is(err, manifest.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestUI(t *testing.T) {
	t.Parallel()

	bundle := fstest.MapFS{
		"plugin.json":    {Data: []byte(`{"sdk_major": 2}`)},
		"remoteEntry.js": {Data: []byte("x")},
	}

	b := manifest.New("svc", "1.0.0")
	b.UI(bundle)

	m := build(t, b)
	if m.GetUi().GetSdkMajor() != 2 || len(m.GetUi().GetHash()) != 64 {
		t.Fatalf("ui: %v", m.GetUi())
	}

	b.UI(bundle)

	if _, err := b.Build(); !errors.Is(err, manifest.ErrDuplicate) || !strings.Contains(err.Error(), "ui declared twice") {
		t.Fatalf("second ui: %v", err)
	}
}

func TestUIWithoutPluginJSON(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.UI(fstest.MapFS{"remoteEntry.js": {Data: []byte("x")}})

	if _, err := b.Build(); err == nil || !strings.Contains(err.Error(), "plugin.json") {
		t.Fatalf("want plugin.json error, got %v", err)
	}
}

func TestFailKept(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	first := errors.New("first")
	b.Fail(first)
	b.Internal([]string{"does.not.Exist"})

	if _, err := b.Build(); !errors.Is(err, first) {
		t.Fatalf("want first, got %v", err)
	}
}

func TestUnknownServiceFails(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.API(&backplanev1.API{
		Prefix: "/does.not.Exist/", Services: []string{"does.not.Exist"},
	})

	if _, err := b.Build(); err == nil || !strings.Contains(err.Error(), "does.not.Exist") {
		t.Fatalf("expected error, got %v", err)
	}
}

// Build returns a copy: changing it does not change the next build.
func TestBuildReturnsCopy(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Hook(&backplanev1.Hook{Name: "h"})

	m := build(t, b)
	m.Hooks = nil

	if m2 := build(t, b); len(m2.GetHooks()) != 1 || m2.GetService() != "svc" || m2.GetVersion() != "1.0.0" {
		t.Fatalf("second build: %v", m2)
	}
}
