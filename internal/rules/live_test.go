package rules_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	sp "github.com/gopherex/schemapb/go/schemapb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/executor"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/rules"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

func unique(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)

	return prefix + "-" + hex.EncodeToString(b)
}

// live connects to BACKPLANE_TEST_NATS and BACKPLANE_TEST_TEMPORAL; the
// test is skipped without either. The service's stream is deleted at
// cleanup.
func live(t *testing.T, service string) (jetstream.JetStream, client.Client) {
	t.Helper()

	natsAddr, temporalAddr := os.Getenv("BACKPLANE_TEST_NATS"), os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if natsAddr == "" || temporalAddr == "" {
		t.Skip("BACKPLANE_TEST_NATS and BACKPLANE_TEST_TEMPORAL not set (make up)")
	}

	conn, err := nats.Connect("nats://" + natsAddr)
	if err != nil {
		t.Fatal(err)
	}

	jet, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}

	c, err := client.DialContext(t.Context(), client.Options{
		HostPort: temporalAddr, Namespace: "default", Logger: tlog.NewStructuredLogger(slog.New(slog.DiscardHandler)),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = jet.DeleteStream(context.Background(), wire.StreamName(service))

		conn.Close()
		c.Close()
	})

	return jet, c
}

// source is an in-memory rules.Source: the test saves, pauses and deletes
// rules by hand.
type source struct {
	cat bindings.Catalog

	mu    sync.Mutex
	rules map[uuid.UUID]bindings.RuleEntry
	subs  []chan struct{}
}

func (s *source) Rules(context.Context) ([]bindings.RuleEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.AppendSeq(make([]bindings.RuleEntry, 0, len(s.rules)), maps.Values(s.rules)), nil
}

func (s *source) RuleVersion(_ context.Context, id uuid.UUID, _ int64) (bindings.RuleVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.rules[id]
	if !ok {
		return bindings.RuleVersion{}, bindings.ErrNoRule
	}

	return e.Current, nil
}

func (s *source) Changes(context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)

	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()

	return ch
}

func (s *source) Catalog() (bindings.Catalog, error) { return s.cat, nil }
func (s *source) RuleAPI() bindings.RuleAPI          { return bindings.RuleAPI{} }

// set saves e and signals the change.
func (s *source) set(e bindings.RuleEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rules[e.ID] = e

	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type signed struct {
	Email string `json:"email"`
	Plan  string `json:"plan"`
}

// recorder is the binding workflow's stand-in: it records every run (by
// workflow id, its run ids and inputs).
type recorder struct {
	mu   sync.Mutex
	runs map[string]map[string]executor.Input
}

func (r *recorder) workflow(ctx workflow.Context, in executor.Input) (*backplanev1.HookResult, error) {
	info := workflow.GetInfo(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.runs[info.WorkflowExecution.ID] == nil {
		r.runs[info.WorkflowExecution.ID] = map[string]executor.Input{}
	}

	r.runs[info.WorkflowExecution.ID][info.WorkflowExecution.RunID] = in

	ceID := ""
	if in.Meta != nil {
		ceID = in.Meta.ID
	}

	return &backplanev1.HookResult{Payload: []byte(`"ran ` + ceID + `"`)}, nil
}

func (r *recorder) of(workflowID string) map[string]executor.Input {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := map[string]executor.Input{}
	maps.Copy(out, r.runs[workflowID])

	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

// TestRulesLive: against NATS and Temporal, an active rule gets its
// consumer; a matching event starts rule/<id>/<ce-id> once — a
// republish of the same ce-id finds its run — and a non-matching one
// starts nothing; a paused rule keeps its consumer and starts nothing, a
// resumed one runs the events published meanwhile; a new version applies
// from the next event; a deleted rule's consumer is deleted. TestRule
// runs a definition on a sample event.
//
//nolint:paralleltest // one scenario over one NATS and Temporal
func TestRulesLive(t *testing.T) {
	svc := unique("rules")
	jet, c := live(t, svc)
	queue := "backplane-" + svc
	ctx := t.Context()

	schema, err := sp.ReflectType[signed](sp.ID(sp.Namespace(svc), sp.SchemaName("Signed"), sp.Ver(1, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}

	m := &backplanev1.Manifest{
		Service: svc, Version: "1.0.0",
		Events:     []*backplanev1.Event{{Name: "Signed", Schema: schema}},
		Activities: []*backplanev1.Activity{{Name: "Noop", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY}},
	}
	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{svc: {Name: svc, Manifests: map[string]*backplanev1.Manifest{"1.0.0": m}}})

	rec := &recorder{runs: map[string]map[string]executor.Input{}}
	w := worker.New(c, queue, worker.Options{})
	w.RegisterWorkflowWithOptions(rec.workflow, workflow.RegisterOptions{Name: rules.Workflow})

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(w.Stop)

	src := &source{cat: bindings.FromRegistry(hub.Current()), rules: map[uuid.UUID]bindings.RuleEntry{}}

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	engine := rules.New(h.Root(), src, hub,
		rules.WithJetStream(func() (jetstream.JetStream, error) { return jet, nil }),
		rules.WithTemporal(func() (client.Client, error) { return c, nil }),
		rules.WithQueue(queue), rules.Resync(time.Second))

	h.Start()

	id := uuid.New()
	rule := bindings.Rule{
		Event: svc + ".Signed", When: `event.plan == "pro"`,
		Steps: []bindings.Step{{Name: "noop", Activity: svc + ".Noop"}},
	}
	entry := bindings.RuleEntry{ID: id, Current: bindings.RuleVersion{RuleID: id, Version: 1, Name: "pro", Rule: rule}}
	src.set(entry)

	stream, durable := wire.StreamName(svc), rules.DurableName(id)

	waitFor(t, "the rule's consumer", func() bool {
		_, err := jet.Consumer(ctx, stream, durable)

		return err == nil
	})

	cons, err := jet.Consumer(ctx, stream, durable)
	if err != nil {
		t.Fatal(err)
	}

	cfg := cons.CachedInfo().Config
	if cfg.FilterSubject != wire.Subject(svc, "Signed") || cfg.AckPolicy != jetstream.AckExplicitPolicy ||
		cfg.DeliverPolicy != jetstream.DeliverNewPolicy || cfg.Metadata[rules.MetaRule] != id.String() {
		t.Fatalf("consumer config: %+v", cfg)
	}

	publish := func(ceID, plan, msgID string) {
		t.Helper()

		msg := &nats.Msg{
			Subject: wire.Subject(svc, "Signed"), Header: nats.Header{},
			Data: []byte(`{"email":"a@b.c","plan":"` + plan + `"}`),
		}
		msg.Header.Set(wire.HeaderSpecVersion, wire.SpecVersion)
		msg.Header.Set(wire.HeaderID, ceID)
		msg.Header.Set(wire.HeaderSource, svc)
		msg.Header.Set(wire.HeaderType, svc+".Signed")
		msg.Header.Set(wire.HeaderTime, time.Now().UTC().Format(time.RFC3339Nano))
		msg.Header.Set(jetstream.MsgIDHeader, msgID)

		if _, err := jet.PublishMsg(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}

	settled := func() bool {
		info, err := cons.Info(ctx)

		return err == nil && info.NumPending == 0 && info.NumAckPending == 0
	}

	run := func(ceID string) string { return rules.RunID(id.String(), ceID) }

	// A matching event runs once; a non-matching one does not.
	publish("e1", "pro", "m1")
	publish("e2", "free", "m2")

	waitFor(t, "run of e1", func() bool { return len(rec.of(run("e1"))) == 1 })
	waitFor(t, "both events settled", settled)

	for _, in := range rec.of(run("e1")) {
		if in.Kind != executor.KindRule || in.Identity.Rule != id.String() || in.Identity.Version != 1 ||
			in.Identity.Test || in.Meta == nil || in.Meta.ID != "e1" || in.Meta.Source != svc ||
			!strings.Contains(string(in.Payload), `"pro"`) || len(in.Program) == 0 {
			t.Fatalf("run input: %+v", in)
		}
	}

	if _, err := c.DescribeWorkflowExecution(ctx, run("e2"), ""); err == nil {
		t.Fatal("a non-matching event started a run")
	}

	// The same ce-id published again (another NATS message id, so JetStream
	// keeps it) finds its run.
	publish("e1", "pro", "m1-again")
	waitFor(t, "the republish settled", settled)

	if n := len(rec.of(run("e1"))); n != 1 {
		t.Fatalf("e1 ran %d times", n)
	}

	// Paused: the consumer stays, events wait.
	entry.Paused = true
	src.set(entry)
	time.Sleep(time.Second)

	publish("e3", "pro", "m3")
	time.Sleep(2 * time.Second)

	if _, err := jet.Consumer(ctx, stream, durable); err != nil {
		t.Fatalf("paused rule lost its consumer: %v", err)
	}

	if len(rec.of(run("e3"))) != 0 {
		t.Fatal("a paused rule started a run")
	}

	// Resumed: the waiting event runs.
	entry.Paused = false
	src.set(entry)

	waitFor(t, "run of e3 after resume", func() bool { return len(rec.of(run("e3"))) == 1 })

	// A new version applies from the next event on, at the same position.
	entry.Current.Version = 2
	entry.Current.Rule.When = `event.plan == "free"`
	src.set(entry)

	waitFor(t, "version 2 on the consumer", func() bool {
		info, err := cons.Info(ctx)

		return err == nil && info.Config.Metadata[rules.MetaVersion] == "2"
	})

	publish("e4", "free", "m4")
	waitFor(t, "run of e4 by version 2", func() bool { return len(rec.of(run("e4"))) == 1 })

	for _, in := range rec.of(run("e4")) {
		if in.Identity.Version != 2 {
			t.Fatalf("e4 ran version %d", in.Identity.Version)
		}
	}

	// TestRule on an unsaved definition: matched, the test run's result.
	res, err := engine.Test(ctx, &consolev1.TestRuleRequest{
		Definition: rule.PB(), Event: `{"email":"x@y.z","plan":"pro"}`,
		Meta: &consolev1.RuleEventMeta{Id: "t1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !res.GetMatched() || res.GetResult().GetOutput() != `"ran t1"` ||
		!strings.HasPrefix(res.GetResult().GetWorkflowId(), "test/rule/draft/") {
		t.Fatalf("test rule: %+v", res)
	}

	res, err = engine.Test(ctx, &consolev1.TestRuleRequest{Definition: rule.PB(), Event: `{"plan":"free"}`})
	if err != nil || res.GetMatched() || res.GetResult() != nil {
		t.Fatalf("test rule, no match: %+v %v", res, err)
	}

	// Deleted: the consumer goes.
	entry.Current.Version = 3
	entry.Current.Deleted = true
	entry.Current.Rule = bindings.Rule{}
	src.set(entry)

	waitFor(t, "the consumer deleted", func() bool {
		_, err := jet.Consumer(ctx, stream, durable)

		return errors.Is(err, jetstream.ErrConsumerNotFound)
	})
}
