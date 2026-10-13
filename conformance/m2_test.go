package conformance_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ws-proto/wsrpc"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/bindings"
)

// M2 execution conformance: the real backplane binary and
// the real hello, with NATS and Temporal of platform-in-a-box, direct process listeners. The milestone's proof — "binding hello.Greet := hello.Echo, rule
// on hello.Greeted := hello.Echo; decoupling end to end, one trace through
// everything" — driven through the console's /ws as the console drives it.
//
// Uses isolated listener ports: `make test-m2`.
const (
	m2Backplane = "m2-backplane-1"
	m2Hello     = "m2-hello-1"
	m2Version   = "0.0.0-m2" // backplane's manifest version: removed after the run
	m2HelloVer  = "9.9.6"
	m2Hook      = service + ".Greet"
	m2Echo      = service + ".Echo"
	m2Event     = service + ".Greeted"
	m2Stream    = "bp_" + service
	m2DLQStream = "bp_dlq_" + service
	m2Wait      = 60 * time.Second // endpoints settle, workers poll, visibility catches up
	// m2Resume bounds a resumed rule's first run: a delivery lost to a
	// stopped pull comes back after the consumer's AckWait (1m).
	m2Resume = 90 * time.Second
	// m2Paused is how long a paused rule is watched not to run.
	m2Paused = 3 * time.Second

	// YAML forms (as the console edits them) of what the scenario saves.
	m2BindingText = `
hook: hello.Greet
steps:
  echo: {activity: hello.Echo, input: {text: '"bound " + req.name'}}
result: {text: echo.text}
`
	m2DraftText = `
hook: hello.Greet
steps:
  echo: {activity: hello.Echo, input: {text: '"test " + req.name'}}
result: {text: echo.text}
`
	m2UnknownActivityText = `
hook: hello.Greet
steps:
  echo: {activity: hello.Nope, input: {text: req.name}}
result: {text: echo.text}
`
	m2TypeErrorText = `
hook: hello.Greet
steps:
  echo: {activity: hello.Echo, input: {text: req.name + 1}}
result: {text: echo.text}
`
	m2RuleText = `
event: hello.Greeted
when: event.name != "skip"
steps:
  echo: {activity: hello.Echo, input: {text: event.name}}
`
)

// m2 is the scenario's world.
type m2 struct {
	consul    *api.Client
	cc        *wsrpc.ClientConn // the direct console
	backplane *m1Proc
	hello     *m1Proc
	jet       jetstream.JetStream
	helloURL  string
	ruleID    string
	ruleDef   *consolev1.RuleDefinition
}

// m2Env skips without the stack: Consul, PostgreSQL, NATS, Temporal.
func m2Env(t *testing.T) (string, string, string, string) {
	t.Helper()

	consul, dsn := os.Getenv("BACKPLANE_TEST_CONSUL"), os.Getenv("BACKPLANE_TEST_PG")
	natsAddr, temporalAddr := os.Getenv("BACKPLANE_TEST_NATS"), os.Getenv("BACKPLANE_TEST_TEMPORAL")

	if consul == "" || dsn == "" || natsAddr == "" || temporalAddr == "" {
		t.Skip("BACKPLANE_TEST_{CONSUL,PG,NATS,TEMPORAL} not set (make up; make test-m2)")
	}

	return consul, dsn, natsAddr, temporalAddr
}

// m2JetStream is the test's own JetStream client. hello's streams (and
// with them every consumer on them: hello's reactor, the rules'
// backplane__rule-<id>) are deleted before the run and after it — the
// cleanup is registered before the processes start, so it runs after
// they stopped.
func m2JetStream(t *testing.T, url string) jetstream.JetStream {
	t.Helper()

	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("nats %s: %v", url, err)
	}

	jet, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}

	drop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		for _, s := range []string{m2Stream, m2DLQStream} {
			if err := jet.DeleteStream(ctx, s); err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
				t.Logf("delete stream %s: %v", s, err)
			}
		}
	}

	drop()
	t.Cleanup(func() {
		drop()
		conn.Close()
	})

	return jet
}

// TestM2 runs backplane and hello with NATS and Temporal and walks the M2
// scenario.
//
//nolint:paralleltest // owns the hello service identity
func TestM2(t *testing.T) {
	addr, dsn, natsAddr, temporalAddr := m2Env(t)
	natsURL := "nats://" + natsAddr

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	m1Until(t, m1Wait, "no live hello instance on this Consul (stop other hello runs)", func() (bool, string) {
		keys, _, err := c.KV().Keys(m1InstancesPrefix(service), "", nil)

		return err == nil && len(keys) == 0, fmt.Sprint(keys, err)
	})

	wipe := func() {
		_, _ = c.KV().DeleteTree("backplane/services/"+service+"/", nil)
		_, _ = c.KV().DeleteTree("config/"+service+"/", nil)
		_, _ = c.KV().Delete(m1InstancesPrefix("backplane")+m2Backplane, nil)
		_, _ = c.KV().Delete("backplane/services/backplane/manifests/"+m2Version, nil)
	}

	scratch := m1Database(t, dsn) // dropped last: rules and bindings go with it

	wipe()
	t.Cleanup(wipe)

	w := &m2{consul: c, jet: m2JetStream(t, natsURL)}
	m2CleanEndpoint(t, temporalAddr)

	host, secret := advertise(t), m1Random(t)
	session := &m1{token: m1Random(t)}

	dir := t.TempDir()
	backplaneBin := m1Build(t, dir, "backplane", "../cmd/backplane", m2Version)
	helloBin := m1Build(t, dir, service, "../examples/hello/cmd/hello", m2HelloVer)

	consolePort, helloPort := freePort(t), freePort(t)
	w.helloURL = "http://127.0.0.1:" + helloPort
	session.console = "127.0.0.1:" + consolePort
	w.backplane = m1Start(t, "backplane", backplaneBin,
		"BACKPLANE_CONSUL_ADDR="+addr,
		"BACKPLANE_NATS_URL="+natsURL,
		"BACKPLANE_TEMPORAL_ADDR="+temporalAddr,
		"BACKPLANE_PG_DSN="+scratch,
		"BACKPLANE_VALKEY_ADDR="+testValkey(t),
		"BACKPLANE_INSTANCE="+m2Backplane,
		"BACKPLANE_ADVERTISE="+host,
		"BACKPLANE_INTERNAL_PORT="+freePort(t),
		"BACKPLANE_PUBLIC_PORT="+freePort(t),
		"BACKPLANE_INTERNAL_SECRET="+secret,
		"BACKPLANE_ADMIN_TOKEN="+session.token,
		"BACKPLANE_CONSOLE_LISTEN=:"+consolePort,
		"BACKPLANE_CONSOLE_PREFIX="+m1Prefix,
		"BACKPLANE_CONSOLE_INSECURE_COOKIE=true",
		"BACKPLANE_LIVE_CONFIG_RECONCILE_INTERVAL="+m1Reconcile.String(),
		"BACKPLANE_SHUTDOWN_DRAIN=100ms",
		"BACKPLANE_CONSUL_CHECK_INTERVAL=2s",
	)

	helloEnv := []string{
		"BACKPLANE_CONSUL_ADDR=" + addr,
		"BACKPLANE_NATS_URL=" + natsURL,
		"BACKPLANE_TEMPORAL_ADDR=" + temporalAddr,
		"BACKPLANE_INSTANCE=" + m2Hello,
		"HELLO_LEGACY_LISTEN=:" + freePort(t),
		"BACKPLANE_ADVERTISE=" + host,
		"BACKPLANE_INTERNAL_PORT=" + freePort(t),
		"BACKPLANE_PUBLIC_PORT=" + helloPort,
		"BACKPLANE_INTERNAL_SECRET=" + secret,
		"BACKPLANE_SHUTDOWN_DRAIN=100ms",
		"BACKPLANE_CONSUL_CHECK_INTERVAL=2s",
	}
	w.hello = m1Start(t, service, helloBin, helloEnv...)

	m1Until(t, m1Wait, "hello ready", func() (bool, string) {
		return m1Ready(w.hello, helloEnv)
	})

	m1Until(t, m1Wait, "console listening", func() (bool, string) {
		code, body := httpGet("http://" + session.console + m1Prefix + "/auth/session")

		return code == http.StatusUnauthorized, fmt.Sprintf("%d %q", code, body)
	})

	w.cc, err = session.dial(t, "http://"+session.console)
	if err != nil {
		t.Fatalf("direct console: %v", err)
	}

	m1Until(t, m1Wait, "CatalogService.ListServices lists hello", func() (bool, string) {
		return m1Listed(t, w.cc)
	})

	// a. No binding yet: hello answers with its local greeter.
	w.unbound(t)

	// b. The binding: text → definition → validate → save → served.
	w.binds(t)

	// c. Invalid definitions are refused and saved nowhere.
	w.rejects(t)

	// f. One trace: HTTP → hook → binding workflow → Echo.
	w.traces(t)

	// e. TestBinding: an unsaved definition and the saved version.
	w.testsBinding(t)

	// Deleting a binding still restores the local fallback. Both the bound
	// and local greeting paths publish Greeted (the replicas test keeps it bound).
	w.unbinds(t)

	// d. The rule: runs per event, dedup by ce-id, pause and resume.
	w.rules(t)

	// e. TestRule: dry runs of a draft, a test run of the saved rule.
	w.testsRule(t)
}

// ---- helpers ----------------------------------------------------------------

// call is a unary console call that must succeed.
func (w *m2) call(t *testing.T, method string, req, res proto.Message) {
	t.Helper()

	if err := m1Call(t.Context(), w.cc, method, req, res); err != nil {
		t.Fatalf("%s(%v): %v", method, req, err)
	}
}

// logged counts the lines of the process's output that contain every one
// of parts.
func (p *m1Proc) logged(parts ...string) int {
	n := 0

	for line := range strings.Lines(p.logs.String()) {
		all := true

		for _, s := range parts {
			if !strings.Contains(line, s) {
				all = false

				break
			}
		}

		if all {
			n++
		}
	}

	return n
}

// m2Get is GET url with headers.
func m2Get(ctx context.Context, url string, headers map[string]string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, err.Error()
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(body)
}

// greets waits until GET /hello/?name= directly answers want.
func (w *m2) greets(t *testing.T, name, want string, headers map[string]string) {
	t.Helper()

	url := w.helloURL + "/hello/?name=" + name

	m1Until(t, m1Wait, "GET /hello/?name="+name+" directly = "+want, func() (bool, string) {
		code, body := m2Get(t.Context(), url, headers)

		return code == http.StatusOK && strings.TrimSpace(body) == want, fmt.Sprintf("%d %q", code, body)
	})
}

// greetOnce is one GET /hello/?name= directly that must answer want.
func (w *m2) greetOnce(t *testing.T, name, want string, headers map[string]string) {
	t.Helper()

	code, body := m2Get(t.Context(), w.helloURL+"/hello/?name="+name, headers)
	if code != http.StatusOK || strings.TrimSpace(body) != want {
		t.Fatalf("GET /hello/?name=%s directly: %d %q, want %q", name, code, body, want)
	}
}

// fallback is the local greeter's text with the defaults (config/hello/
// is wiped).
func fallback(name string) string { return "Hello, " + name + "!" }

// jsonField is field of a JSON object text; "" when absent or not JSON.
func jsonField(text, field string) string {
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) != nil {
		return ""
	}

	s, _ := m[field].(string)

	return s
}

// traced: text carries the trace id — as it is, or inside a base64
// payload the console could not show as JSON.
func traced(text, traceID string) bool {
	if strings.Contains(text, traceID) {
		return true
	}

	if raw, err := base64.StdEncoding.DecodeString(strings.Trim(text, `"`)); err == nil {
		return strings.Contains(string(raw), traceID)
	}

	return false
}

// traceparent is a fresh W3C trace context and its trace id.
// It returns the header value and the trace id.
func traceparent(t *testing.T) (string, string) {
	t.Helper()

	traceID := m1Random(t)   // 16 bytes
	span := m1Random(t)[:16] // 8 bytes

	return "00-" + traceID + "-" + span + "-01", traceID
}

// hookBinding is hello.Greet as ListBindings shows it.
func (w *m2) hookBinding(t *testing.T) (*consolev1.HookBinding, string) {
	t.Helper()

	var res consolev1.ListBindingsResponse
	if err := m1Call(t.Context(), w.cc, "/backplane.console.v1.BindingService/ListBindings",
		&consolev1.ListBindingsRequest{Service: service}, &res); err != nil {
		return nil, err.Error()
	}

	for _, b := range res.GetBindings() {
		if b.GetHook() == m2Hook {
			seen := b.String()

			return b, seen
		}
	}

	return nil, fmt.Sprint(res.GetBindings())
}

// callHook is CallService.CallHook of hello.Greet.
func (w *m2) callHook(t *testing.T, name string) (*consolev1.CallResult, error) {
	t.Helper()

	var res consolev1.CallHookResponse

	err := m1Call(t.Context(), w.cc, "/backplane.console.v1.CallService/CallHook",
		&consolev1.CallHookRequest{Hook: m2Hook, Input: `{"name":"` + name + `"}`}, &res)

	return res.GetResult(), err
}

func (w *m2) parseBinding(t *testing.T, text string) *consolev1.BindingDefinition {
	t.Helper()

	b, err := bindings.BindingYAML(text)
	if err != nil {
		t.Fatalf("binding %q: %v", text, err)
	}

	return b.PB()
}

// bindingRuns is ListBindingRuns of hello.Greet.
func (w *m2) bindingRuns(t *testing.T, tests bool) ([]*consolev1.Run, error) {
	t.Helper()

	var res consolev1.ListBindingRunsResponse
	if err := m1Call(t.Context(), w.cc, "/backplane.console.v1.BindingService/ListBindingRuns",
		&consolev1.ListBindingRunsRequest{Hook: m2Hook, Tests: tests, PageSize: 500}, &res); err != nil {
		return nil, fmt.Errorf("ListBindingRuns: %w", err)
	}

	return res.GetRuns(), nil
}

func (w *m2) bindingRun(t *testing.T, workflowID string) (*consolev1.GetBindingRunResponse, error) {
	t.Helper()

	var res consolev1.GetBindingRunResponse
	if err := m1Call(t.Context(), w.cc, "/backplane.console.v1.BindingService/GetBindingRun",
		&consolev1.GetBindingRunRequest{WorkflowId: workflowID}, &res); err != nil {
		return nil, fmt.Errorf("GetBindingRun %s: %w", workflowID, err)
	}

	return &res, nil
}

// findBindingRun polls the hook's runs for a completed one that match
// accepts; the failure lists what was seen.
func (w *m2) findBindingRun(
	t *testing.T, what string, match func(*consolev1.GetBindingRunResponse) (bool, string),
) *consolev1.GetBindingRunResponse {
	t.Helper()

	var found *consolev1.GetBindingRunResponse

	m1Until(t, m2Wait, what, func() (bool, string) {
		runs, err := w.bindingRuns(t, false)
		if err != nil {
			return false, err.Error()
		}

		seen := make([]string, 0, len(runs))

		for _, r := range runs {
			if r.GetStatus() != consolev1.RunStatus_RUN_STATUS_COMPLETED {
				seen = append(seen, r.GetWorkflowId()+" "+r.GetStatus().String())

				continue
			}

			run, err := w.bindingRun(t, r.GetWorkflowId())
			if err != nil {
				seen = append(seen, err.Error())

				continue
			}

			ok, why := match(run)
			if ok {
				found = run

				return true, ""
			}

			seen = append(seen, r.GetWorkflowId()+": "+why)
		}

		return false, fmt.Sprintf("%d runs: %s", len(runs), strings.Join(seen, "; "))
	})

	return found
}

// echoStep is the step echo of a run.
func echoStep(run *consolev1.GetBindingRunResponse) *consolev1.StepRun {
	for _, s := range run.GetSteps() {
		if s.GetStep() == "echo" && !s.GetUndo() {
			return s
		}
	}

	return nil
}

// scheduled is the payloads of the run's ActivityTaskScheduled events.
func scheduled(history []*consolev1.HistoryEvent) []string {
	var out []string

	for _, e := range history {
		if e.GetType() == "ActivityTaskScheduled" {
			out = append(out, e.GetSummary()+" "+e.GetPayload())
		}
	}

	return out
}

// ---- a. before any binding --------------------------------------------------

// unbound: hello.Greet is required and unbound — the hook call fails with
// NoBinding and the HTTP handler answers with its local greeter.
func (w *m2) unbound(t *testing.T) {
	t.Helper()

	w.greets(t, "x", fallback("x"), nil)

	m1Until(t, m2Wait, "ListBindings: hello.Greet required-unbound", func() (bool, string) {
		b, last := w.hookBinding(t)

		return b != nil && b.GetDeclared() && b.GetRequired() && b.GetService() == service &&
			b.GetState() == consolev1.BindingState_BINDING_STATE_REQUIRED_UNBOUND && b.GetCurrent() == nil, last
	})

	// The executor answers (its Nexus endpoint "hello" exists after a
	// settle delay): NoBinding, not "endpoint not found".
	m1Until(t, m2Wait, "CallHook hello.Greet unbound: "+noBindingType, func() (bool, string) {
		res, err := w.callHook(t, "unbound")
		if err != nil {
			return false, err.Error()
		}

		return res.GetErrorType() == noBindingType, res.String()
	})

	w.greetOnce(t, "x", fallback("x"), nil)
}

// ---- b. the binding -----------------------------------------------------------

// binds saves hello.Greet := hello.Echo from its text form and waits until
// hello's HTTP handler answers through it.
func (w *m2) binds(t *testing.T) {
	t.Helper()

	def := w.parseBinding(t, m2BindingText)

	if def.GetHook() != m2Hook || len(def.GetSteps()) != 1 || def.GetSteps()["echo"].GetActivity() != m2Echo ||
		len(def.GetResult().GetStructValue().GetFields()) != 1 {
		t.Fatalf("binding: %v", def)
	}

	// The wiring editor sees the contract and the binding's graph.
	var cat consolev1.GetWiringCatalogResponse
	w.call(t, "/backplane.console.v1.WiringService/GetWiringCatalog", &consolev1.GetWiringCatalogRequest{}, &cat)

	if !slices.ContainsFunc(cat.GetServices(), func(c *consolev1.WiringContract) bool {
		return c.GetService() == service && slices.ContainsFunc(c.GetActivities(), func(a *backplanev1.Activity) bool {
			return service+"."+a.GetName() == m2Echo
		})
	}) {
		t.Fatalf("GetWiringCatalog lacks %s: %v", m2Echo, &cat)
	}

	var analyzed consolev1.AnalyzeBindingResponse
	w.call(t, "/backplane.console.v1.WiringService/AnalyzeBinding",
		&consolev1.AnalyzeBindingRequest{Definition: def}, &analyzed)

	if a := analyzed.GetAnalysis(); len(a.GetViolations()) > 0 || len(a.GetSteps()) != 1 ||
		!slices.ContainsFunc(a.GetReferences(), func(r *consolev1.Reference) bool {
			return r.GetPath() == "/result/text" && r.GetVariable() == "echo"
		}) {
		t.Fatalf("AnalyzeBinding: %v", a)
	}

	var valid consolev1.ValidateBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/ValidateBinding",
		&consolev1.ValidateBindingRequest{Definition: def}, &valid)

	if len(valid.GetViolations()) > 0 {
		t.Fatalf("ValidateBinding %s: %v", m2BindingText, valid.GetViolations())
	}

	var saved consolev1.SaveBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/SaveBinding",
		&consolev1.SaveBindingRequest{Definition: def, Comment: "m2"}, &saved)

	v := saved.GetVersion()
	if len(saved.GetViolations()) > 0 || v.GetVersion() != 1 || v.GetHook() != m2Hook || v.GetDeleted() ||
		!strings.HasPrefix(v.GetAuthor(), "console:") || !proto.Equal(v.GetDefinition(), def) {
		t.Fatalf("SaveBinding: want version 1 by a console session, got %v", &saved)
	}

	m1Until(t, m2Wait, "ListBindings: hello.Greet bound at version 1", func() (bool, string) {
		b, last := w.hookBinding(t)

		return b != nil && b.GetState() == consolev1.BindingState_BINDING_STATE_BOUND &&
			b.GetCurrent().GetVersion() == 1, last
	})

	// The executor serves it.
	m1Until(t, m2Wait, "CallHook hello.Greet answered by the binding", func() (bool, string) {
		res, err := w.callHook(t, "probe")
		if err != nil {
			return false, err.Error()
		}

		return res.GetError() == "" && jsonField(res.GetOutput(), "text") == "bound probe", res.String()
	})

	// hello's HTTP handler, directly, answers through the binding.
	w.greets(t, "x", "bound x", nil)

	// The run of that call: step echo completed with its input and output.
	run := w.findBindingRun(t, "ListBindingRuns/GetBindingRun: a run of GET ?name=x with echo completed",
		func(run *consolev1.GetBindingRunResponse) (bool, string) {
			echo := echoStep(run)
			if echo == nil {
				return false, fmt.Sprintf("no step echo in %v", run.GetSteps())
			}

			return echo.GetStatus() == consolev1.StepRunStatus_STEP_RUN_STATUS_COMPLETED &&
					jsonField(echo.GetInput(), "text") == "bound x" && jsonField(echo.GetOutput(), "text") == "bound x",
				echo.String()
		})

	echo := echoStep(run)
	if run.GetHook() != m2Hook || run.GetVersion() != 1 || run.GetTest() || echo.GetActivity() != m2Echo ||
		echo.GetUndo() || echo.GetWorkflow() || echo.GetAttempt() < 1 ||
		run.GetRun().GetRun().GetStatus() != consolev1.RunStatus_RUN_STATUS_COMPLETED {
		t.Fatalf("GetBindingRun %s: hook %s version %d test %v, run %v, echo %v", run.GetRun().GetRun().GetWorkflowId(),
			run.GetHook(), run.GetVersion(), run.GetTest(), run.GetRun().GetRun(), echo)
	}

	if !strings.HasPrefix(run.GetRun().GetRun().GetWorkflowId(), "binding/"+m2Hook+"/") {
		t.Errorf("binding run id %q: want binding/%s/<request id>", run.GetRun().GetRun().GetWorkflowId(), m2Hook)
	}

	t.Logf("binding run %s: echo %s -> %s", run.GetRun().GetRun().GetWorkflowId(), echo.GetInput(), echo.GetOutput())
}

// ---- c. invalid bindings --------------------------------------------------------

// rejects: an unknown activity and a CEL type error are violations,
// neither validates nor saves; the binding stays at version 1.
func (w *m2) rejects(t *testing.T) {
	t.Helper()

	for _, c := range []struct {
		name, text string
		codes      []string
	}{
		{"unknown activity", m2UnknownActivityText, []string{"UNKNOWN_ACTIVITY"}},
		{"CEL type error", m2TypeErrorText, []string{"CEL_ERROR", "TYPE_MISMATCH"}},
	} {
		def := w.parseBinding(t, c.text)

		var valid consolev1.ValidateBindingResponse
		w.call(t, "/backplane.console.v1.BindingService/ValidateBinding",
			&consolev1.ValidateBindingRequest{Definition: def}, &valid)

		if !slices.ContainsFunc(valid.GetViolations(), func(v *consolev1.Violation) bool {
			return slices.Contains(c.codes, v.GetCode())
		}) {
			t.Fatalf("ValidateBinding, %s: want a violation %v, got %v", c.name, c.codes, valid.GetViolations())
		}

		t.Logf("ValidateBinding, %s: %v", c.name, valid.GetViolations())

		var saved consolev1.SaveBindingResponse
		w.call(t, "/backplane.console.v1.BindingService/SaveBinding",
			&consolev1.SaveBindingRequest{Definition: def, Comment: "m2 " + c.name}, &saved)

		if saved.GetVersion() != nil || len(saved.GetViolations()) == 0 {
			t.Fatalf("SaveBinding, %s: want violations and no version, got %v", c.name, &saved)
		}
	}

	var versions consolev1.ListBindingVersionsResponse
	w.call(t, "/backplane.console.v1.BindingService/ListBindingVersions",
		&consolev1.ListBindingVersionsRequest{Hook: m2Hook}, &versions)

	if len(versions.GetVersions()) != 1 || versions.GetVersions()[0].GetVersion() != 1 {
		t.Fatalf("ListBindingVersions after refused saves: want only version 1, got %v", versions.GetVersions())
	}

	var current consolev1.GetBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/GetBinding", &consolev1.GetBindingRequest{Hook: m2Hook}, &current)

	if current.GetVersion().GetVersion() != 1 {
		t.Fatalf("GetBinding after refused saves: %v", &current)
	}

	w.greetOnce(t, "x", "bound x", nil)
}

// ---- f. one trace ----------------------------------------------------------------

// traces: the trace id of the HTTP request's traceparent is the one of
// the hook call (HookCall.trace of hello's CallHook run), of the binding
// run (its input) and of the Echo call (ActivityCall.trace in the
// run's ActivityTaskScheduled).
func (w *m2) traces(t *testing.T) {
	t.Helper()

	header, traceID := traceparent(t)
	key := "m2-trace-" + m1Random(t)[:8]

	// With the key the hook runs once however often the poll asks.
	w.greets(t, "trace", "bound trace", map[string]string{"traceparent": header, "Idempotency-Key": key})

	// HTTP → hook: hello's CallHook run hook/hello/Greet/<key>.
	hookRun := "hook/" + service + "/Greet/" + key

	var call consolev1.GetRunResponse
	w.call(t, "/backplane.console.v1.WorkflowService/GetRun", &consolev1.GetRunRequest{WorkflowId: hookRun}, &call)

	if !traced(call.GetInput(), traceID) {
		t.Errorf("hook call %s: input (HookCall) without trace id %s: %s", hookRun, traceID, call.GetInput())
	}

	// hook → binding workflow → Echo.
	run := w.findBindingRun(t, "the binding run of the traced request (its input carries trace id "+traceID+")",
		func(run *consolev1.GetBindingRunResponse) (bool, string) {
			return traced(run.GetRun().GetInput(), traceID), "input " + run.GetRun().GetInput()
		})

	if echo := echoStep(run); jsonField(echo.GetInput(), "text") != "bound trace" {
		t.Errorf("traced binding run %s: echo %v", run.GetRun().GetRun().GetWorkflowId(), echo)
	}

	acts := scheduled(run.GetRun().GetHistory())
	if len(acts) == 0 || !traced(strings.Join(acts, "\n"), traceID) {
		t.Errorf("traced binding run %s: ActivityCall of Echo without trace id %s: %v",
			run.GetRun().GetRun().GetWorkflowId(), traceID, acts)
	}

	t.Logf("trace %s: HTTP → %s → %s → Echo", traceID, hookRun, run.GetRun().GetRun().GetWorkflowId())
}

// ---- e. TestBinding ------------------------------------------------------------------

// testsBinding: an unsaved definition and the current version run as test
// runs test/<hook>/<uuid>, listed apart from the hook calls.
func (w *m2) testsBinding(t *testing.T) {
	t.Helper()

	draft := w.parseBinding(t, m2DraftText)

	var res consolev1.TestBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/TestBinding",
		&consolev1.TestBindingRequest{Definition: draft, Input: `{"name":"t"}`}, &res)

	r := res.GetResult()
	if len(res.GetViolations()) > 0 || res.GetVersion() != 0 || r.GetError() != "" ||
		jsonField(r.GetOutput(), "text") != "test t" || !strings.HasPrefix(r.GetWorkflowId(), "test/"+m2Hook+"/") {
		t.Fatalf("TestBinding of an unsaved definition: %v", &res)
	}

	var current consolev1.TestBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/TestBinding",
		&consolev1.TestBindingRequest{Hook: m2Hook, Input: `{"name":"v"}`}, &current)

	if len(current.GetViolations()) > 0 || current.GetVersion() != 1 || current.GetResult().GetError() != "" ||
		jsonField(current.GetResult().GetOutput(), "text") != "bound v" {
		t.Fatalf("TestBinding of the current version: %v", &current)
	}

	// An invalid draft: violations, nothing runs.
	var bad consolev1.TestBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/TestBinding",
		&consolev1.TestBindingRequest{Definition: w.parseBinding(t, m2UnknownActivityText), Input: `{"name":"t"}`}, &bad)

	if len(bad.GetViolations()) == 0 || bad.GetResult() != nil {
		t.Fatalf("TestBinding of an invalid definition: %v", &bad)
	}

	m1Until(t, m2Wait, "ListBindingRuns(tests) lists "+r.GetWorkflowId(), func() (bool, string) {
		runs, err := w.bindingRuns(t, true)
		if err != nil {
			return false, err.Error()
		}

		ids := make([]string, 0, len(runs))
		for _, run := range runs {
			ids = append(ids, run.GetWorkflowId())
		}

		return slices.Contains(ids, r.GetWorkflowId()) && slices.Contains(ids, current.GetResult().GetWorkflowId()),
			fmt.Sprint(ids)
	})

	run, err := w.bindingRun(t, r.GetWorkflowId())
	if err != nil {
		t.Fatal(err)
	}

	if !run.GetTest() || run.GetVersion() != 0 || run.GetHook() != m2Hook ||
		jsonField(echoStep(run).GetOutput(), "text") != "test t" {
		t.Fatalf("GetBindingRun of the test run: %v", run)
	}

	// Test runs are not hook calls.
	calls, err := w.bindingRuns(t, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range calls {
		if strings.HasPrefix(c.GetWorkflowId(), "test/") {
			t.Fatalf("ListBindingRuns (calls) lists a test run: %s", c.GetWorkflowId())
		}
	}
}

// unbinds deletes the binding: a tombstone version 2, required-unbound
// again, and the local greeter answers.
func (w *m2) unbinds(t *testing.T) {
	t.Helper()

	var res consolev1.DeleteBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/DeleteBinding",
		&consolev1.DeleteBindingRequest{Hook: m2Hook, Comment: "m2 rules"}, &res)

	if !res.GetVersion().GetDeleted() || res.GetVersion().GetVersion() != 2 {
		t.Fatalf("DeleteBinding: want tombstone version 2, got %v", &res)
	}

	m1Until(t, m2Wait, "ListBindings: hello.Greet required-unbound after the delete", func() (bool, string) {
		b, last := w.hookBinding(t)

		return b != nil && b.GetState() == consolev1.BindingState_BINDING_STATE_REQUIRED_UNBOUND &&
			b.GetCurrent().GetDeleted() && b.GetCurrent().GetVersion() == 2, last
	})

	w.greets(t, "x", fallback("x"), nil)
}

// ---- d. the rule -------------------------------------------------------------------------

// greeted is one hello.Greeted in hello's stream.
type greeted struct {
	seq  uint64
	ceID string
	data string
}

// greetedOf finds the Greeted of name in the stream (EventService.
// PeekMessages, newest 500); exactly one is expected.
func (w *m2) greetedOf(t *testing.T, name string) greeted {
	t.Helper()

	var found []greeted

	m1Until(t, m2Wait, "hello.Greeted of "+name+" in "+m2Stream, func() (bool, string) {
		var res consolev1.PeekMessagesResponse
		if err := m1Call(t.Context(), w.cc, "/backplane.console.v1.EventService/PeekMessages",
			&consolev1.PeekMessagesRequest{Service: service, Event: "Greeted", Limit: 500}, &res); err != nil {
			return false, err.Error()
		}

		found = found[:0]
		names := make([]string, 0, len(res.GetMessages()))

		for _, m := range res.GetMessages() {
			n := jsonField(m.GetData(), "name")
			names = append(names, n)

			if n == name && !m.GetTest() {
				found = append(found, greeted{seq: m.GetSeq(), ceID: m.GetCloudEvent().GetId(), data: m.GetData()})
			}
		}

		return len(found) > 0, fmt.Sprintf("names %v, stream %v", names, res.GetStream())
	})

	if len(found) != 1 || found[0].ceID == "" {
		t.Fatalf("hello.Greeted of %s: want one with a ce-id, got %+v", name, found)
	}

	return found[0]
}

// durable is the rule's consumer.
func (w *m2) durable() string { return "backplane__rule-" + w.ruleID }

// consumerInfo of the rule's consumer.
func (w *m2) consumerInfo(t *testing.T) (*jetstream.ConsumerInfo, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	c, err := w.jet.Consumer(ctx, m2Stream, w.durable())
	if err != nil {
		return nil, fmt.Errorf("consumer %s: %w", w.durable(), err)
	}

	info, err := c.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("consumer %s: %w", w.durable(), err)
	}

	return info, nil
}

func consumerState(info *jetstream.ConsumerInfo) string {
	return fmt.Sprintf("delivered %d, ack floor %d, ack pending %d, pending %d, redelivered %d, waiting %d",
		info.Delivered.Stream, info.AckFloor.Stream, info.NumAckPending, info.NumPending, info.NumRedelivered,
		info.NumWaiting)
}

// settled waits until the rule's consumer acknowledged everything up to
// seq: the engine has decided on every event up to it.
func (w *m2) settled(t *testing.T, seq uint64) {
	t.Helper()

	m1Until(t, m2Wait, fmt.Sprintf("%s acknowledged up to stream seq %d", w.durable(), seq), func() (bool, string) {
		info, err := w.consumerInfo(t)
		if err != nil {
			return false, err.Error()
		}

		return info.AckFloor.Stream >= seq && info.NumAckPending == 0, consumerState(info)
	})
}

// ruleRuns is ListRuleRuns of the rule, by workflow id.
func (w *m2) ruleRuns(t *testing.T, tests bool) (map[string][]*consolev1.Run, error) {
	t.Helper()

	var res consolev1.ListRuleRunsResponse
	if err := m1Call(t.Context(), w.cc, "/backplane.console.v1.RuleService/ListRuleRuns",
		&consolev1.ListRuleRunsRequest{Id: w.ruleID, Tests: tests, PageSize: 500}, &res); err != nil {
		return nil, fmt.Errorf("ListRuleRuns: %w", err)
	}

	out := map[string][]*consolev1.Run{}
	for _, r := range res.GetRuns() {
		out[r.GetWorkflowId()] = append(out[r.GetWorkflowId()], r)
	}

	return out, nil
}

func runIDs(runs map[string][]*consolev1.Run) string {
	var b strings.Builder

	for id, rs := range runs {
		for _, r := range rs {
			fmt.Fprintf(&b, "%s (%s %s); ", id, r.GetRunId(), r.GetStatus())
		}
	}

	return b.String()
}

// ranOnce waits until the rule's run of ceID completed.
func (w *m2) ranOnce(t *testing.T, ceID string, limit time.Duration) string {
	t.Helper()

	id := "rule/" + w.ruleID + "/" + ceID

	m1Until(t, limit, "ListRuleRuns: "+id+" completed", func() (bool, string) {
		runs, err := w.ruleRuns(t, false)
		if err != nil {
			return false, err.Error()
		}

		found := runs[id]

		state := ""
		if info, err := w.consumerInfo(t); err == nil {
			state = consumerState(info)
		}

		return len(found) == 1 && found[0].GetStatus() == consolev1.RunStatus_RUN_STATUS_COMPLETED,
			runIDs(runs) + " | consumer: " + state
	})

	return id
}

// runsAre asserts the rule's (non-test) runs are exactly want, one each.
func (w *m2) runsAre(t *testing.T, what string, want ...string) {
	t.Helper()

	runs, err := w.ruleRuns(t, false)
	if err != nil {
		t.Fatal(err)
	}

	ok := len(runs) == len(want)
	for _, id := range want {
		ok = ok && len(runs[id]) == 1
	}

	if !ok {
		t.Fatalf("%s: rule runs %s, want exactly one each of %v", what, runIDs(runs), want)
	}
}

// rules saves the rule and checks what runs: one run per matching event,
// id rule/<id>/<ce-id>, none for a filtered one, none again for a
// republished ce-id, none while paused and the waiting event's after the
// resume.
func (w *m2) rules(t *testing.T) {
	t.Helper()

	rule, err := bindings.RuleYAML(m2RuleText)
	if err != nil {
		t.Fatal(err)
	}

	def := rule.PB()
	if def.GetEvent() != m2Event || def.GetWhen() != `event.name != "skip"` ||
		len(def.GetSteps()) != 1 || def.GetSteps()["echo"].GetActivity() != m2Echo {
		t.Fatalf("rule %q: %v", m2RuleText, def)
	}

	w.ruleDef = def

	var valid consolev1.ValidateRuleResponse
	w.call(t, "/backplane.console.v1.RuleService/ValidateRule", &consolev1.ValidateRuleRequest{Definition: def}, &valid)

	if len(valid.GetViolations()) > 0 {
		t.Fatalf("ValidateRule: %v", valid.GetViolations())
	}

	var saved consolev1.SaveRuleResponse
	w.call(t, "/backplane.console.v1.RuleService/SaveRule",
		&consolev1.SaveRuleRequest{Name: "m2-echo", Definition: def, Comment: "m2"}, &saved)

	if len(saved.GetViolations()) > 0 || saved.GetVersion().GetRuleId() == "" || saved.GetVersion().GetVersion() != 1 {
		t.Fatalf("SaveRule: %v", &saved)
	}

	w.ruleID = saved.GetVersion().GetRuleId()
	t.Logf("rule %s, consumer %s", w.ruleID, w.durable())

	// The engine consumes: the consumer exists (deliver policy new — from
	// here on events reach it) and the console lists it as a subscriber.
	m1Until(t, m2Wait, "rule consumer "+w.durable()+" on "+m2Stream, func() (bool, string) {
		info, err := w.consumerInfo(t)
		if err != nil {
			return false, err.Error() + fmt.Sprintf(" (backplane logged \"rule consuming\" %d times)",
				w.backplane.logged("rule consuming", w.ruleID))
		}

		return w.backplane.logged("rule consuming", w.ruleID) > 0, consumerState(info)
	})

	m1Until(t, m2Wait, "EventService.ListEvents: hello.Greeted has the rule as a subscriber", func() (bool, string) {
		var res consolev1.ListEventsResponse
		if err := m1Call(t.Context(), w.cc, "/backplane.console.v1.EventService/ListEvents",
			&consolev1.ListEventsRequest{Service: service}, &res); err != nil {
			return false, err.Error()
		}

		for _, e := range res.GetEvents() {
			if e.GetEvent() != m2Event {
				continue
			}

			for _, s := range e.GetSubscribers() {
				if s.GetKind() == consolev1.SubscriberKind_SUBSCRIBER_KIND_RULE && s.GetDurable() == w.durable() &&
					s.GetState() != nil {
					return true, ""
				}
			}

			return false, fmt.Sprint(e.GetSubscribers())
		}

		return false, fmt.Sprintf("no %s in %v (nats error %q)", m2Event, res.GetEvents(), res.GetNatsError())
	})

	// Two greetings: r1 matches `when`, skip does not. r1 carries a trace.
	header, traceID := traceparent(t)
	w.greetOnce(t, "r1", fallback("r1"), map[string]string{"traceparent": header})
	w.greetOnce(t, "skip", fallback("skip"), nil)

	first, skip := w.greetedOf(t, "r1"), w.greetedOf(t, "skip")

	r1Run := w.ranOnce(t, first.ceID, m2Wait)
	w.settled(t, max(first.seq, skip.seq))
	w.runsAre(t, "after r1 and skip", r1Run)

	// The run: Echo got event.name, and the event's trace.
	var got consolev1.GetRuleRunResponse
	w.call(t, "/backplane.console.v1.RuleService/GetRuleRun",
		&consolev1.GetRuleRunRequest{Id: w.ruleID, WorkflowId: r1Run}, &got)

	acts := scheduled(got.GetRun().GetHistory())
	if got.GetRun().GetRun().GetStatus() != consolev1.RunStatus_RUN_STATUS_COMPLETED || len(acts) == 0 ||
		!strings.Contains(strings.Join(acts, "\n"), m2Echo) {
		t.Fatalf("GetRuleRun %s: status %v, scheduled %v, failure %q", r1Run, got.GetRun().GetRun().GetStatus(), acts,
			got.GetRun().GetFailure())
	}

	if !traced(strings.Join(acts, "\n"), traceID) {
		t.Errorf("rule run %s: ActivityCall of Echo without the trace id %s of GET ?name=r1: %v (input %s)",
			r1Run, traceID, acts, got.GetRun().GetInput())
	}

	// The same ce-id again: through the console (JetStream's dedup window
	// may drop it), then by hand with a new Nats-Msg-Id so the stream keeps
	// it and the engine sees the ce-id a second time — the run exists, no
	// second one starts.
	var again consolev1.PublishTestEventResponse
	w.call(t, "/backplane.console.v1.EventService/PublishTestEvent", &consolev1.PublishTestEventRequest{
		Event: m2Event, Payload: first.data, Key: "r1", Id: first.ceID,
	}, &again)

	t.Logf("PublishTestEvent with ce-id %s: seq %d, duplicate %v", first.ceID, again.GetSeq(), again.GetDuplicate())

	republished := w.republish(t, first.seq)
	w.settled(t, max(republished, again.GetSeq()))
	w.runsAre(t, "after republishing r1's ce-id", r1Run)

	// Paused: events wait in the consumer, no run.
	var paused consolev1.PauseRuleResponse
	w.call(t, "/backplane.console.v1.RuleService/PauseRule", &consolev1.PauseRuleRequest{Id: w.ruleID}, &paused)

	if !paused.GetRule().GetPaused() {
		t.Fatalf("PauseRule: %v", &paused)
	}

	m1Until(t, m2Wait, "backplane stops consuming the paused rule", func() (bool, string) {
		n := w.backplane.logged("rule stopped, consumer kept", w.ruleID)

		return n > 0, fmt.Sprintf("logged %d times", n)
	})

	consumingBefore := w.backplane.logged("rule consuming", w.ruleID)

	w.greetOnce(t, "p1", fallback("p1"), nil)
	held := w.greetedOf(t, "p1")

	time.Sleep(m2Paused)

	info, err := w.consumerInfo(t)
	if err != nil {
		t.Fatal(err)
	}

	if info.AckFloor.Stream >= held.seq {
		t.Fatalf("paused rule: p1 (seq %d) acknowledged: %s", held.seq, consumerState(info))
	}

	w.runsAre(t, "while paused", r1Run)

	// Resumed: the waiting event runs.
	var resumed consolev1.ResumeRuleResponse
	w.call(t, "/backplane.console.v1.RuleService/ResumeRule", &consolev1.ResumeRuleRequest{Id: w.ruleID}, &resumed)

	if resumed.GetRule().GetPaused() {
		t.Fatalf("ResumeRule: %v", &resumed)
	}

	p1Run := w.ranOnce(t, held.ceID, m2Resume)

	if n := w.backplane.logged("rule consuming", w.ruleID); n <= consumingBefore {
		t.Errorf("resumed rule: backplane logged \"rule consuming\" %d times, %d before the resume", n, consumingBefore)
	}

	w.settled(t, held.seq)
	w.runsAre(t, "after the resume", r1Run, p1Run)
}

// republish publishes the message at seq of hello's stream again, as is
// (ce-id included) but under a new Nats-Msg-Id; the new stream seq.
func (w *m2) republish(t *testing.T, seq uint64) uint64 {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	stream, err := w.jet.Stream(ctx, m2Stream)
	if err != nil {
		t.Fatalf("stream %s: %v", m2Stream, err)
	}

	raw, err := stream.GetMsg(ctx, seq)
	if err != nil {
		t.Fatalf("stream %s seq %d: %v", m2Stream, seq, err)
	}

	msg := nats.NewMsg(raw.Subject)
	msg.Data = raw.Data

	for k, vs := range raw.Header {
		for _, v := range vs {
			msg.Header.Add(k, v)
		}
	}

	msg.Header.Set(jetstream.MsgIDHeader, "m2-republish-"+m1Random(t)[:8])

	ack, err := w.jet.PublishMsg(ctx, msg)
	if err != nil {
		t.Fatalf("republish seq %d: %v", seq, err)
	}

	if ack.Duplicate {
		t.Fatalf("republish seq %d under a new Nats-Msg-Id: dropped as a duplicate", seq)
	}

	t.Logf("republished seq %d (headers %v) as seq %d", seq, raw.Header, ack.Sequence)

	return ack.Sequence
}

// ---- e. TestRule --------------------------------------------------------------------------

// testsRule: a dry run of a draft evaluates `when` alone; a test run of the
// saved rule runs as test/rule/<id>/<uuid>, apart from the event runs.
func (w *m2) testsRule(t *testing.T) {
	t.Helper()

	for _, c := range []struct {
		event   string
		matched bool
	}{
		{`{"name":"skip","count":1}`, false},
		{`{"name":"d1","count":1}`, true},
	} {
		var res consolev1.TestRuleResponse
		w.call(t, "/backplane.console.v1.RuleService/TestRule",
			&consolev1.TestRuleRequest{Definition: w.ruleDef, Event: c.event, DryRun: true}, &res)

		if len(res.GetViolations()) > 0 || res.GetError() != "" || res.GetMatched() != c.matched || res.GetResult() != nil {
			t.Fatalf("TestRule dry run of %s: want matched %v and no run, got %v", c.event, c.matched, &res)
		}
	}

	var res consolev1.TestRuleResponse
	w.call(t, "/backplane.console.v1.RuleService/TestRule", &consolev1.TestRuleRequest{
		Id: w.ruleID, Event: `{"name":"t1","count":1}`, Meta: &consolev1.RuleEventMeta{Id: "m2-test-" + m1Random(t)[:8]},
	}, &res)

	r := res.GetResult()
	if len(res.GetViolations()) > 0 || !res.GetMatched() || r == nil || r.GetError() != "" ||
		!strings.HasPrefix(r.GetWorkflowId(), "test/rule/"+w.ruleID+"/") {
		t.Fatalf("TestRule of the saved rule: %v", &res)
	}

	m1Until(t, m2Wait, "ListRuleRuns(tests) lists "+r.GetWorkflowId(), func() (bool, string) {
		runs, err := w.ruleRuns(t, true)
		if err != nil {
			return false, err.Error()
		}

		return len(runs[r.GetWorkflowId()]) == 1, runIDs(runs)
	})

	// Neither test lands among the event runs.
	runs, err := w.ruleRuns(t, false)
	if err != nil {
		t.Fatal(err)
	}

	for id := range runs {
		if !strings.HasPrefix(id, "rule/"+w.ruleID+"/") {
			t.Fatalf("ListRuleRuns (events) lists %s", id)
		}
	}
}

// m2CleanEndpoint removes the example's endpoint after all test processes
// stop, so a stopped reconciler cannot recreate it during cleanup.
func m2CleanEndpoint(t *testing.T, address string) {
	t.Helper()

	temporal, err := client.DialContext(t.Context(), client.Options{HostPort: address})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		defer temporal.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		res, err := temporal.OperatorService().ListNexusEndpoints(ctx, &operatorservice.ListNexusEndpointsRequest{Name: service, PageSize: 1})
		if err != nil {
			t.Errorf("list cleanup endpoint: %v", err)
			return
		}

		for _, endpoint := range res.GetEndpoints() {
			_, err := temporal.OperatorService().DeleteNexusEndpoint(ctx, &operatorservice.DeleteNexusEndpointRequest{Id: endpoint.GetId(), Version: endpoint.GetVersion()})
			if err != nil {
				t.Errorf("delete cleanup endpoint: %v", err)
			}
		}
	})
}
