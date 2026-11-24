package audit_test

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/audit"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

func isolatedDatabase(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	ctx := t.Context()

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	name := "backplane_audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, dropErr := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if dropErr != nil {
			t.Error(dropErr)
		}

		admin.Close(cleanup)
	})

	parsed, err := url.Parse(dsn)
	if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		parsed.Path = "/" + name
		parsed.RawPath = ""

		return parsed.String()
	}

	return dsn + " dbname=" + name
}

func replica(
	t *testing.T, dsn string, publish func(context.Context, audit.Entry) error, options ...audit.Option,
) (*audit.Service, *store.Store) {
	t.Helper()
	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(dsn)}))
	svc := audit.New(h.Root(), st, append([]audit.Option{audit.WithPublisher(publish)}, options...)...)
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return svc, st.Get()
}

func eventually(t *testing.T, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("audit condition timed out")
}

func client(t *testing.T, register func(grpc.ServiceRegistrar)) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	register(server)

	go func() { _ = server.Serve(listener) }()

	t.Cleanup(func() { server.Stop(); listener.Close() })

	connection, err := grpc.NewClient("passthrough:///audit", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { connection.Close() })

	return connection
}

func TestAuditReplicasAndRecovery(t *testing.T) {
	t.Parallel()
	dsn := isolatedDatabase(t)

	var unavailable atomic.Bool
	unavailable.Store(true)

	var publishedMu sync.Mutex

	published := make(map[string]int)
	sink := func(_ context.Context, entry audit.Entry) error {
		if unavailable.Load() {
			return errors.New("delivery unavailable")
		}

		publishedMu.Lock()
		published[entry.ID]++
		publishedMu.Unlock()

		return nil
	}
	_, first := replica(t, dsn, sink)

	_, second := replica(t, dsn, sink)
	for range 12 {
		if _, err := first.AppendAudit(t.Context(), store.AuditDraft{Actor: "test", Action: "test.save", Subject: "fixture", Outcome: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}

	eventually(t, func() bool {
		var count int

		err := second.Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.audit_outbox WHERE attempts > 0 AND published_at IS NULL").Scan(&count)

		return err == nil && count > 0
	})

	var count int
	if err := first.Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.audit_entry").Scan(&count); err != nil || count != 12 {
		t.Fatal(count, err)
	}

	unavailable.Store(false)
	eventually(t, func() bool {
		var delivered int

		err := first.Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.audit_outbox WHERE published_at IS NOT NULL").Scan(&delivered)

		return err == nil && delivered == 12
	})
	publishedMu.Lock()
	defer publishedMu.Unlock()

	if len(published) != 12 {
		t.Fatal("lost outbox records", published)
	}
}

func TestAuditServiceFilter(t *testing.T) {
	t.Parallel()

	svc, st := replica(t, isolatedDatabase(t), func(context.Context, audit.Entry) error { return nil })
	for _, draft := range []store.AuditDraft{
		{Actor: "operator", Action: "config.save", Subject: "hello", Outcome: "succeeded", Service: "hello"},
		{Actor: "operator", Action: "hook.call", Subject: "hook=hello.Greet", Outcome: "intent", Service: "hello"},
		{Actor: "operator", Action: "config.save", Subject: "formatter", Outcome: "succeeded", Service: "formatter"},
		{Actor: "admin", Action: "session.create", Subject: uuid.NewString(), Outcome: "succeeded"},
	} {
		if _, err := st.AppendAudit(t.Context(), draft); err != nil {
			t.Fatal(err)
		}
	}

	api := consolev1.NewAuditServiceClient(client(t, svc.Register))

	page, err := api.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{Filter: where(is(consolev1.AuditField_AUDIT_FIELD_SERVICE, "hello"))})
	if err != nil || len(page.GetRecords()) != 2 {
		t.Fatal(page, err)
	}

	for _, entry := range page.GetRecords() {
		if entry.GetService() != "hello" {
			t.Fatal("entry of another service", entry)
		}
	}
}

func TestAuditPagesKeepTheirPlace(t *testing.T) {
	t.Parallel()

	svc, st := replica(t, isolatedDatabase(t), func(context.Context, audit.Entry) error { return nil })
	for range 5 {
		if _, err := st.AppendAudit(t.Context(), store.AuditDraft{Actor: "operator", Action: "config.save", Subject: "hello", Outcome: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}

	api := consolev1.NewAuditServiceClient(client(t, svc.Register))
	filter := where(is(consolev1.AuditField_AUDIT_FIELD_ACTOR, "operator"))

	page, err := api.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{Filter: filter, PageSize: 2})
	if err != nil || len(page.GetRecords()) != 2 || page.GetNextPageCursor() == "" {
		t.Fatal(page, err)
	}

	latest := page.GetRecords()[0].GetSequence()

	if _, err = st.AppendAudit(t.Context(), store.AuditDraft{Actor: "operator", Action: "config.save", Subject: "late", Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}

	older, err := api.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{Filter: filter, PageSize: 2, PageCursor: page.GetNextPageCursor()})
	if err != nil || len(older.GetRecords()) != 2 || older.GetRecords()[0].GetSequence() >= latest {
		t.Fatal("snapshot shifted", older, err)
	}

	_, err = api.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{Filter: where(is(consolev1.AuditField_AUDIT_FIELD_ACTOR, "other")), PageCursor: page.GetNextPageCursor()})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatal("cursor accepted changed filter", err)
	}
}

func TestExpiredLeaseAndStaleAcknowledgment(t *testing.T) {
	t.Parallel()
	dsn := isolatedDatabase(t)
	started, release := make(chan struct{}), make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()

	_, first := replica(t, dsn, func(context.Context, audit.Entry) error { close(started); <-release; return nil })

	entry, err := first.AppendAudit(t.Context(), store.AuditDraft{Actor: "test", Action: "lease.crash", Subject: "fixture", Outcome: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first replica did not claim")
	}

	if _, err = first.Pool.Exec(t.Context(), "UPDATE backplane.audit_outbox SET leased_until=now()-interval '1 second' WHERE sequence=$1", entry.Sequence); err != nil {
		t.Fatal(err)
	}

	var secondDelivered atomic.Bool

	_, second := replica(t, dsn, func(_ context.Context, event audit.Entry) error {
		if event.ID == entry.ID.String() {
			secondDelivered.Store(true)
		}

		return nil
	})
	eventually(t, secondDelivered.Load)
	unblock()
	eventually(t, func() bool {
		var complete bool

		dbErr := second.Pool.QueryRow(t.Context(), "SELECT published_at IS NOT NULL AND lease IS NULL FROM backplane.audit_outbox WHERE sequence=$1", entry.Sequence).Scan(&complete)

		return dbErr == nil && complete
	})
}

func TestRetentionKeepsUndeliveredPrefix(t *testing.T) {
	t.Parallel()
	svc, st := replica(t, isolatedDatabase(t), func(context.Context, audit.Entry) error { return errors.New("offline") })

	sequences := make([]int64, 0, 3)

	for range 3 {
		entry, err := st.AppendAudit(t.Context(), store.AuditDraft{Actor: "test", Action: "retention.save", Subject: "fixture", Outcome: "succeeded"})
		if err != nil {
			t.Fatal(err)
		}

		sequences = append(sequences, entry.Sequence)
	}

	if _, err := st.Pool.Exec(t.Context(), "UPDATE backplane.audit_outbox SET published_at=now() WHERE sequence=$1 OR sequence=$2", sequences[0], sequences[2]); err != nil {
		t.Fatal(err)
	}

	if err := svc.Expire(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	clock, err := st.Q.GetAuditClock(t.Context())
	if err != nil || clock.RetainedAfter != sequences[0] {
		t.Fatal("retention crossed pending event", clock, err)
	}

	for _, sequence := range sequences[1:] {
		if _, err = st.Q.GetAuditEntry(t.Context(), sequence); err != nil {
			t.Fatal("pending prefix was deleted", err)
		}
	}
}

func TestDisabledExportRetainsHistoryAndPendingWork(t *testing.T) {
	t.Parallel()
	dsn := isolatedDatabase(t)
	_, first := replica(t, dsn, nil)

	pending, err := first.AppendAudit(t.Context(), store.AuditDraft{Actor: "test", Action: "pending", Outcome: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	dep := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(dsn)}, store.AuditExport(false)))
	svc := audit.New(h.Root(), dep)
	h.Start()

	st := dep.Get()

	newest, err := st.AppendAudit(t.Context(), store.AuditDraft{Actor: "test", Action: "local-only", Outcome: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err = st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.audit_outbox").Scan(&count); err != nil || count != 1 {
		t.Fatalf("disabled export: %d %v", count, err)
	}

	if err = svc.Expire(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if _, err = st.Q.GetAuditEntry(t.Context(), pending.Sequence); err != nil {
		t.Fatal("old pending entry lost", err)
	}

	if _, err = st.Q.GetAuditEntry(t.Context(), newest.Sequence); err != nil {
		t.Fatal("cursor prefix crossed pending entry", err)
	}

	if _, err = st.Pool.Exec(t.Context(), "UPDATE backplane.audit_outbox SET published_at=now()"); err != nil {
		t.Fatal(err)
	}

	if err = svc.Expire(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	clock, err := st.Q.GetAuditClock(t.Context())
	if err != nil || clock.RetainedAfter != newest.Sequence {
		t.Fatalf("no-export entries block retention: %v %v", clock, err)
	}
}
