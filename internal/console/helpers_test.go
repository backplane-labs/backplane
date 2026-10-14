package console_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ws-proto/wsrpc"

	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

// adminToken is the configured admin token of the tests.
const adminToken = "test-admin-token-0123"

// attempts is a settable Attempts: allows while wait and err are zero,
// counts failures and successes.
type attempts struct {
	mu                sync.Mutex
	wait              time.Duration
	err               error
	addrs             []string
	failed, succeeded int
}

func newAttempts() *attempts { return &attempts{} }

func (a *attempts) Allow(_ context.Context, addr string) (time.Duration, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.addrs = append(a.addrs, addr)

	return a.wait, a.err
}

func (a *attempts) Failed(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failed++

	return a.err
}

func (a *attempts) Succeeded(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.succeeded++

	return a.err
}

func (a *attempts) set(wait time.Duration, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.wait, a.err = wait, err
}

func (a *attempts) counts() ([]string, int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.addrs), a.failed, a.succeeded
}

// clock is a settable time.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// memSessions is Sessions in memory: the contract test holds it to PG's
// behavior.
type memSessions struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]console.Session
	tokens map[uuid.UUID]string // id -> token hash
}

var _ console.Sessions = (*memSessions)(nil)

func newMemSessions() *memSessions {
	return &memSessions{byID: map[uuid.UUID]console.Session{}, tokens: map[uuid.UUID]string{}}
}

func (m *memSessions) Create(_ context.Context, tokenHash []byte, s console.Session) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s.ID = uuid.New()
	s.LastSeenAt = s.CreatedAt
	m.byID[s.ID] = s
	m.tokens[s.ID] = string(tokenHash)

	return s.ID, nil
}

func (m *memSessions) ByToken(_ context.Context, tokenHash []byte) (console.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, h := range m.tokens {
		if h == string(tokenHash) {
			return m.byID[id], nil
		}
	}

	return console.Session{}, console.ErrNoSession
}

func (m *memSessions) Get(_ context.Context, id uuid.UUID) (console.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.byID[id]
	if !ok {
		return console.Session{}, console.ErrNoSession
	}

	return s, nil
}

func (m *memSessions) Touch(_ context.Context, id uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.byID[id]; ok && s.LastSeenAt.Before(at) {
		s.LastSeenAt = at
		m.byID[id] = s
	}

	return nil
}

func (m *memSessions) List(context.Context) ([]console.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]console.Session, 0, len(m.byID))
	for _, s := range m.byID {
		out = append(out, s)
	}

	slices.SortFunc(out, func(a, b console.Session) int { return b.CreatedAt.Compare(a.CreatedAt) })

	return out, nil
}

func (m *memSessions) Delete(_ context.Context, id uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.byID[id]
	delete(m.byID, id)
	delete(m.tokens, id)

	return ok, nil
}

func (m *memSessions) DeleteOthers(_ context.Context, keep uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var n int64

	for id := range m.byID {
		if id != keep {
			delete(m.byID, id)
			delete(m.tokens, id)

			n++
		}
	}

	return n, nil
}

func (m *memSessions) DeleteStale(_ context.Context, now, idleSince time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var n int64

	for id, s := range m.byID {
		if !s.ExpiresAt.After(now) || !s.LastSeenAt.After(idleSince) {
			delete(m.byID, id)
			delete(m.tokens, id)

			n++
		}
	}

	return n, nil
}

// env is one console under test behind an httptest server.
type env struct {
	t        *testing.T
	console  *console.Console
	sessions console.Sessions
	attempts *attempts
	hub      *registry.Hub
	clock    *clock
	srv      *httptest.Server
}

type envOption func(s *console.Settings)

// newEnv starts a console with the admin token adminToken over memory
// sessions (or the given ones).
func newEnv(t *testing.T, sessions console.Sessions, opts ...envOption) *env {
	t.Helper()
	return newEnvOptions(t, sessions, nil, opts...)
}

func newEnvOptions(t *testing.T, sessions console.Sessions, options []console.Option, opts ...envOption) *env {
	t.Helper()

	if sessions == nil {
		sessions = newMemSessions()
	}

	e := &env{t: t, sessions: sessions, attempts: newAttempts(), hub: registry.NewHub(), clock: newClock()}
	settings := console.Settings{AdminToken: adminToken, InternalSecret: "relay-secret"}

	for _, o := range opts {
		o(&settings)
	}

	h := backplanetest.New(t, backplanetest.Name("backplane"))

	options = append(options, console.WithClock(e.clock.Now))
	e.console = console.New(h.Root(), settings, sessions, e.attempts, e.hub, options...)
	h.Start()

	e.srv = httptest.NewServer(e.console.Handler())
	t.Cleanup(e.srv.Close)

	return e
}

func (e *env) url(p string) string { return e.srv.URL + p }

func (e *env) origin() string { return e.srv.URL }

// request sends method p with body (JSON when not nil) and headers as
// pairs.
func (e *env) request(method, p string, body any, header ...string) *http.Response {
	e.t.Helper()

	var payload io.Reader = http.NoBody

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}

		payload = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(e.t.Context(), method, e.url(p), payload)
	if err != nil {
		e.t.Fatal(err)
	}

	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Add(header[i], header[i+1])
	}

	res, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}

	e.t.Cleanup(func() { _ = res.Body.Close() })

	return res
}

// login posts token and returns the response and its session cookie.
func (e *env) login(token string, header ...string) (*http.Response, *http.Cookie) {
	e.t.Helper()

	res := e.request(http.MethodPost, "/auth/login", map[string]string{"token": token}, header...)
	for _, c := range res.Cookies() {
		if c.Name == console.CookieName {
			return res, c
		}
	}

	return res, nil
}

// mustLogin logs in with adminToken.
func (e *env) mustLogin() *http.Cookie {
	e.t.Helper()

	res, c := e.login(adminToken)
	if res.StatusCode != http.StatusOK || c == nil {
		e.t.Fatalf("login: %s", res.Status)
	}

	return c
}

func cookieHeader(c *http.Cookie) string { return c.Name + "=" + c.Value }

// dial opens /ws with the cookie and origin.
func (e *env) dial(c *http.Cookie, origin string) (*wsrpc.ClientConn, error) {
	e.t.Helper()

	h := http.Header{}
	if c != nil {
		h.Set("Cookie", cookieHeader(c))
	}

	if origin != "" {
		h.Set("Origin", origin)
	}

	cc, err := wsrpc.Dial(e.t.Context(), "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", wsrpc.WithHeader(h))
	if err == nil {
		e.t.Cleanup(func() { _ = cc.Close() })
	}

	return cc, err
}

func (e *env) mustDial(c *http.Cookie) *wsrpc.ClientConn {
	e.t.Helper()

	cc, err := e.dial(c, e.origin())
	if err != nil {
		e.t.Fatalf("dial: %v", err)
	}

	return cc
}

// call makes a unary call over ws-proto.
func call(ctx context.Context, cc *wsrpc.ClientConn, method string, req, res proto.Message) error {
	s, err := cc.NewStream(ctx, method, nil)
	if err != nil {
		return err
	}

	if err := s.Send(req); err != nil {
		return err
	}

	if err := s.CloseSend(); err != nil {
		return err
	}

	if err := s.Recv(res); err != nil {
		return err
	}

	if err := s.Recv(res); !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}
