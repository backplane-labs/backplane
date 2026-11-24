package backplane_test

import (
	"bytes"
	"context"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
)

func TestManifestMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		args    []string
		want    bool
		wantErr bool
	}{
		{"no arguments", []string{"svc"}, false, false},
		{"another command", []string{"svc", "probe"}, false, false},
		{"manifest", []string{"svc", "manifest"}, true, false},
		{"manifest with more", []string{"svc", "manifest", "x"}, true, true},
		{"empty", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := backplane.ManifestMode(tc.args)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("ManifestMode(%v) = %v, %v", tc.args, got, err)
			}
		})
	}
}

// "<binary> manifest" prints the declared manifest on stdout, reaches no
// Consul (an unreachable address is configured) and starts nothing.
func TestManifestCommand(t *testing.T) {
	t.Setenv("BACKPLANE_CONSUL_ADDR", "127.0.0.1:1")

	var stdout bytes.Buffer

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		if !root.Describing() {
			t.Error("the manifest command is not describing")
		}

		return &testState{}, nil
	}, baseOptions("svc-manifest", backplane.Process([]string{"svc", backplane.ManifestCommand}, &stdout))...)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var m backplanev1.Manifest
	if err := protojson.Unmarshal(stdout.Bytes(), &m); err != nil {
		t.Fatalf("stdout is not a manifest: %v\n%s", err, stdout.String())
	}

	if m.GetService() != "svc-manifest" || m.GetConfig() == nil {
		t.Fatalf("manifest = %v", &m)
	}
}

// A normal open is not describing.
func TestNotDescribing(t *testing.T) {
	t.Parallel()

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		if root.Describing() {
			t.Error("a run is describing")
		}

		return &testState{}, nil
	}, baseOptions("svc-run", backplane.Process([]string{"svc"}, nil))...)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = svc.Close() })
}
