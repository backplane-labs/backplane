package console_test

import (
	"net/http"
	"testing"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/obs"
)

func TestObsCookieBoundary(t *testing.T) {
	t.Parallel()

	svc, err := obs.New(obs.Config{LogsURL: "http://not-contacted.invalid"})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(svc.Close)

	e := newEnvOptions(t, nil, []console.Option{console.WithServices(svc.Register)})
	if _, err = e.dial(nil, e.origin()); err == nil {
		t.Fatal("anonymous telemetry read connection accepted")
	}

	cookie := e.mustLogin()
	connection := e.mustDial(cookie)
	response := &consolev1.GetObsCapabilitiesResponse{}

	err = call(t.Context(), connection, consolev1.ObsService_GetObsCapabilities_FullMethodName, &consolev1.GetObsCapabilitiesRequest{}, response)
	if err != nil || len(response.GetSignals()) != 1 {
		t.Fatal(response, err)
	}

	logout := e.request(http.MethodPost, "/auth/logout", nil, "Cookie", cookieHeader(cookie))
	if logout.StatusCode != http.StatusNoContent {
		t.Fatal(logout.Status)
	}

	if _, err = e.dial(cookie, e.origin()); err == nil {
		t.Fatal("revoked cookie telemetry read connection accepted")
	}
}
