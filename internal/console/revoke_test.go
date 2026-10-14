package console_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/console"
)

type failingSessions struct {
	console.Sessions
	read  atomic.Bool
	write atomic.Bool
}

var errSessionStorage = errors.New("session storage unavailable")

func (s *failingSessions) ByToken(ctx context.Context, token []byte) (console.Session, error) {
	if s.read.Load() {
		return console.Session{}, errSessionStorage
	}

	return s.Sessions.ByToken(ctx, token)
}

func (s *failingSessions) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	if s.write.Load() {
		return false, errSessionStorage
	}

	return s.Sessions.Delete(ctx, id)
}

func TestLogoutStorageFailureRetainsSession(t *testing.T) {
	t.Parallel()

	sessions := &failingSessions{Sessions: newMemSessions()}
	e := newEnv(t, sessions)
	cookie := e.mustLogin()
	connection := e.mustDial(cookie)

	for _, failure := range []*atomic.Bool{&sessions.read, &sessions.write} {
		failure.Store(true)

		res := e.request(http.MethodPost, "/auth/logout", nil, "Cookie", cookieHeader(cookie))
		if res.StatusCode != http.StatusServiceUnavailable || len(res.Cookies()) != 0 {
			t.Fatal("logout falsely reported revocation", res.StatusCode, res.Cookies())
		}

		failure.Store(false)

		var catalog consolev1.ListServicesResponse
		if err := call(t.Context(), connection, consolev1.CatalogService_ListServices_FullMethodName,
			&consolev1.ListServicesRequest{}, &catalog); err != nil {
			t.Fatal("failed revocation closed connection", err)
		}
	}

	res := e.request(http.MethodPost, "/auth/logout", nil, "Cookie", cookieHeader(cookie))
	if res.StatusCode != http.StatusNoContent {
		t.Fatal(res.StatusCode)
	}
}

func TestRevokeSessionStorageFailure(t *testing.T) {
	t.Parallel()

	sessions := &failingSessions{Sessions: newMemSessions()}
	e := newEnv(t, sessions)
	connection := e.mustDial(e.mustLogin())

	var listed consolev1.ListSessionsResponse
	if err := call(t.Context(), connection, consolev1.SessionService_ListSessions_FullMethodName,
		&consolev1.ListSessionsRequest{}, &listed); err != nil {
		t.Fatal(err)
	}

	sessions.write.Store(true)

	var response consolev1.RevokeSessionResponse

	err := call(t.Context(), connection, consolev1.SessionService_RevokeSession_FullMethodName,
		&consolev1.RevokeSessionRequest{Id: listed.GetSessions()[0].GetId()}, &response)
	if status.Code(err) != codes.Unavailable {
		t.Fatal("revoke hid storage error", err)
	}
}
