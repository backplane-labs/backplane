package api_test

import (
	"bytes"
	"errors"
	"testing"
	"testing/fstest"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/backplane/pkg/backplane/api"
	"github.com/gopherex/backplane/pkg/backplane/internal/apifiles"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
)

func TestLargeAPIStaysOutsideManifest(t *testing.T) {
	t.Parallel()

	files := fstest.MapFS{"api.yaml": {Data: bytes.Repeat([]byte("x"), manifest.MaxSize+1)}}
	declaration := api.HTTP("/api/", api.OpenAPIFS(files, "api.yaml"))

	r, err := link.APIs.Decl(declaration)
	if err != nil {
		t.Fatal(err)
	}

	builder := manifest.New("hello", "1.0.0")
	builder.API(r)

	m, err := builder.Build()
	if err != nil || proto.Size(m) > 1024 || r.GetBundle().GetHash() == "" {
		t.Fatalf("manifest: %v %v", m, err)
	}

	if link.APIs.Files(declaration) == nil {
		t.Fatal("file snapshot not attached")
	}
}

func TestAPIFileDeclarationErrors(t *testing.T) {
	t.Parallel()

	d := api.HTTP("/api/", api.OpenAPIFS(fstest.MapFS{}, "missing.yaml"))
	if _, err := link.APIs.Decl(d); !errors.Is(err, apifiles.ErrBundle) {
		t.Fatalf("missing entry: %v", err)
	}

	d = api.HTTP("/api/", api.OpenAPIFS(fstest.MapFS{}, "missing.yaml"), api.OpenAPI([]byte("{}")))
	if r, err := link.APIs.Decl(d); err != nil || r.GetBundle() != nil || link.APIs.Files(d) != nil {
		t.Fatalf("last option did not replace files: %v %v", r, err)
	}
}
