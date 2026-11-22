package backplane_test

import (
	"context"
	"io"
	"os"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
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

	args := os.Args
	os.Args = []string{"svc", backplane.ManifestCommand}
	t.Cleanup(func() { os.Args = args })

	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = w
	t.Cleanup(func() { os.Stdout = stdout })

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		hook.Declare[struct{ A string }, struct{ B string }](root, "Lookup")

		return &testState{}, nil
	}, baseOptions("svc-manifest")...)
	if err != nil {
		t.Fatal(err)
	}

	runErr := svc.Run(context.Background())
	_ = w.Close()
	os.Stdout = stdout

	if runErr != nil {
		t.Fatal(runErr)
	}

	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	var m backplanev1.Manifest
	if err := protojson.Unmarshal(raw, &m); err != nil {
		t.Fatalf("stdout is not a manifest: %v\n%s", err, raw)
	}

	if m.GetService() != "svc-manifest" || len(m.GetHooks()) != 1 || m.GetHooks()[0].GetName() != "Lookup" {
		t.Fatalf("manifest = %v", &m)
	}
}
