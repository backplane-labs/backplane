package console //nolint:testpackage // validates shared cache and upstream transport without listeners

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
)

type apiTransport func(*http.Request) (*http.Response, error)

func (f apiTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type apiSource struct {
	registry.Source
	catalog registry.Catalog
}

func (s apiSource) Current() registry.Catalog { return s.catalog }

type apiSessions struct {
	Sessions
	session Session
	revoked bool
}

func (s *apiSessions) ByToken(context.Context, []byte) (Session, error) {
	if s.revoked {
		return Session{}, ErrNoSession
	}

	return s.session, nil
}

func TestAPIFileProxy(t *testing.T) {
	t.Parallel()

	hash := strings.Repeat("a", 64)
	now := time.Now()
	sessions := &apiSessions{session: Session{ID: uuid.New(), CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}}
	old := &backplanev1.Manifest{Version: "1.0.0", Apis: []*backplanev1.API{{Schema: &backplanev1.API_Bundle{Bundle: &backplanev1.APISchemaBundle{Hash: hash}}}}}

	svc := registry.Service{Name: "hello", Manifests: map[string]*backplanev1.Manifest{"1.0.0": old, "2.0.0": {Version: "2.0.0"}}}
	for _, version := range []string{"1.0.0", "2.0.0"} {
		svc.Instances = append(svc.Instances, registry.Instance{Registered: true, Healthy: true, State: &backplanev1.InstanceState{Version: version, Phase: backplanev1.InstancePhase_INSTANCE_PHASE_SERVING, Address: version + ".example", PlatformPort: 9000}})
	}

	c := &Console{sessions: sessions, now: func() time.Time { return now }, src: apiSource{catalog: registry.Catalog{Services: map[string]registry.Service{"hello": svc}}}, bundles: newBundles("relay-secret", defaultCacheBytes)}
	hits := 0

	c.bundles.store(bundleFile{key: "hello/" + hash + "/api.yaml", body: []byte("UI cache entry")})
	c.bundles.client.Transport = apiTransport(func(r *http.Request) (*http.Response, error) {
		hits++

		if r.URL.Host != "1.0.0.example:9000" || r.URL.Path != "/_backplane/api/"+hash+"/api.yaml" || r.Header.Get(SecretHTTPHeader) != "relay-secret" {
			t.Fatalf("wrong upstream or secret: %v", r)
		}

		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"` + hash + `"`}}, Body: io.NopCloser(strings.NewReader("openapi: 3.0.3"))}, nil
	})

	get := func(file string, login bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/schemas/hello/"+hash+"/"+file, http.NoBody)
		r.SetPathValue("service", "hello")
		r.SetPathValue("hash", hash)
		r.SetPathValue("path", file)

		if login {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: "operator"})
		}

		w := httptest.NewRecorder()
		c.apiFile(w, r)

		return w
	}
	if w := get("api.yaml", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}

	for range 2 {
		w := get("api.yaml", true)
		if w.Code != http.StatusOK || w.Body.String() != "openapi: 3.0.3" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("file: %d %q %v", w.Code, w.Body.String(), w.Header())
		}
	}

	if hits != 1 {
		t.Fatalf("cache: %d upstream requests", hits)
	}

	sessions.revoked = true

	if w := get("api.yaml", true); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked cached request: %d", w.Code)
	}

	sessions.revoked = false

	if w := get("../secret.yaml", true); w.Code != http.StatusNotFound {
		t.Fatalf("traversal: %d", w.Code)
	}
}
