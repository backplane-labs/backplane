package backplane //nolint:testpackage // checks platform mux registration without opening listeners

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/apifiles"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
)

func TestSchemaFileHandler(t *testing.T) {
	t.Parallel()

	files, err := apifiles.Snapshot(fstest.MapFS{"api.yaml": {Data: []byte("root")}, "schemas/model.yaml": {Data: []byte("model")}}, "api.yaml")
	if err != nil {
		t.Fatal(err)
	}

	ref := &backplanev1.APISchemaBundle{Hash: files.Hash(), Entry: "api.yaml"}
	bundle := &routes.SchemaFiles{Files: files, Ref: ref}
	c := &core{platform: http.NewServeMux()}
	c.mountSchemaFiles(bundle)
	c.mountSchemaFiles(bundle) // two routes can share a snapshot

	get := func(file, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/_backplane/api/"+files.Hash()+"/"+file, http.NoBody)
		r.Header.Set("If-None-Match", etag)

		w := httptest.NewRecorder()
		c.platform.ServeHTTP(w, r)

		return w
	}

	w := get("schemas/model.yaml", "")
	if w.Code != http.StatusOK || w.Body.String() != "model" || w.Header().Get("ETag") != `"`+files.Hash()+`"` {
		t.Fatalf("file: %d %q %v", w.Code, w.Body.String(), w.Header())
	}

	if revalidated := get("schemas/model.yaml", `"`+files.Hash()+`"`); revalidated.Code != http.StatusNotModified {
		t.Fatalf("revalidation: %d", revalidated.Code)
	}

	if missing := get("missing.yaml", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", missing.Code)
	}
}
