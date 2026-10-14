package conformance_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/types/known/sourcecontextpb"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/standard"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
)

const reactorWait = 30 * time.Second

// Payload versions: v2 added a field to v1.
type (
	pingV1 struct {
		ID   string `json:"id"`
		Mode string `json:"mode"`
	}
	pingV2 struct {
		ID    string `json:"id"`
		Mode  string `json:"mode"`
		Added string `json:"added"`
	}
	// sourceV2 is google.protobuf.SourceContext's JSON with a field the
	// message does not have.
	sourceV2 struct {
		FileName string `json:"fileName"` //nolint:tagliatelle // protojson name of SourceContext.file_name
		Added    string `json:"added"`
	}
)

type reactorsConfig struct {
	config.Backplane `json:"backplane"`
}

// reactorsState is what a test service built: its root, to publish and
// redrive from.
type reactorsState struct{ root deps.Scope }

// natsURL of platform-in-a-box; the test is skipped without it.
func natsURLOf(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_NATS")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_NATS not set (make up)")
	}

	return "nats://" + addr
}

// jetStreamOf connects the test's own JetStream client and deletes the
// streams of service when the test ends.
func jetStreamOf(t *testing.T, url, service string) jetStream {
	t.Helper()

	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}

	jet, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_ = jet.DeleteStream(ctx, "bp_"+service)
		_ = jet.DeleteStream(ctx, "bp_dlq_"+service)

		conn.Close()
	})

	return jetStream{jet}
}

// natsService is a service under test running in this process.
type natsService struct {
	state *reactorsState
	logs  *syncBuffer
	stop  func() error // stops it; Run's error
}

// runNATSService opens name with NATS at url (no Consul, no Temporal), lets
// declare add its events and reactors, runs it and waits until it is ready
// and every consumer in durables exists on stream. shutdown is the YAML of
// the backplane.shutdown block.
func runNATSService(
	t *testing.T, url string, jet jetStream, name, shutdown string, declare func(root deps.Scope),
	stream string, durables ...string,
) *natsService {
	t.Helper()

	platform := freePort(t)
	file := filepath.Join(t.TempDir(), name+".yaml")
	cfg := "backplane:\n" +
		"  nats:\n    url: " + url + "\n" +
		"  internal_port: " + platform + "\n" +
		"  public_port: " + freePort(t) + "\n" +
		"  shutdown:\n" + shutdown

	if err := os.WriteFile(file, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	logs := &syncBuffer{}

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[reactorsConfig]) (*reactorsState, error) {
		declare(root)

		return &reactorsState{root: root}, nil
	},
		standard.Drivers(), backplane.Name(name), backplane.Instance(name+"-1"), backplane.Version("1.0.0"),
		backplane.Advertise("127.0.0.1"), backplane.KeepSlog(),
		backplane.Logger(xlog.NewJSON(xlog.WithWriter(logs))),
		backplane.ConfigOptions(config.WithoutEnv(), config.WithoutConsul(), config.File(file)),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	var (
		once   sync.Once
		runErr error
	)

	stop := func() error {
		once.Do(func() {
			cancel()

			runErr = <-done
		})

		return runErr
	}

	t.Cleanup(func() {
		_ = stop()

		if t.Failed() {
			t.Logf("%s logs:\n%s", name, logs)
		}
	})

	waitFor(t, name+" ready", func() bool {
		code, _ := httpGet("http://127.0.0.1:" + platform + "/healthz/readiness")

		return code == http.StatusOK
	})

	for _, durable := range durables {
		waitFor(t, "consumer "+durable, func() bool {
			_, err := jet.Consumer(t.Context(), stream, durable)

			return err == nil
		})
	}

	return &natsService{state: svc.State(), logs: logs, stop: stop}
}

const quickStop = "    drain: 0s\n    listeners: 1s\n    reserve: 1s\n    timeout: 5s\n"

// deadLetters are the messages on subject of the service's dead-letter
// stream.
func deadLetters(t *testing.T, jet jetStream, service, subject string) []*jetstream.RawStreamMsg {
	t.Helper()

	s, err := jet.Stream(t.Context(), "bp_dlq_"+service)
	if err != nil {
		t.Fatalf("dead-letter stream: %v", err)
	}

	info, err := s.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var out []*jetstream.RawStreamMsg

	for seq := max(info.State.FirstSeq, 1); seq <= info.State.LastSeq; seq++ {
		m, err := s.GetMsg(t.Context(), seq, jetstream.WithGetMsgSubject(subject))
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		out = append(out, m)
		seq = m.Sequence
	}

	return out
}

// TestReactorDeadLetters: through backplane.Open, a reactor's terminal
// error dead-letters at once and a failing handler after its last
// delivery; each dead letter keeps the event and carries bp-error,
// bp-consumer and bp-delivered. Redrive runs the reactor's handler on them:
// a handled one is deleted, one that fails again stays.
//
//nolint:paralleltest // one NATS, services named per run
func TestReactorDeadLetters(t *testing.T) {
	url := natsURLOf(t)
	name := uniqueName("dlq")
	jet := jetStreamOf(t, url, name)

	var (
		ping      event.Ref[pingV1]
		handlerMu sync.Mutex
		fixed     bool
		seen      []string
	)

	svc := runNATSService(t, url, jet, name, quickStop, func(root deps.Scope) {
		ping = event.Declare[pingV1](root, "Ping")
		event.React(root, name+".Ping", func(_ context.Context, v pingV1) error {
			handlerMu.Lock()
			defer handlerMu.Unlock()

			switch {
			case v.Mode == "terminal":
				return event.Terminal(errors.New("bad input"))
			case v.Mode == "fail" && !fixed:
				return errors.New("still failing")
			}

			seen = append(seen, v.ID)

			return nil
		}, event.Consumer("sink"), event.MaxDeliver(2), event.Redelivery(50*time.Millisecond, 100*time.Millisecond))
	}, "bp_"+name, name+"__sink")

	for _, p := range []pingV1{{ID: "t", Mode: "terminal"}, {ID: "f", Mode: "fail"}, {ID: "o", Mode: "ok"}} {
		if err := ping.Publish(t.Context(), p, event.ID(name+"-"+p.ID)); err != nil {
			t.Fatal(err)
		}
	}

	subject := "bp.dlq." + name + ".sink"

	var dead []*jetstream.RawStreamMsg

	waitFor(t, "two dead letters", func() bool {
		dead = deadLetters(t, jet, name, subject)

		return len(dead) == 2
	})

	byID := map[string]*jetstream.RawStreamMsg{}
	for _, m := range dead {
		byID[m.Header.Get("ce-id")] = m
	}

	for id, want := range map[string]struct{ cause, delivered string }{
		name + "-t": {"bad input", "1"},
		name + "-f": {"still failing", "2"},
	} {
		m := byID[id]
		if m == nil {
			t.Fatalf("no dead letter for %s: %v", id, byID)
		}

		if !strings.Contains(m.Header.Get("bp-error"), want.cause) || m.Header.Get("bp-consumer") != "sink" ||
			m.Header.Get("bp-delivered") != want.delivered || m.Header.Get("ce-type") != name+".Ping" {
			t.Errorf("dead letter %s headers: %v", id, m.Header)
		}

		if !strings.HasPrefix(m.Header.Get("Nats-Msg-Id"), name+"__sink:bp_"+name+":") {
			t.Errorf("dead letter %s Nats-Msg-Id %q", id, m.Header.Get("Nats-Msg-Id"))
		}
	}

	handlerMu.Lock()
	fixed = true
	handlerMu.Unlock()

	handled, err := event.Redrive(t.Context(), svc.state.root, "sink")
	if handled != 1 || err == nil || !strings.Contains(err.Error(), "bad input") {
		t.Fatalf("redrive: handled %d, err %v", handled, err)
	}

	left := deadLetters(t, jet, name, subject)
	if len(left) != 1 || left[0].Header.Get("ce-id") != name+"-t" {
		t.Errorf("left after redrive: %d", len(left))
	}

	handlerMu.Lock()
	defer handlerMu.Unlock()

	if strings.Join(seen, ",") != "o,f" {
		t.Errorf("handled %v, want o then f (redriven)", seen)
	}
}

// TestReactorStopReturnsMessage: a handler still running when the service
// stops is cancelled at the end of the stop budget; that is not the
// message's failure — it is returned, never dead-lettered, and the next
// start gets the same event again.
//
//nolint:paralleltest // one NATS, services named per run
func TestReactorStopReturnsMessage(t *testing.T) {
	url := natsURLOf(t)
	name := uniqueName("stop")
	jet := jetStreamOf(t, url, name)

	type delivery struct {
		id      string
		attempt int
	}

	var (
		ping    event.Ref[pingV1]
		started = make(chan struct{}, 1)
		got     = make(chan delivery, 4)
	)

	declare := func(block bool) func(root deps.Scope) {
		return func(root deps.Scope) {
			ping = event.Declare[pingV1](root, "Ping")
			event.React(root, name+".Ping", func(ctx context.Context, _ pingV1) error {
				d, _ := event.DeliveryOf(ctx)
				got <- delivery{id: d.ID, attempt: d.Attempt}

				if block {
					started <- struct{}{}

					<-ctx.Done()

					return ctx.Err()
				}

				return nil
			}, event.Consumer("slow"), event.MaxDeliver(1), event.Timeout(5*time.Second))
		}
	}

	// A stop budget far shorter than the handler's timeout: the stop, not
	// the timeout, cancels the handler.
	stopBudget := "    drain: 0s\n    listeners: 1s\n    reserve: 1s\n    timeout: 3s\n"

	first := runNATSService(t, url, jet, name, stopBudget, declare(true), "bp_"+name, name+"__slow")

	if err := ping.Publish(t.Context(), pingV1{ID: "x"}, event.ID(name+"-x")); err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-time.After(reactorWait):
		t.Fatal("handler not started")
	}

	<-got

	stopped := time.Now()

	// The in-flight handler holds the stop to the end of its budget; Run
	// reports that, and still returns.
	_ = first.stop()

	if took := time.Since(stopped); took > 10*time.Second {
		t.Errorf("stop took %v", took)
	}

	waitFor(t, "the stop returns the message", func() bool {
		return strings.Contains(first.logs.String(), "reactor stopped mid-handler, message returned")
	})

	// Not a dead letter: MaxDeliver(1) would have dead-lettered a failure.
	if s, err := jet.Stream(t.Context(), "bp_dlq_"+name); err == nil {
		if info, err := s.Info(t.Context()); err == nil && info.State.Msgs != 0 {
			t.Fatalf("%d dead letters after a stop", info.State.Msgs)
		}
	}

	runNATSService(t, url, jet, name, quickStop, declare(false), "bp_"+name, name+"__slow")

	// Returned at once, or at the latest after ack_wait (timeout + 15s).
	select {
	case d := <-got:
		if d.id != name+"-x" || d.attempt < 2 {
			t.Errorf("redelivered %+v, want id %s-x, attempt >= 2", d, name)
		}
	case <-time.After(5*time.Second + 15*time.Second + 10*time.Second):
		t.Fatal("message not redelivered after restart")
	}
}

// TestEventSchemaEvolution: payloads evolve additively. A reader with the
// older type ignores the field a newer emitter added (JSON and protojson
// alike); a reader with the newer type reads a missing field as its zero
// value. Neither is a decode failure.
//
//nolint:paralleltest // one NATS, services named per run
func TestEventSchemaEvolution(t *testing.T) {
	url := natsURLOf(t)
	name := uniqueName("evo")
	jet := jetStreamOf(t, url, name)

	var (
		newer  event.Ref[pingV2]
		older  event.Ref[pingV1]
		source event.Ref[sourceV2]
		gotOld = make(chan pingV1, 1)
		gotNew = make(chan pingV2, 1)
		gotPB  = make(chan string, 1)
	)

	runNATSService(t, url, jet, name, quickStop, func(root deps.Scope) {
		newer = event.Declare[pingV2](root, "Newer")
		older = event.Declare[pingV1](root, "Older")
		source = event.Declare[sourceV2](root, "Source")

		event.React(root, name+".Newer", func(_ context.Context, v pingV1) error { gotOld <- v; return nil },
			event.Consumer("old-reader"), event.MaxDeliver(1))
		event.React(root, name+".Older", func(_ context.Context, v pingV2) error { gotNew <- v; return nil },
			event.Consumer("new-reader"), event.MaxDeliver(1))
		event.React(root, name+".Source", func(_ context.Context, v *sourcecontextpb.SourceContext) error {
			gotPB <- v.GetFileName()

			return nil
		}, event.Consumer("proto-reader"), event.MaxDeliver(1))
	}, "bp_"+name, name+"__old-reader", name+"__new-reader", name+"__proto-reader")

	if err := newer.Publish(t.Context(), pingV2{ID: "n", Mode: "m", Added: "later"}); err != nil {
		t.Fatal(err)
	}

	if err := older.Publish(t.Context(), pingV1{ID: "o", Mode: "m"}); err != nil {
		t.Fatal(err)
	}

	if err := source.Publish(t.Context(), sourceV2{FileName: "a.proto", Added: "later"}); err != nil {
		t.Fatal(err)
	}

	select {
	case v := <-gotOld:
		if v != (pingV1{ID: "n", Mode: "m"}) {
			t.Errorf("older reader got %+v", v)
		}
	case <-time.After(reactorWait):
		t.Fatal("older reader: nothing")
	}

	select {
	case v := <-gotNew:
		if v != (pingV2{ID: "o", Mode: "m"}) {
			t.Errorf("newer reader got %+v", v)
		}
	case <-time.After(reactorWait):
		t.Fatal("newer reader: nothing")
	}

	select {
	case v := <-gotPB:
		if v != "a.proto" {
			t.Errorf("proto reader got %q", v)
		}
	case <-time.After(reactorWait):
		t.Fatal("proto reader: nothing")
	}

	if s, err := jet.Stream(t.Context(), "bp_dlq_"+name); err == nil {
		if info, err := s.Info(t.Context()); err == nil && info.State.Msgs != 0 {
			t.Errorf("%d dead letters: a decode failed", info.State.Msgs)
		}
	}
}
