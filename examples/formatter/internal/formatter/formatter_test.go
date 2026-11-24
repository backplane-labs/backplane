package formatter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/backplane-labs/backplane/examples/formatter/internal/formatter"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

func TestFormat(t *testing.T) {
	t.Parallel()
	cfg := backplanetest.Config[formatter.Config](t, nil)
	h := formatter.HTTP(&cfg)
	for _, tc := range []struct {
		prefix, path, text string
		status             int
	}{{"Welcome", "/?name=ann", "\"Welcome, ann!\"\n", 200}, {"Hi", "/?name=bob", "\"Hi, bob!\"\n", 200}, {"Hi", "/", "name is required\n", 400}} {
		backplanetest.SetLive(&cfg.Prefix, tc.prefix)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.status || w.Body.String() != tc.text {
			t.Errorf("%s: %d %q", tc.path, w.Code, w.Body)
		}
	}
}
