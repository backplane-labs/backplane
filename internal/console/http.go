package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gopherex/xlog"
)

const (
	// CookieName is the session cookie.
	CookieName = "bp_session"
	// maxLoginBody bounds the login request.
	maxLoginBody = 4 << 10

	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; " +
		"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	hsts = "max-age=31536000"
)

var (
	errUnauthenticated = errors.New("console: no valid session")
	errOrigin          = errors.New("console: origin not allowed")
)

// routes is the console's mux under its base, wrapped with the security
// headers.
func (c *Console) routes(ws http.Handler, shell fs.FS, telemetry, sessionTelemetry http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", c.login)
	mux.HandleFunc("POST /auth/logout", c.logout)
	mux.HandleFunc("GET /auth/session", c.current)
	mux.Handle("GET /ws", c.upgrade(ws))
	mux.HandleFunc("GET /plugins/{service}/{hash}/{path...}", c.plugin)
	mux.HandleFunc("GET /schemas/{service}/{hash}/{path...}", c.apiFile)

	if sessionTelemetry != nil {
		logs := http.StripPrefix("/auth/telemetry", sessionTelemetry)

		mux.HandleFunc("POST /auth/telemetry/v1/logs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")

			if c.crossSite(r) {
				httpError(w, http.StatusForbidden, errOrigin.Error())
				return
			}

			if _, ok := c.authorized(w, r); ok {
				logs.ServeHTTP(w, r)
			}
		})
	}

	if telemetry != nil {
		admission := http.StripPrefix("/telemetry", telemetry)
		mux.Handle("POST /telemetry/", admission)
		mux.Handle("OPTIONS /telemetry/", admission)
		mux.Handle("GET /telemetry/", admission)
	}

	mux.Handle("GET /", shellHandler(shell))

	h := secure(mux, !c.settings.InsecureCookie)
	if c.base != "" {
		return http.StripPrefix(c.base, h)
	}

	return h
}

// secure sets the security headers of every response (§13: CSP without
// inline scripts).
func secure(next http.Handler, tls bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		if tls {
			h.Set("Strict-Transport-Security", hsts)
		}

		next.ServeHTTP(w, r)
	})
}

// originAllowed: the browser's Origin is one of Settings.Origins, or —
// without them — the request's own host. A missing Origin is not allowed.
func (c *Console) originAllowed(r *http.Request) bool {
	origin := strings.ToLower(r.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		return false
	}

	if len(c.origins) > 0 {
		return slices.Contains(c.origins, origin)
	}

	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}

	return strings.EqualFold(u.Host, r.Host) || strings.EqualFold(u.Host, forwardedAuthority(r))
}

// forwardedAuthority is the request's host with the port the client
// connected to, when a proxy stripped the port from Host and passed it in
// X-Forwarded-Port — a reverse proxy does both (§6), so a console on a
// non-default port (localhost:10000) still recognizes its own origin.
// Empty when Host has a port or X-Forwarded-Port is absent.
func forwardedAuthority(r *http.Request) string {
	port := r.Header.Get("X-Forwarded-Port")
	if port == "" {
		return ""
	}

	if _, _, err := net.SplitHostPort(r.Host); err == nil {
		return ""
	}

	return net.JoinHostPort(strings.Trim(r.Host, "[]"), port)
}

// crossSite rejects a state-changing request from another site: an Origin
// that is not allowed, or none with Sec-Fetch-Site other than same-origin
// (a non-browser client sends neither).
func (c *Console) crossSite(r *http.Request) bool {
	if r.Header.Get("Origin") != "" {
		return !c.originAllowed(r)
	}

	site := r.Header.Get("Sec-Fetch-Site")

	return site != "" && site != "same-origin" && site != "none"
}

// cookie is the session cookie for token, or a deleting one when token is
// empty.
func (c *Console) cookie(token string, expires time.Time) *http.Cookie {
	cookie := &http.Cookie{
		Name: CookieName, Value: token, Path: c.Route(),
		HttpOnly: true, Secure: !c.settings.InsecureCookie, SameSite: http.SameSiteStrictMode,
	}

	if token == "" {
		cookie.MaxAge = -1
	} else {
		cookie.Expires = expires
	}

	return cookie
}

// sessionView is the JSON of a session over HTTP.
type sessionView struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	IdleTimeout int64     `json:"idle_timeout_seconds"`
}

func view(s Session) sessionView {
	return sessionView{
		ID: s.ID.String(), CreatedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt, IdleTimeout: int64(IdleTimeout / time.Second),
	}
}

type loginRequest struct {
	Token string `json:"token"`
}

// login exchanges the admin token for a session.
func (c *Console) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	if c.crossSite(r) {
		httpError(w, http.StatusForbidden, errOrigin.Error())

		return
	}

	now := c.now()
	addr := c.proxies.client(r)

	wait, err := c.attempts.Allow(r.Context(), addr)
	if err != nil {
		c.Log().Error("console: login refused", xlog.Err(err))
		httpError(w, http.StatusServiceUnavailable, "login unavailable")

		return
	}

	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		httpError(w, http.StatusTooManyRequests, "too many login attempts")

		return
	}

	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&req); err != nil || req.Token == "" {
		httpError(w, http.StatusBadRequest, `want {"token": "..."}`)

		return
	}

	if !c.token.match(req.Token) {
		c.Log().Warn("console: failed login", xlog.String("address", addr))

		if err := c.attempts.Failed(r.Context()); err != nil {
			c.Log().Error("console: failed login not counted", xlog.Err(err))
		}

		httpError(w, http.StatusUnauthorized, "wrong token")

		return
	}

	if err := c.attempts.Succeeded(r.Context()); err != nil {
		c.Log().Warn("console: login backoff not reset", xlog.Err(err))
	}

	token, err := randomToken()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "no randomness")

		return
	}

	s := Session{CreatedAt: now, ExpiresAt: now.Add(SessionTTL), LastSeenAt: now, Address: addr, UserAgent: r.UserAgent()}
	if s.ID, err = c.sessions.Create(r.Context(), sessionHash(token), s); err != nil {
		c.Log().Warn("console: login: create session", xlog.Err(err))
		httpError(w, http.StatusServiceUnavailable, "console storage unavailable")

		return
	}

	c.Log().Info("console: login", xlog.String("session", s.ID.String()), xlog.String("address", addr))
	http.SetCookie(w, c.cookie(token, s.ExpiresAt))
	writeJSON(w, http.StatusOK, view(s))
}

// logout ends the cookie's session and its connections.
func (c *Console) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	if c.crossSite(r) {
		httpError(w, http.StatusForbidden, errOrigin.Error())

		return
	}

	if ck, err := r.Cookie(CookieName); err == nil {
		s, lookupErr := c.sessions.ByToken(r.Context(), sessionHash(ck.Value))
		if lookupErr != nil && !errors.Is(lookupErr, ErrNoSession) {
			httpError(w, http.StatusServiceUnavailable, "console storage unavailable")
			return
		}

		if lookupErr == nil {
			if revokeErr := c.revoke(r.Context(), s.ID); revokeErr != nil {
				httpError(w, http.StatusServiceUnavailable, "console storage unavailable")
				return
			}
		}
	}

	http.SetCookie(w, c.cookie("", time.Time{}))
	w.WriteHeader(http.StatusNoContent)
}

// current answers the cookie's session.
func (c *Console) current(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	s, ok := c.authorized(w, r)
	if ok {
		writeJSON(w, http.StatusOK, view(s))
	}
}

// authorized authenticates r, answering 401 (or 503) itself when it fails.
func (c *Console) authorized(w http.ResponseWriter, r *http.Request) (Session, bool) {
	s, err := c.authenticate(r.Context(), r)

	switch {
	case err == nil:
		return s, true
	case errors.Is(err, errUnauthenticated):
		httpError(w, http.StatusUnauthorized, "login required")
	default:
		c.Log().Warn("console: session lookup", xlog.Err(err))
		httpError(w, http.StatusServiceUnavailable, "console storage unavailable")
	}

	return Session{}, false
}

// authenticate finds the live session of r's cookie and records the
// activity; errUnauthenticated without one.
func (c *Console) authenticate(ctx context.Context, r *http.Request) (Session, error) {
	ck, err := r.Cookie(CookieName)
	if err != nil || ck.Value == "" {
		return Session{}, errUnauthenticated
	}

	s, err := c.sessions.ByToken(ctx, sessionHash(ck.Value))
	if errors.Is(err, ErrNoSession) {
		return Session{}, errUnauthenticated
	}

	if err != nil {
		return Session{}, fmt.Errorf("console: session: %w", err)
	}

	now := c.now()
	if !alive(s, now) {
		if _, err := c.sessions.Delete(ctx, s.ID); err != nil {
			c.Log().Debug("console: delete ended session", xlog.Err(err))
		}

		return Session{}, errUnauthenticated
	}

	if now.Sub(s.LastSeenAt) >= touchEvery {
		if err := c.sessions.Touch(ctx, s.ID, now); err != nil {
			c.Log().Warn("console: touch session", xlog.Err(err))
		}

		s.LastSeenAt = now
	}

	return s, nil
}

// shellHandler serves the shell, index.html for paths that are not files
// (the shell's own routes); without one, a placeholder page.
func shellHandler(shell fs.FS) http.Handler {
	if shell == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)

				return
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!doctype html><title>backplane</title>" +
				"<p>backplane console: the shell is not built into this binary.</p>\n"))
		})
	}

	files := http.FileServerFS(shell)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if st, err := fs.Stat(shell, name); name != "" && (err != nil || st.IsDir()) {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}

		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

type errorBody struct {
	Error string `json:"error"`
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorBody{Error: msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // the client went away
}
