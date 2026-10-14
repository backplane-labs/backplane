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

type commandServer struct {
	consolev1.UnimplementedWorkflowServiceServer
	t     *testing.T
	store *store.Store
	calls atomic.Int64
}

func (s *commandServer) StartWorkflow(ctx context.Context, _ *consolev1.StartWorkflowRequest) (*consolev1.StartWorkflowResponse, error) {
	s.calls.Add(1)

	var count int
	if err := s.store.Pool.QueryRow(ctx, "SELECT count(*) FROM backplane.audit_entry WHERE outcome='intent'").Scan(&count); err != nil || count != 1 {
		s.t.Error("dispatch without durable intent", count, err)
	}

	return &consolev1.StartWorkflowResponse{WorkflowId: "workflow", RunId: "run"}, nil
}

func TestCommandIntentBeforeDispatch(t *testing.T) {
	t.Parallel()
	svc, st := replica(t, isolatedDatabase(t), func(context.Context, audit.Entry) error { return nil })
	handler := &commandServer{t: t, store: st}
	register := svc.Commands(func(registrar grpc.ServiceRegistrar) { consolev1.RegisterWorkflowServiceServer(registrar, handler) }, func(context.Context) string { return "operator" })
	api := consolev1.NewWorkflowServiceClient(client(t, register))

	_, err := api.StartWorkflow(t.Context(), &consolev1.StartWorkflowRequest{Service: "hello", Workflow: "flow"})
	if err != nil {
		t.Fatal(err)
	}

	history, err := svc.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{})
	if err != nil || len(history.GetRecords()) != 2 {
		t.Fatal(history, err)
	}

	result, intent := history.GetRecords()[0], history.GetRecords()[1]
	if result.GetOutcome() != "succeeded" || intent.GetOutcome() != "intent" || result.GetOperationId() != intent.GetOperationId() ||
		result.GetAttributes().GetFields()["run_id"].GetStringValue() != "run" {
		t.Fatal(history)
	}

	st.Pool.Close()

	_, err = api.StartWorkflow(t.Context(), &consolev1.StartWorkflowRequest{Service: "hello", Workflow: "flow"})
	if status.Code(err) != codes.Unavailable || handler.calls.Load() != 1 {
		t.Fatal("dispatched without audit database", handler.calls.Load(), err)
	}
}

type runServer struct {
	consolev1.UnimplementedWorkflowServiceServer
	consolev1.UnimplementedRuleServiceServer
}

func (runServer) CancelRun(context.Context, *consolev1.CancelRunRequest) (*consolev1.CancelRunResponse, error) {
	return &consolev1.CancelRunResponse{}, nil
}

func (runServer) TerminateRun(context.Context, *consolev1.TerminateRunRequest) (*consolev1.TerminateRunResponse, error) {
	return &consolev1.TerminateRunResponse{}, nil
}

func (runServer) SignalRun(context.Context, *consolev1.SignalRunRequest) (*consolev1.SignalRunResponse, error) {
	return &consolev1.SignalRunResponse{}, nil
}

func (runServer) CancelRuleRun(context.Context, *consolev1.CancelRuleRunRequest) (*consolev1.CancelRuleRunResponse, error) {
	return &consolev1.CancelRuleRunResponse{}, nil
}

// TestRunCommandAttribution: run commands are entries of the service the
// run belongs to — by id convention, the rule's event owner, or the task
// queue Temporal names — with the signal name and reason, never the
// signal's argument.
func TestRunCommandAttribution(t *testing.T) {
	t.Parallel()

	queues := map[string]string{"orders-42": "billing", "call-7": "hello.hooks"}
	svc, st := replica(t, isolatedDatabase(t), func(context.Context, audit.Entry) error { return nil },
		audit.WithRunQueue(func(_ context.Context, workflowID, _ string) (string, error) {
			if queue, ok := queues[workflowID]; ok {
				return queue, nil
			}

			return "", errors.New("no such run")
		}))

	rule := uuid.New()
	for _, statement := range []string{
		`INSERT INTO backplane.rule (id) VALUES ($1)`,
		`INSERT INTO backplane.rule_version (rule_id, version, name, definition, author)
		VALUES ($1, 1, 'r', '{"event":"formatter.Formatted"}', 'test')`,
	} {
		if _, err := st.Pool.Exec(t.Context(), statement, rule); err != nil {
			t.Fatal(err)
		}
	}

	register := svc.Commands(func(registrar grpc.ServiceRegistrar) {
		consolev1.RegisterWorkflowServiceServer(registrar, runServer{})
		consolev1.RegisterRuleServiceServer(registrar, runServer{})
	}, func(context.Context) string { return "operator" })
	conn := client(t, register)
	workflows, rules := consolev1.NewWorkflowServiceClient(conn), consolev1.NewRuleServiceClient(conn)
	ctx := t.Context()

	calls := []func() error{
		func() error {
			_, err := workflows.CancelRun(ctx, &consolev1.CancelRunRequest{WorkflowId: "console/hello/GreetMany/1"})
			return err
		},
		func() error {
			_, err := workflows.SignalRun(ctx, &consolev1.SignalRunRequest{WorkflowId: "call-7", Signal: "go", Input: `"secret"`})
			return err
		},
		func() error {
			_, err := workflows.TerminateRun(ctx, &consolev1.TerminateRunRequest{WorkflowId: "orders-42", RunId: "r", Reason: "stuck"})
			return err
		},
		func() error {
			_, err := rules.CancelRuleRun(ctx, &consolev1.CancelRuleRunRequest{Id: rule.String(), WorkflowId: "rule/" + rule.String() + "/e"})
			return err
		},
	}
	for _, call := range calls {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}

	history, err := svc.SearchAudit(ctx, &consolev1.SearchAuditRequest{})
	if err != nil {
		t.Fatal(err)
	}

	results := map[string]*consolev1.AuditRecord{}

	for _, e := range history.GetRecords() {
		if strings.Contains(e.String(), "secret") {
			t.Fatalf("payload in audit: %v", e)
		}

		if e.GetOutcome() == "succeeded" {
			results[e.GetAction()] = e
		}
	}

	for action, want := range map[string]string{
		"workflow.cancel": "hello", "workflow.signal": "hello", "workflow.terminate": "billing", "rule.cancel": "formatter",
	} {
		if results[action].GetService() != want {
			t.Errorf("%s: service %q, want %q", action, results[action].GetService(), want)
		}
	}

	if d := results["workflow.signal"].GetAttributes().GetFields(); d["signal"].GetStringValue() != "go" || d["workflow_id"].GetStringValue() != "call-7" {
		t.Errorf("signal detail: %v", d)
	}

	if d := results["workflow.terminate"].GetAttributes().GetFields(); d["reason"].GetStringValue() != "stuck" || d["run_id"].GetStringValue() != "r" {
		t.Errorf("terminate detail: %v", d)
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
