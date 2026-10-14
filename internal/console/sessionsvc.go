package console

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// sessionService serves SessionService.
type sessionService struct {
	consolev1.UnimplementedSessionServiceServer

	c *Console
}

// current is the session of the calling connection.
func current(ctx context.Context) (uuid.UUID, error) {
	cn := connOf(ctx)
	if cn == nil {
		return uuid.Nil, status.Errorf(codes.Unauthenticated, "no session")
	}

	return cn.session, nil
}

// ListSessions implements SessionService.
func (s sessionService) ListSessions(
	ctx context.Context, _ *consolev1.ListSessionsRequest,
) (*consolev1.ListSessionsResponse, error) {
	self, err := current(ctx)
	if err != nil {
		return nil, err
	}

	list, err := s.c.sessions.List(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	now := s.c.now()
	res := &consolev1.ListSessionsResponse{}

	for _, x := range list {
		if !alive(x, now) {
			continue
		}

		res.Sessions = append(res.Sessions, &consolev1.Session{
			Id: x.ID.String(), CreatedAt: timestamppb.New(x.CreatedAt), ExpiresAt: timestamppb.New(x.ExpiresAt),
			LastSeenAt: timestamppb.New(x.LastSeenAt), Address: x.Address, UserAgent: x.UserAgent, Current: x.ID == self,
		})
	}

	return res, nil
}

// RevokeSession implements SessionService.
func (s sessionService) RevokeSession(
	ctx context.Context, req *consolev1.RevokeSessionRequest,
) (*consolev1.RevokeSessionResponse, error) {
	if _, err := current(ctx); err != nil {
		return nil, err
	}

	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "session id %q: %v", req.GetId(), err)
	}

	if _, err := s.c.sessions.Get(ctx, id); errors.Is(err, ErrNoSession) {
		return nil, status.Errorf(codes.NotFound, "no session %s", id)
	} else if err != nil {
		return nil, status.Error(codes.Unavailable, "console storage unavailable") //nolint:wrapcheck // RPC status
	}

	if err := s.c.revoke(ctx, id); err != nil {
		return nil, status.Error(codes.Unavailable, "console storage unavailable") //nolint:wrapcheck // RPC status
	}

	s.c.Log().Info("console: session revoked", xlog.String("session", id.String()))

	return &consolev1.RevokeSessionResponse{}, nil
}

// RevokeOtherSessions implements SessionService.
func (s sessionService) RevokeOtherSessions(
	ctx context.Context, _ *consolev1.RevokeOtherSessionsRequest,
) (*consolev1.RevokeOtherSessionsResponse, error) {
	self, err := current(ctx)
	if err != nil {
		return nil, err
	}

	revoked, err := s.c.sessions.DeleteOthers(ctx, self)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	s.c.conns.close(uuid.Nil, self)
	s.c.Log().Warn("console: other sessions revoked", xlog.Int64("revoked_sessions", revoked))

	return &consolev1.RevokeOtherSessionsResponse{RevokedSessions: uint32(revoked)}, nil //nolint:gosec // a count
}
