package manifest_test

import (
	"errors"
	"fmt"
	"io/fs"
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

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
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

// served is the file names of the descriptor set b serves for m, which
// names it by hash.
func served(t *testing.T, b *manifest.Builder, m *backplanev1.Manifest) []string {
	t.Helper()

	set, ok, err := b.DescriptorFiles()
	if err != nil || !ok || set.Hash() != m.GetDescriptorsHash() {
		t.Fatalf("descriptor files: ok %v, %v; hash %q, manifest %q", ok, err, set.Hash(), m.GetDescriptorsHash())
	}

	raw, err := fs.ReadFile(set, manifest.DescriptorFile)
	if err != nil {
		t.Fatal(err)
	}

	return files(t, raw)
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
		Services: []string{healthService},
	})
	b.Internal([]string{healthService})

	m := build(t, b)

	if r := m.GetApis()[0]; r.GetSchema() != nil || r.GetPrefix() != "/"+healthService+"/" {
		t.Fatalf("managed route: %v", r)
	}

	if got := served(t, b, m); !slices.Contains(got, "grpc/health/v1/health.proto") {
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

	if got := served(t, b, m); !slices.Contains(got, "grpc/reflection/v1/reflection.proto") {
		t.Fatalf("descriptors: %v", got)
	}
}

// givenSet is a FileDescriptorSet of service x.v1.X, which no registry
// knows, and of a copy of the health file the registry also has.
func givenSet(t *testing.T) []byte {
	t.Helper()

	raw, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		{
			Name: proto.String("x/v1/x.proto"), Package: proto.String("x.v1"),
			Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("X")}},
		},
		{Name: proto.String("grpc/health/v1/health.proto"), Package: proto.String("other")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// Descriptors a declaration gives join the served set and cover their
// services; a file the registry gives is taken once, the registry's; the
// manifest carries only the set's hash, which follows its content.
func TestGivenDescriptors(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.API(&backplanev1.API{Kind: backplanev1.ApiKind_API_KIND_GRPC, Prefix: "/x.v1.X/", Services: []string{"x.v1.X"}})
	b.GiveDescriptors(givenSet(t))
	b.Internal([]string{healthService})

	m := build(t, b)

	got := served(t, b, m)
	if !slices.Contains(got, "x/v1/x.proto") || len(got) != len(slices.Compact(slices.Sorted(slices.Values(got)))) {
		t.Fatalf("served files: %v", got)
	}

	set, _, _ := b.DescriptorFiles()
	raw, _ := fs.ReadFile(set, manifest.DescriptorFile)

	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fds); err != nil {
		t.Fatal(err)
	}

	for _, f := range fds.GetFile() {
		if f.GetName() == "grpc/health/v1/health.proto" && f.GetPackage() != "grpc.health.v1" {
			t.Fatalf("health file from the given set, not the registry: %v", f.GetPackage())
		}
	}

	// Another service changes the set and so the hash.
	other := manifest.New("svc", "1.0.0")
	other.API(&backplanev1.API{Kind: backplanev1.ApiKind_API_KIND_GRPC, Prefix: "/x.v1.X/", Services: []string{"x.v1.X"}})
	other.GiveDescriptors(givenSet(t))

	if build(t, other).GetDescriptorsHash() == m.GetDescriptorsHash() {
		t.Fatal("different sets, one hash")
	}

	// Without services there is no set.
	if _, ok, err := manifest.New("svc", "1.0.0").DescriptorFiles(); ok || err != nil {
		t.Fatalf("no services: ok %v %v", ok, err)
	}

	bad := manifest.New("svc", "1.0.0")
	bad.GiveDescriptors([]byte{0xff})

	if _, err := bad.Build(); err == nil {
		t.Fatal("undecodable given descriptors must fail the build")
	}
}

func TestDescribedServices(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		fds  []byte
		want []string
		bad  bool
	}{
		"set":  {fds: givenSet(t), want: []string{"x.v1.X"}},
		"none": {},
		"bad":  {fds: []byte{0xff}, bad: true},
	} {
		got, err := manifest.DescribedServices(c.fds)
		if (err != nil) != c.bad || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

// A manifest of many services stays small: their descriptors are served,
// not carried.
func TestManyServicesFit(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")

	var set descriptorpb.FileDescriptorSet

	for i := range 2000 {
		name := fmt.Sprintf("big%d", i)
		set.File = append(set.File, &descriptorpb.FileDescriptorProto{
			Name: proto.String(name + ".proto"), Package: proto.String(name),
			Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("S"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: proto.String(strings.Repeat("M", 400)), InputType: proto.String(".google.protobuf.Empty"), OutputType: proto.String(".google.protobuf.Empty")},
			}}},
		})
	}

	raw, err := proto.Marshal(&set)
	if err != nil {
		t.Fatal(err)
	}

	if len(raw) <= manifest.MaxSize {
		t.Fatalf("set of %d bytes does not exceed the KV limit", len(raw))
	}

	b.API(&backplanev1.API{Kind: backplanev1.ApiKind_API_KIND_GRPC, Prefix: "/big0.S/", Services: []string{"big0.S"}})
	b.GiveDescriptors(raw)

	if m := build(t, b); proto.Size(m) > manifest.MaxSize || m.GetDescriptorsHash() == "" {
		t.Fatalf("manifest of %d bytes, hash %q", proto.Size(m), m.GetDescriptorsHash())
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
		b.API(&backplanev1.API{Prefix: "/api/", Listener: "web"})
	}

	_, err := b.Build()
	if !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("expected duplicates, got %v", err)
	}

	for _, want := range []string{
		`hook "h"`, `event "e"`, `activity "a"`, `reactor "c:other.E"`,
		`route "prefix /api/ on listener \"web\""`,
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

// The same match on another listener, or a host match, is not a duplicate.
func TestRouteMatchScopedByListener(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.API(&backplanev1.API{Prefix: "/api/"})
	b.API(&backplanev1.API{Prefix: "/api/", Listener: "admin"})
	b.API(&backplanev1.API{Host: "api.example.com"})

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
