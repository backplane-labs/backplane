package backplanetest

import (
	"context"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/mtls"
)

// Caller is ctx as a request from caller over mutual TLS carries it:
// backplane.CallerOf reports caller, so a test calls a handler served with
// api.MTLS directly, or over bufconn with Caller in a server interceptor.
//
//	ctx := backplanetest.Caller(t.Context(), backplane.Caller{Service: "sync"})
//	_, err := rooms.Join(ctx, req) // authorizes caller sync
func Caller(ctx context.Context, caller backplane.Caller) context.Context {
	return mtls.WithPeer(ctx, mtls.Peer(caller))
}
