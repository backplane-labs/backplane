package console_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// The admin token is configuration: login compares against it, and a
// console without one (or with a short one) does not start.
func TestAdminTokenSettings(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		token string
		want  error
	}{
		"configured": {adminToken, nil},
		"missing":    {"", console.ErrNoAdminToken},
		"short":      {"short", console.ErrShortAdminToken},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := console.Settings{AdminToken: config.Secret(tc.token)}.Validate()
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}

	e := newEnv(t, nil, func(s *console.Settings) { s.AdminToken = "another-admin-token-42" })
	if res, c := e.login("another-admin-token-42"); res.StatusCode != http.StatusOK || c == nil {
		t.Fatalf("configured token: %s", res.Status)
	}

	for _, wrong := range []string{adminToken, "another-admin-token-4", "another-admin-token-42 "} {
		if res, c := e.login(wrong); res.StatusCode != http.StatusUnauthorized || c != nil {
			t.Fatalf("wrong token %q: %s", wrong, res.Status)
		}
	}
}

func TestLoginFlow(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil)

	if res, c := e.login("wrong"); res.StatusCode != http.StatusUnauthorized || c != nil {
		t.Fatalf("wrong token: %s", res.Status)
	}

	if res := e.request(http.MethodPost, "/auth/login", "not an object"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body: %s", res.Status)
	}

	if res, _ := e.login(adminToken, "Origin", "https://evil.example"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin login: %s", res.Status)
	}

	login, c := e.login(adminToken, "Origin", e.origin())
	if login.StatusCode != http.StatusOK || c == nil {
		t.Fatalf("login: %s", login.Status)
	}

	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie flags: %+v", c)
	}

	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Strict-Transport-Security"} {
		if login.Header.Get(h) == "" {
			t.Fatalf("no %s", h)
		}
	}

	if login.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control %q", login.Header.Get("Cache-Control"))
	}

	if res := e.request(http.MethodGet, "/auth/session", nil, "Cookie", cookieHeader(c)); res.StatusCode != http.StatusOK {
		t.Fatalf("session: %s", res.Status)
	}

	if res := e.request(http.MethodGet, "/auth/session", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session without cookie: %s", res.Status)
	}

	if res := e.request(http.MethodPost, "/auth/logout", nil, "Cookie", cookieHeader(c)); res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %s", res.Status)
	}

	if res := e.request(http.MethodGet, "/auth/session", nil, "Cookie", cookieHeader(c)); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout: %s", res.Status)
	}
}

func TestInsecureCookieAndPrefix(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil, func(s *console.Settings) { s.InsecureCookie, s.Prefix = true, "/bp" })

	res := e.request(http.MethodPost, "/bp/auth/login", map[string]string{"token": adminToken})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login under the prefix: %s", res.Status)
	}

	c := res.Cookies()[0]
	if c.Secure || c.Path != "/bp/" || res.Header.Get("Strict-Transport-Security") != "" {
		t.Fatalf("insecure cookie: %+v, hsts %q", c, res.Header.Get("Strict-Transport-Security"))
	}

	if e.console.Route() != "/bp/" {
		t.Fatalf("route %q", e.console.Route())
	}
}

func TestSessionExpiry(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil)
	session := func(c *http.Cookie) int {
		return e.request(http.MethodGet, "/auth/session", nil, "Cookie", cookieHeader(c)).StatusCode
	}

	// Idle: activity keeps it, an hour without ends it.
	idle := e.mustLogin()

	e.clock.Advance(console.IdleTimeout - time.Minute)

	if code := session(idle); code != http.StatusOK {
		t.Fatalf("active within the idle timeout: %d", code)
	}

	e.clock.Advance(console.IdleTimeout - time.Minute)

	if code := session(idle); code != http.StatusOK {
		t.Fatalf("activity extends it: %d", code)
	}

	e.clock.Advance(console.IdleTimeout)

	if code := session(idle); code != http.StatusUnauthorized {
		t.Fatalf("idle for the timeout: %d", code)
	}

	// Absolute: activity does not extend it past its lifetime.
	abs := e.mustLogin()
	for elapsed := time.Duration(0); elapsed < console.SessionTTL-console.IdleTimeout; elapsed += console.IdleTimeout / 2 {
		e.clock.Advance(console.IdleTimeout / 2)

		if code := session(abs); code != http.StatusOK {
			t.Fatalf("active session at %v: %d", elapsed, code)
		}
	}

	e.clock.Advance(console.IdleTimeout)

	if code := session(abs); code != http.StatusUnauthorized {
		t.Fatalf("past the lifetime: %d", code)
	}
}

func TestLoginAttempts(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil, func(s *console.Settings) { s.TrustedProxies = []string{"127.0.0.1"} })
	from := func(addr, token string) *http.Response {
		res, _ := e.login(token, "X-Forwarded-For", addr)

		return res
	}

	// A wrong token counts a failure of the client's address.
	if res := from("198.51.100.1", "wrong"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %s", res.Status)
	}

	// Refused: 429 with the wait rounded up to seconds.
	e.attempts.set(1500*time.Millisecond, nil)

	limited := from("198.51.100.1", adminToken)
	if limited.StatusCode != http.StatusTooManyRequests || limited.Header.Get("Retry-After") != "2" {
		t.Fatalf("limited: %s, retry-after %q", limited.Status, limited.Header.Get("Retry-After"))
	}

	// Attempts that cannot be counted refuse the login.
	e.attempts.set(0, errors.New("login attempt store down"))

	if res := from("198.51.100.1", adminToken); res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts unavailable: %s", res.Status)
	}

	e.attempts.set(0, nil)

	if res := from("198.51.100.2", adminToken); res.StatusCode != http.StatusOK {
		t.Fatalf("login: %s", res.Status)
	}

	addrs, failed, succeeded := e.attempts.counts()
	if !slices.Equal(addrs, []string{"198.51.100.1", "198.51.100.1", "198.51.100.1", "198.51.100.2"}) || failed != 1 || succeeded != 1 {
		t.Fatalf("addrs %v, failed %d, succeeded %d", addrs, failed, succeeded)
	}
}

// X-Forwarded-For counts only through trusted proxies.
func TestClientAddress(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		trusted []string
		xff     string
		want    string
	}{
		"untrusted peer":       {nil, "198.51.100.7", "127.0.0.1"},
		"trusted peer":         {[]string{"127.0.0.0/8"}, "198.51.100.7", "198.51.100.7"},
		"chain of proxies":     {[]string{"127.0.0.1", "10.0.0.0/8"}, "198.51.100.7, 10.1.2.3", "198.51.100.7"},
		"spoofed left entries": {[]string{"127.0.0.1"}, "1.1.1.1, 198.51.100.7", "198.51.100.7"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sessions := newMemSessions()
			e := newEnv(t, sessions, func(s *console.Settings) { s.TrustedProxies = tc.trusted })

			if res, _ := e.login(adminToken, "X-Forwarded-For", tc.xff); res.StatusCode != http.StatusOK {
				t.Fatalf("login: %s", res.Status)
			}

			list, _ := sessions.List(t.Context())
			if len(list) != 1 || list[0].Address != tc.want {
				t.Fatalf("address: %+v, want %s", list, tc.want)
			}
		})
	}
}
