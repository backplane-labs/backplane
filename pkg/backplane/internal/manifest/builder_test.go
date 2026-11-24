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
}

func TestSealPanics(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Internal([]string{"before"})
	b.Seal()

	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "internal API declared after the service started") {
			t.Fatalf("panic: %v", r)
		}

		if m := build(t, b); !slices.Equal(m.GetInternalServices(), []string{"before"}) {
			t.Fatalf("sealed manifest changed: %v", m)
		}
	}()

	b.Internal([]string{"late"})
}

func TestTooLarge(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Internal([]string{strings.Repeat("x", manifest.MaxSize)})

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

func TestBuildReturnsCopy(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Internal([]string{"h"})

	m := build(t, b)
	m.InternalServices = nil

	if m2 := build(t, b); len(m2.GetInternalServices()) != 1 || m2.GetService() != "svc" || m2.GetVersion() != "1.0.0" {
		t.Fatalf("second build: %v", m2)
	}
}
