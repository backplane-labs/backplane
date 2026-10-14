package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/gopherex/ws-proto/wsrpc"
	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// revokeDelay lets the RPC that revoked its own session answer before its
// connection closes.
const revokeDelay = 200 * time.Millisecond

// newWS is the ws-proto server of /ws: backplane's own API, the relay for
// everything else, activity recorded per RPC.
func (c *Console) newWS(services []func(grpc.ServiceRegistrar)) *wsrpc.Server {
	srv := wsrpc.NewServer(
		// upgrade checks the origin itself, together with the session.
		wsrpc.WithInsecureSkipOriginCheck(),
		wsrpc.WithUnknownHandler(c.relay.handle),
		wsrpc.WithMiddleware(c.activity),
	)

	reg := wsrpc.GRPCRegistrar(srv)
	consolev1.RegisterCatalogServiceServer(reg, catalogService{c: c})
	consolev1.RegisterSessionServiceServer(reg, sessionService{c: c})

	for _, register := range services {
		register(reg)
	}

	return srv
}

// upgrade admits a WebSocket only from an allowed origin with a live
// session; the connection lives while the session does.
func (c *Console) upgrade(ws http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.originAllowed(r) {
			httpError(w, http.StatusForbidden, errOrigin.Error())

			return
		}

		s, ok := c.authorized(w, r)
		if !ok {
			return
		}

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		cur := &conn{session: s.ID, cancel: cancel}
		cur.touched.Store(s.LastSeenAt.UnixNano())

		c.conns.add(cur)
		defer c.conns.remove(cur)

		go c.watch(ctx, cur)

		ws.ServeHTTP(w, r.WithContext(context.WithValue(ctx, connKey{}, cur)))
	})
}

// activity records an RPC as activity of the connection's session, at
// most once per touchEvery, and makes the call's metadata trustworthy for
// backplane's own handlers: the browser's authorization and bp-* are
// dropped, bp-console-session names the connection's session.
func (c *Console) activity(next wsrpc.Handler) wsrpc.Handler {
	return func(ctx context.Context, s *wsrpc.Stream) error {
		if hdr := s.Header(); hdr != nil {
			// The OPEN headers: read by the handler (as incoming metadata)
			// only after this.
			for k := range hdr {
				if lk := strings.ToLower(k); lk == "authorization" || strings.HasPrefix(lk, "bp-") {
					delete(hdr, k)
				}
			}
		}

		if cur := connOf(ctx); cur != nil {
			if hdr := s.Header(); hdr != nil {
				hdr[SessionHeader] = cur.session.String()
			}

			c.touchConn(ctx, cur)
		}

		return next(ctx, s)
	}
}

// touchConn records activity of the connection's session, at most once
// per touchEvery.
func (c *Console) touchConn(ctx context.Context, cur *conn) {
	now := c.now()

	last := cur.touched.Load()
	if now.Sub(time.Unix(0, last)) < touchEvery || !cur.touched.CompareAndSwap(last, now.UnixNano()) {
		return
	}

	if err := c.sessions.Touch(ctx, cur.session, now); err != nil {
		c.Log().Warn("console: touch session", xlog.Err(err))
	}
}

// watch closes the connection once its session is gone, expired or idle,
// re-reading it every recheckEvery (a revocation on another replica).
func (c *Console) watch(ctx context.Context, cur *conn) {
	t := time.NewTicker(recheckEvery)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		s, err := c.sessions.Get(ctx, cur.session)

		switch {
		case errors.Is(err, ErrNoSession), err == nil && !alive(s, c.now()):
			cur.cancel()

			return
		case err != nil && ctx.Err() == nil:
			c.Log().Debug("console: recheck session", xlog.Err(err))
		}
	}
}

// revoke deletes a session and closes its connections on this replica
// (others notice within recheckEvery).
func (c *Console) revoke(ctx context.Context, id uuid.UUID) error {
	_, err := c.sessions.Delete(ctx, id)
	if err != nil {
		c.Log().Warn("console: revoke session", xlog.Err(err))
		return fmt.Errorf("revoke session: %w", err)
	}

	c.conns.close(id, uuid.Nil)

	return nil
}

// conn is one open /ws connection.
type conn struct {
	session uuid.UUID
	cancel  context.CancelFunc
	touched atomic.Int64 // unix nanoseconds of the last recorded activity
}

type connKey struct{}

// SessionID is the console session a call on /ws comes from: backplane's
// own handlers read it from their context (the metadata bp-console-session
// carries it too when the call has any headers).
func SessionID(ctx context.Context) (uuid.UUID, bool) {
	if cn := connOf(ctx); cn != nil {
		return cn.session, true
	}

	return uuid.Nil, false
}

func connOf(ctx context.Context) *conn {
	cn, _ := ctx.Value(connKey{}).(*conn)

	return cn
}

// connections are the open /ws connections by session.
type connections struct {
	mu sync.Mutex
	m  map[uuid.UUID]map[*conn]struct{}
}

func newConnections() *connections { return &connections{m: map[uuid.UUID]map[*conn]struct{}{}} }

func (cs *connections) add(cur *conn) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	set, ok := cs.m[cur.session]
	if !ok {
		set = map[*conn]struct{}{}
		cs.m[cur.session] = set
	}

	set[cur] = struct{}{}
}

func (cs *connections) remove(cur *conn) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	delete(cs.m[cur.session], cur)

	if len(cs.m[cur.session]) == 0 {
		delete(cs.m, cur.session)
	}
}

// closeAll ends every connection now.
func (cs *connections) closeAll() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for _, set := range cs.m {
		for cur := range set {
			cur.cancel()
		}
	}
}

// close ends the connections of session id — or, with id Nil, of every
// session but keep. The caller's own connection closes after revokeDelay.
func (cs *connections) close(id, keep uuid.UUID) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for sid, set := range cs.m {
		if (id != uuid.Nil && sid != id) || (id == uuid.Nil && sid == keep) {
			continue
		}

		for cn := range set {
			time.AfterFunc(revokeDelay, cn.cancel)
		}
	}
}
