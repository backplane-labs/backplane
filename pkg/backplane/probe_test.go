package backplane_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane"
)

func TestProbeKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		args       []string
		kind       string
		probe, bad bool
	}{
		{args: []string{"svc"}},
		{args: []string{"svc", "serve"}},
		{args: []string{"svc", "probe"}, kind: "readiness", probe: true},
		{args: []string{"svc", "probe", "liveness"}, kind: "liveness", probe: true},
		{args: []string{"svc", "probe", "startup"}, kind: "startup", probe: true},
		{args: []string{"svc", "probe", "nope"}, probe: true, bad: true},
		{args: []string{"svc", "probe", "readiness", "x"}, probe: true, bad: true},
	}

	for _, c := range cases {
		kind, probe, err := backplane.ProbeKind(c.args)
		if kind != c.kind || probe != c.probe || (err != nil) != c.bad {
			t.Errorf("%v: %q %v %v", c.args, kind, probe, err)
		}
	}
}

func TestProbe(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz/readiness":
			http.Error(w, "store: down", http.StatusServiceUnavailable)
		case "/healthz/liveness":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	addr := strings.TrimPrefix(srv.URL, "http://")

	if err := backplane.Probe(t.Context(), addr, "liveness"); err != nil {
		t.Fatalf("liveness: %v", err)
	}

	err := backplane.Probe(t.Context(), addr, "readiness")
	if !errors.Is(err, backplane.ErrProbe) || !strings.Contains(err.Error(), "store: down") {
		t.Fatalf("readiness: %v", err)
	}

	if err := backplane.Probe(t.Context(), "127.0.0.1:1", "readiness"); !errors.Is(err, backplane.ErrProbe) {
		t.Fatalf("nothing listening: %v", err)
	}
}
