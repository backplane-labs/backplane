package console_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

// TestPGAttempts checks per-address windows and shared exponential backoff.
func TestPGAttempts(t *testing.T) {
	t.Parallel()
	st := pgStore(t)

	a := console.NewPGAttempts(st, console.LoginLimits{
		Burst: 2, Window: 500 * time.Millisecond,
		Threshold: 3, Base: 200 * time.Millisecond, Max: 300 * time.Millisecond, Forget: 10 * time.Second,
	})
	allow := func(addr string) time.Duration {
		t.Helper()

		wait, err := a.Allow(t.Context(), addr)
		if err != nil {
			t.Fatal(err)
		}

		return wait
	}

	// Per address: Burst attempts per window, then the rest of the window.
	for i := range 2 {
		if wait := allow("198.51.100.1"); wait != 0 {
			t.Fatalf("attempt %d refused for %v", i, wait)
		}
	}

	if wait := allow("198.51.100.1"); wait <= 0 || wait > 500*time.Millisecond {
		t.Fatalf("over the burst: wait %v", wait)
	}

	if wait := allow("198.51.100.2"); wait != 0 {
		t.Fatalf("another address refused for %v", wait)
	}

	time.Sleep(600 * time.Millisecond)

	if wait := allow("198.51.100.1"); wait != 0 {
		t.Fatalf("next window refused for %v", wait)
	}

	// Global: from the threshold on everyone waits, doubling up to Max.
	for range 3 {
		if err := a.Failed(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if wait := allow("192.0.2.1"); wait <= 0 || wait > 200*time.Millisecond {
		t.Fatalf("backoff: wait %v", wait)
	}

	if err := a.Failed(t.Context()); err != nil {
		t.Fatal(err)
	}

	if wait := allow("192.0.2.2"); wait <= 200*time.Millisecond || wait > 300*time.Millisecond {
		t.Fatalf("capped backoff: wait %v", wait)
	}

	time.Sleep(350 * time.Millisecond)

	if wait := allow("192.0.2.3"); wait != 0 {
		t.Fatalf("after the backoff: wait %v", wait)
	}

	// A success resets the failures: the next one is below the threshold.
	if err := a.Succeeded(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := a.Failed(t.Context()); err != nil {
		t.Fatal(err)
	}

	if wait := allow("192.0.2.4"); wait != 0 {
		t.Fatalf("after a success: wait %v", wait)
	}
}

func TestPGAttemptsAcrossReplicas(t *testing.T) {
	t.Parallel()
	st := pgStore(t)
	limits := console.LoginLimits{Burst: 5, Window: time.Minute, Threshold: 4, Base: time.Second, Max: 4 * time.Second, Forget: time.Hour}
	first, second := console.NewPGAttempts(st, limits), console.NewPGAttempts(st, limits)

	var group sync.WaitGroup

	results := make(chan time.Duration, 16)
	failures := make(chan error, 16)

	for i := range 16 {
		group.Go(func() {
			a := first
			if i%2 == 0 {
				a = second
			}

			wait, err := a.Allow(t.Context(), "198.51.100.12")
			if err != nil {
				failures <- err
				return
			}

			results <- wait
		})
	}

	group.Wait()
	close(results)
	close(failures)

	for err := range failures {
		t.Error(err)
	}

	allowed := 0

	for wait := range results {
		if wait == 0 {
			allowed++
		}
	}

	if allowed != limits.Burst {
		t.Fatalf("replicas allowed %d attempts, want %d", allowed, limits.Burst)
	}

	row, err := st.Get().Q.GetLoginAttempt(t.Context(), "198.51.100.12")
	if err != nil || row.Attempts != int32(limits.Burst) {
		t.Fatalf("counter grew after refusal: %v %v", row, err)
	}

	for i := range limits.Threshold {
		group.Go(func() {
			a := first
			if i%2 == 0 {
				a = second
			}

			if err := a.Failed(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}

	group.Wait()

	if wait, err := first.Allow(t.Context(), "192.0.2.4"); err != nil || wait <= 0 {
		t.Fatalf("shared backoff: %v %v", wait, err)
	}

	state, err := st.Get().Q.GetLoginState(t.Context())
	if err != nil || state.Failures != int64(limits.Threshold) {
		t.Fatalf("lost concurrent failures: %v %v", state, err)
	}

	// Database time owns expiry, independent of the replica's wall clock.
	if _, err := st.Get().Pool.Exec(t.Context(), "UPDATE backplane.console_login_state SET forget_at = clock_timestamp() - interval '1 second', blocked_until = 'epoch'"); err != nil {
		t.Fatal(err)
	}

	if err := second.Failed(t.Context()); err != nil {
		t.Fatal(err)
	}

	state, err = st.Get().Q.GetLoginState(t.Context())
	if err != nil || state.Failures != 1 {
		t.Fatalf("expired failures survived: %v %v", state, err)
	}

	if wait, err := first.Allow(t.Context(), "192.0.2.5"); err != nil || wait != 0 {
		t.Fatalf("expired global backoff: %v %v", wait, err)
	}

	// A new backend object (restart) sees existing shared counters.
	restarted := console.NewPGAttempts(st, limits)
	if wait, err := restarted.Allow(t.Context(), "198.51.100.12"); err != nil || wait <= 0 {
		t.Fatalf("restart reset IP window: %v %v", wait, err)
	}
}

func TestPGAttemptsPruneAndOutage(t *testing.T) {
	t.Parallel()
	st := pgStore(t)

	a := console.NewPGAttempts(st, console.DefaultLoginLimits())
	if _, err := a.Allow(t.Context(), "192.0.2.1"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.Get().Pool.Exec(t.Context(), "INSERT INTO backplane.console_login_attempt(address, attempts, expires_at) SELECT 'expired-' || n, 1, clock_timestamp() - interval '1 minute' FROM generate_series(1, 1500) n"); err != nil {
		t.Fatal(err)
	}

	if err := a.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := st.Get().Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.console_login_attempt").Scan(&count); err != nil || count != 1 {
		t.Fatalf("cleanup lost active window or kept expired rows: %d %v", count, err)
	}

	// A failed backend refuses login, rather than using local counters.
	h := backplanetest.New(t, backplanetest.Name("console-outage"))
	c := console.New(h.Root(), console.Settings{AdminToken: adminToken, InsecureCookie: true}, console.NewPG(st), a, registry.NewHub())
	st.Get().Pool.Close()

	if _, err := a.Allow(t.Context(), "192.0.2.2"); !errors.Is(err, console.ErrAttempts) {
		t.Fatalf("outage admitted attempt: %v", err)
	}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/login", strings.NewReader(`{"token":"`+adminToken+`"}`))
	response := httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("outage login: %d", response.Code)
	}
}

func TestPGSharedDatabaseSchema(t *testing.T) {
	t.Parallel()
	st := pgStoreWithSetup(t, func(ctx context.Context, conn *pgx.Conn) {
		// The service's database default must not choose platform migration storage.
		if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{conn.Config().Database}.Sanitize()+" SET search_path = public, service_data"); err != nil {
			t.Fatal(err)
		}

		_, err := conn.Exec(ctx, `
CREATE TABLE public.sqld_migrations (marker text);
INSERT INTO public.sqld_migrations VALUES ('service-owned');
CREATE TABLE public.console_login_state (marker text);
INSERT INTO public.console_login_state VALUES ('service-owned');
CREATE TABLE public.console_login_attempt (marker text);
INSERT INTO public.console_login_attempt VALUES ('service-owned');
CREATE TABLE public.console_session (marker text);
INSERT INTO public.console_session VALUES ('service-owned');
CREATE SCHEMA service_data;
CREATE TABLE service_data.widgets (marker text);
INSERT INTO service_data.widgets VALUES ('service-owned');`)
		if err != nil {
			t.Fatal(err)
		}
	})

	a := console.NewPGAttempts(st, console.DefaultLoginLimits())
	if wait, err := a.Allow(t.Context(), "192.0.2.1"); err != nil || wait != 0 {
		t.Fatalf("shared database: %v %v", wait, err)
	}

	if err := a.Failed(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := a.Succeeded(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := console.NewPG(st).Create(t.Context(), []byte("schema-isolation"), console.Session{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"public.sqld_migrations", "public.console_login_state", "public.console_login_attempt", "public.console_session", "service_data.widgets"} {
		var marker string
		if err := st.Get().Pool.QueryRow(t.Context(), "SELECT marker FROM "+table).Scan(&marker); err != nil || marker != "service-owned" {
			t.Fatalf("modified service table %s: %q %v", table, marker, err)
		}
	}

	var path string
	if err := st.Get().Pool.QueryRow(t.Context(), "SHOW search_path").Scan(&path); err != nil || path != "backplane" {
		t.Fatalf("search_path: %q %v", path, err)
	}

	var publicObjects int
	if err := st.Get().Pool.QueryRow(t.Context(), "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON c.relnamespace = n.oid WHERE n.nspname = 'public' AND c.relkind IN ('r','S','v','m')").Scan(&publicObjects); err != nil || publicObjects != 4 {
		t.Fatalf("objects leaked into public: %d %v", publicObjects, err)
	}

	var applied int
	if err := st.Get().Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.sqld_migrations").Scan(&applied); err != nil || applied < 1 {
		t.Fatalf("platform migration ledger: %d %v", applied, err)
	}
}

func TestPGLoginMigrationKeepsExistingSessions(t *testing.T) {
	t.Parallel()
	st := pgStoreWithSetup(t, func(ctx context.Context, conn *pgx.Conn) {
		inits, err := filepath.Glob("../store/migrations/*_init.sql")
		if err != nil || len(inits) != 1 {
			t.Fatalf("init migration: %v %v", inits, err)
		}

		initial, err := os.ReadFile(inits[0])
		if err != nil {
			t.Fatal(err)
		}

		legacy, err := postgres.Open(ctx, xlog.NewJSON(), postgres.Config{DSN: config.Secret(conn.Config().ConnString())},
			postgres.Schema(store.Schema), postgres.Migrations(fstest.MapFS{filepath.Base(inits[0]): {Data: initial}}))
		if err != nil {
			t.Fatal(err)
		}
		defer legacy.Close()

		if _, err := legacy.Pool.Exec(ctx, `INSERT INTO backplane.console_session(token_hash, created_at, expires_at, last_seen_at, address, user_agent)
VALUES ('existing-session', now(), now()+interval '1 hour', now(), '192.0.2.1', 'existing-client')`); err != nil {
			t.Fatal(err)
		}

		var count int
		if err := legacy.Pool.QueryRow(ctx, "SELECT count(*) FROM backplane.sqld_migrations").Scan(&count); err != nil || count != 1 {
			t.Fatalf("old migration ledger: %d %v", count, err)
		}
	})

	var agent string
	if err := st.Get().Pool.QueryRow(t.Context(), "SELECT user_agent FROM backplane.console_session").Scan(&agent); err != nil || agent != "existing-client" {
		t.Fatalf("upgrade lost session: %q %v", agent, err)
	}

	if wait, err := console.NewPGAttempts(st, console.DefaultLoginLimits()).Allow(t.Context(), "192.0.2.1"); err != nil || wait != 0 {
		t.Fatalf("upgraded login state: %v %v", wait, err)
	}
}
