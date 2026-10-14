package backplane_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
)

// uiGet fetches path of the UI bundle with the internal secret and, when
// given, If-None-Match.
func uiGet(t *testing.T, p ports, path, ifNoneMatch string) (*http.Response, string) {
	t.Helper()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, p.platformURL()+"/_backplane/ui/"+path, http.NoBody)
	req.Header.Set(guard.HTTPHeader, secret)

	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	return res, string(body)
}

// TestUIBundleCaching: every file of the bundle carries ETag = ui.hash and
// Cache-Control: no-cache; a matching If-None-Match gets 304, a missing file
// a plain 404 without validators.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestUIBundleCaching(t *testing.T) {
	p := setup(t, "")

	bundle := fstest.MapFS{
		"plugin.json":         {Data: []byte(`{"sdk_major":1}`)},
		"assets/remote.js":    {Data: []byte(`export default 1`)},
		"mf-manifest.json":    {Data: []byte(`{}`)},
		"assets/nested/a.css": {Data: []byte(`body{}`)},
	}

	hash, err := manifest.UIHash(bundle)
	if err != nil {
		t.Fatal(err)
	}

	etag := `"` + hash + `"`

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-ui")...)
	if err != nil {
		t.Fatal(err)
	}

	svc.UI(bundle)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	for _, path := range []string{"plugin.json", "assets/remote.js", "assets/nested/a.css"} {
		res, body := uiGet(t, p, path, "")
		if res.StatusCode != http.StatusOK || body != string(bundle[path].Data) {
			t.Fatalf("%s: %d %q", path, res.StatusCode, body)
		}

		if got := res.Header.Get("ETag"); got != etag {
			t.Fatalf("%s: ETag %q, want %q", path, got, etag)
		}

		if got := res.Header.Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s: Cache-Control %q", path, got)
		}

		if revalidated, empty := uiGet(t, p, path, etag); revalidated.StatusCode != http.StatusNotModified || empty != "" {
			t.Fatalf("%s revalidated: %d %q", path, revalidated.StatusCode, empty)
		}

		if stale, _ := uiGet(t, p, path, `"stale"`); stale.StatusCode != http.StatusOK {
			t.Fatalf("%s with a stale ETag: %d", path, stale.StatusCode)
		}
	}

	res, _ := uiGet(t, p, "missing.js", "")
	if res.StatusCode != http.StatusNotFound || res.Header.Get("ETag") != "" || res.Header.Get("Cache-Control") != "" {
		t.Fatalf("missing file: %d %v", res.StatusCode, res.Header)
	}

	// Neither mutations of existing bytes nor additions/deletions may change
	// the content already advertised by this running service.
	copy(bundle["assets/remote.js"].Data, "changed contents")
	delete(bundle, "assets/nested/a.css")
	bundle["new.js"] = &fstest.MapFile{Data: []byte("new")}

	res, body := uiGet(t, p, "assets/remote.js", "")
	if res.StatusCode != http.StatusOK || body != "export default 1" || res.Header.Get("ETag") != etag {
		t.Fatalf("snapshot changed: %d %q %v", res.StatusCode, body, res.Header)
	}

	res, body = uiGet(t, p, "assets/nested/a.css", "")
	if res.StatusCode != http.StatusOK || body != "body{}" {
		t.Fatalf("snapshot lost file: %d %q", res.StatusCode, body)
	}

	res, _ = uiGet(t, p, "new.js", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("snapshot exposed new file: %d", res.StatusCode)
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}
