package conformance_test

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
)

// Direct runtime conformance: the real backplane binary and the real
// hello with Consul, PostgreSQL and NATS of platform-in-a-box — no
// Temporal. hello's hook calls reach backplane's HookService over gRPC
// (hooks.transport auto resolves to direct), the binding runs in
// backplane's process calling hello.Echo over hello's platform port, the
// rule runs inline in the event's delivery; the console lists the runs
// from PostgreSQL.
//
// Uses isolated listener ports: `make test-direct`.
const (
	directBackplane = "direct-backplane-1"
	directHello     = "direct-hello-1"
	directVersion   = "0.0.0-direct"
	directHelloVer  = "9.9.7"
	directWait      = 60 * time.Second
	directRunPrefix = "direct/"
)

// directEnv skips without Consul, PostgreSQL and NATS.
func directEnv(t *testing.T) (string, string, string) {
	t.Helper()

	consul, dsn, natsAddr := os.Getenv("BACKPLANE_TEST_CONSUL"), os.Getenv("BACKPLANE_TEST_PG"), os.Getenv("BACKPLANE_TEST_NATS")
	if consul == "" || dsn == "" || natsAddr == "" {
		t.Skip("BACKPLANE_TEST_{CONSUL,PG,NATS} not set (make up; make test-direct)")
	}

	return consul, dsn, natsAddr
}

// TestDirect runs backplane and hello without Temporal and walks the
// scenario: unbound fallback, a binding served through the direct
// runtime with its recorded run, a test of a draft, a rule running inline
// per event with dedup, a rule test, the delete.
//
//nolint:paralleltest // owns the hello service identity
func TestDirect(t *testing.T) {
	addr, dsn, natsAddr := directEnv(t)
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
		_, _ = c.KV().Delete(m1InstancesPrefix("backplane")+directBackplane, nil)
		_, _ = c.KV().Delete("backplane/services/backplane/manifests/"+directVersion, nil)
	}

	scratch := m1Database(t, dsn)

	wipe()
	t.Cleanup(wipe)

	w := &m2{consul: c, jet: m2JetStream(t, natsURL)}

	host, secret := advertise(t), m1Random(t)
	session := &m1{token: m1Random(t)}

	dir := t.TempDir()
	backplaneBin := m1Build(t, dir, "backplane", "../cmd/backplane", directVersion)
	helloBin := m1Build(t, dir, service, "../examples/hello/cmd/hello", directHelloVer)

	consolePort, helloPort := freePort(t), freePort(t)
	w.helloURL = "http://127.0.0.1:" + helloPort
	session.console = "127.0.0.1:" + consolePort
	w.backplane = m1Start(t, "backplane", backplaneBin,
		"BACKPLANE_CONSUL_ADDR="+addr,
		"BACKPLANE_NATS_URL="+natsURL,
		"BACKPLANE_PG_DSN="+scratch,
		"BACKPLANE_INSTANCE="+directBackplane,
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
		"BACKPLANE_DIRECT_RUN_TIMEOUT=30s",
	)

	helloEnv := []string{
		"BACKPLANE_CONSUL_ADDR=" + addr,
		"BACKPLANE_NATS_URL=" + natsURL,
		"BACKPLANE_INSTANCE=" + directHello,
		"HELLO_LEGACY_LISTEN=:" + freePort(t),
		"BACKPLANE_ADVERTISE=" + host,
		"BACKPLANE_INTERNAL_PORT=" + freePort(t),
		"BACKPLANE_PUBLIC_PORT=" + helloPort,
		"BACKPLANE_INTERNAL_SECRET=" + secret,
		"BACKPLANE_SHUTDOWN_DRAIN=100ms",
		"BACKPLANE_CONSUL_CHECK_INTERVAL=2s",
	}
	w.hello = m1Start(t, service, helloBin, helloEnv...)

	m1Until(t, m1Wait, "hello ready", func() (bool, string) { return m1Ready(w.hello, helloEnv) })

	m1Until(t, m1Wait, "console listening", func() (bool, string) {
		code, body := httpGet("http://" + session.console + m1Prefix + "/auth/session")

		return code == http.StatusUnauthorized, fmt.Sprintf("%d %q", code, body)
	})

	w.cc, err = session.dial(t, "http://"+session.console)
	if err != nil {
		t.Fatalf("direct console: %v", err)
	}

	m1Until(t, m1Wait, "CatalogService.ListServices lists hello", func() (bool, string) { return m1Listed(t, w.cc) })

	// The platform says what runs where.
	var caps consolev1.GetCapabilitiesResponse
	w.call(t, "/backplane.console.v1.PlatformService/GetCapabilities", &consolev1.GetCapabilitiesRequest{}, &caps)

	enabled := map[string]bool{}
	for _, cp := range caps.GetCapabilities() {
		enabled[cp.GetName()] = cp.GetEnabled()
	}

	if !enabled["bindings"] || !enabled["rules"] || !enabled["runtime_direct"] || enabled["runtime_temporal"] ||
		enabled["workflows"] {
		t.Fatalf("capabilities %v", enabled)
	}

	// a. No binding: hello's own greeter answers (the hook says NoBinding
	// over the direct transport); the console shows the hook unbound.
	w.greets(t, "x", fallback("x"), nil)

	m1Until(t, directWait, "ListBindings: hello.Greet required-unbound", func() (bool, string) {
		b, last := w.hookBinding(t)

		return b != nil && b.GetDeclared() && b.GetRequired() &&
			b.GetState() == consolev1.BindingState_BINDING_STATE_REQUIRED_UNBOUND, last
	})

	// b. The binding: saved with no runtime, it takes the installation's
	// default (direct, there being no Temporal); hello answers through it.
	w.directBinds(t)

	// c. A definition using what direct does not run is refused.
	w.directRejects(t)

	// e. TestBinding of a draft and of the saved version.
	w.directTests(t)

	// d. The rule runs inline per event, once per ce-id.
	w.directRules(t)

	// The delete restores the fallback.
	w.unbinds(t)
}

// directBinds saves hello.Greet := hello.Echo and checks the run the
// direct runtime recorded.
func (w *m2) directBinds(t *testing.T) {
	t.Helper()

	def := w.parseBinding(t, m2BindingText)

	var analyzed consolev1.AnalyzeBindingResponse
	w.call(t, "/backplane.console.v1.WiringService/AnalyzeBinding",
		&consolev1.AnalyzeBindingRequest{Definition: def}, &analyzed)

	if a := analyzed.GetAnalysis(); len(a.GetViolations()) > 0 || a.GetRuntime() != consolev1.Runtime_RUNTIME_DIRECT {
		t.Fatalf("AnalyzeBinding: %v", a)
	}

	var saved consolev1.SaveBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/SaveBinding",
		&consolev1.SaveBindingRequest{Definition: def, Comment: "direct"}, &saved)

	if len(saved.GetViolations()) > 0 || saved.GetVersion().GetVersion() != 1 || !proto.Equal(saved.GetVersion().GetDefinition(), def) {
		t.Fatalf("SaveBinding: %v", &saved)
	}

	m1Until(t, directWait, "ListBindings: hello.Greet bound at version 1", func() (bool, string) {
		b, last := w.hookBinding(t)

		return b != nil && b.GetState() == consolev1.BindingState_BINDING_STATE_BOUND && b.GetCurrent().GetVersion() == 1, last
	})

	// hello's HTTP handler answers through the binding: its hook call went
	// to backplane's HookService, the run called hello.Echo back.
	w.greets(t, "x", "bound x", nil)

	run := w.findBindingRun(t, "ListBindingRuns/GetBindingRun: a direct run with echo completed",
		func(run *consolev1.GetBindingRunResponse) (bool, string) {
			echo := echoStep(run)
			if echo == nil {
				return false, fmt.Sprintf("no step echo in %v", run.GetSteps())
			}

			return echo.GetStatus() == consolev1.StepRunStatus_STEP_RUN_STATUS_COMPLETED &&
					jsonField(echo.GetInput(), "text") == "bound x" && jsonField(echo.GetOutput(), "text") == "bound x",
				echo.String()
		})

	id := run.GetRun().GetRun().GetWorkflowId()
	if !strings.HasPrefix(id, directRunPrefix+"binding/"+m2Hook+"/") || run.GetRun().GetRun().GetWorkflowType() != "backplane.Direct.v1" ||
		run.GetHook() != m2Hook || run.GetVersion() != 1 || run.GetTest() ||
		run.GetRun().GetRun().GetStatus() != consolev1.RunStatus_RUN_STATUS_COMPLETED ||
		jsonField(run.GetRun().GetInput(), "name") != "x" || jsonField(run.GetRun().GetResult(), "text") != "bound x" {
		t.Fatalf("direct run %s: %v", id, run)
	}

	t.Logf("direct binding run %s: echo %s -> %s", id, echoStep(run).GetInput(), echoStep(run).GetOutput())
}

// directRejects: undo, retries and heartbeats do not compile for the
// direct runtime; naming the temporal runtime without Temporal is refused
// too.
func (w *m2) directRejects(t *testing.T) {
	t.Helper()

	def := w.parseBinding(t, `
hook: hello.Greet
steps:
  echo: {activity: hello.Echo, input: {text: req.name}, undo: hello.Echo, retry: {attempts: 2}}
result: {text: echo.text}
`)

	var valid consolev1.ValidateBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/ValidateBinding", &consolev1.ValidateBindingRequest{Definition: def}, &valid)

	codes := map[string]int{}
	for _, v := range valid.GetViolations() {
		codes[v.GetCode()]++
	}

	if codes["RUNTIME_UNSUPPORTED"] != 2 {
		t.Fatalf("ValidateBinding: %v", valid.GetViolations())
	}

	temporal := w.parseBinding(t, m2BindingText)
	temporal.Runtime = consolev1.Runtime_RUNTIME_TEMPORAL

	var test consolev1.TestBindingResponse

	err := m1Call(t.Context(), w.cc, "/backplane.console.v1.BindingService/TestBinding",
		&consolev1.TestBindingRequest{Definition: temporal, Input: `{"name":"t"}`}, &test)
	if err == nil || !strings.Contains(err.Error(), "temporal") {
		t.Fatalf("TestBinding on the temporal runtime without Temporal: %v %v", &test, err)
	}
}

// directTests: TestBinding runs a draft and the saved version on the
// direct runtime; test runs are listed apart from the calls.
func (w *m2) directTests(t *testing.T) {
	t.Helper()

	var draft consolev1.TestBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/TestBinding",
		&consolev1.TestBindingRequest{Definition: w.parseBinding(t, m2DraftText), Input: `{"name":"d"}`}, &draft)

	if r := draft.GetResult(); len(draft.GetViolations()) > 0 || draft.GetVersion() != 0 || r.GetError() != "" ||
		jsonField(r.GetOutput(), "text") != "test d" || !strings.HasPrefix(r.GetWorkflowId(), directRunPrefix+"test/"+m2Hook+"/") {
		t.Fatalf("TestBinding draft: %v", &draft)
	}

	var current consolev1.TestBindingResponse
	w.call(t, "/backplane.console.v1.BindingService/TestBinding",
		&consolev1.TestBindingRequest{Hook: m2Hook, Input: `{"name":"c"}`}, &current)

	if r := current.GetResult(); current.GetVersion() != 1 || r.GetError() != "" || jsonField(r.GetOutput(), "text") != "bound c" {
		t.Fatalf("TestBinding current: %v", &current)
	}

	m1Until(t, directWait, "ListBindingRuns(tests) lists both test runs", func() (bool, string) {
		runs, err := w.bindingRuns(t, true)
		if err != nil {
			return false, err.Error()
		}

		return len(runs) == 2, fmt.Sprint(runs)
	})

	runs, err := w.bindingRuns(t, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range runs {
		if strings.Contains(r.GetWorkflowId(), "/test/") {
			t.Fatalf("ListBindingRuns (calls) lists %s", r.GetWorkflowId())
		}
	}
}

// directRules: the rule runs inline per matching event, once per ce-id,
// its run read back with the step; a dry run and a test run.
func (w *m2) directRules(t *testing.T) {
	t.Helper()

	rule, err := bindings.RuleYAML(m2RuleText)
	if err != nil {
		t.Fatal(err)
	}

	def := rule.PB()
	w.ruleDef = def

	var saved consolev1.SaveRuleResponse
	w.call(t, "/backplane.console.v1.RuleService/SaveRule",
		&consolev1.SaveRuleRequest{Name: "direct-echo", Definition: def, Comment: "direct"}, &saved)

	if len(saved.GetViolations()) > 0 || saved.GetVersion().GetVersion() != 1 {
		t.Fatalf("SaveRule: %v", &saved)
	}

	w.ruleID = saved.GetVersion().GetRuleId()

	m1Until(t, directWait, "rule consumer "+w.durable(), func() (bool, string) {
		_, err := w.consumerInfo(t)

		return err == nil && w.backplane.logged("rule consuming", w.ruleID) > 0, errString(err)
	})

	w.greetOnce(t, "r1", "bound r1", nil)
	w.greetOnce(t, "skip", "bound skip", nil)

	first, skip := w.greetedOf(t, "r1"), w.greetedOf(t, "skip")
	runID := directRunPrefix + "rule/" + w.ruleID + "/" + first.ceID

	m1Until(t, directWait, "ListRuleRuns: "+runID+" completed", func() (bool, string) {
		runs, err := w.ruleRuns(t, false)
		if err != nil {
			return false, err.Error()
		}

		return len(runs[runID]) == 1 && runs[runID][0].GetStatus() == consolev1.RunStatus_RUN_STATUS_COMPLETED, runIDs(runs)
	})

	w.settled(t, max(first.seq, skip.seq))
	w.runsAre(t, "after r1 and skip", runID)

	var got consolev1.GetRuleRunResponse
	w.call(t, "/backplane.console.v1.RuleService/GetRuleRun", &consolev1.GetRuleRunRequest{Id: w.ruleID, WorkflowId: runID}, &got)

	if len(got.GetSteps()) != 1 || got.GetSteps()[0].GetStep() != "echo" || jsonField(got.GetSteps()[0].GetInput(), "text") != "r1" ||
		got.GetSteps()[0].GetStatus() != consolev1.StepRunStatus_STEP_RUN_STATUS_COMPLETED || got.GetVersion() != 1 {
		t.Fatalf("GetRuleRun %s: %v", runID, &got)
	}

	// The same ce-id again: the run exists, no second one.
	republished := w.republish(t, first.seq)
	w.settled(t, republished)
	w.runsAre(t, "after republishing r1's ce-id", runID)

	// Tests: a dry run evaluates when alone; a test run runs apart.
	var dry consolev1.TestRuleResponse
	w.call(t, "/backplane.console.v1.RuleService/TestRule",
		&consolev1.TestRuleRequest{Definition: def, Event: `{"name":"skip"}`, DryRun: true}, &dry)

	if dry.GetMatched() || dry.GetResult() != nil || dry.GetError() != "" {
		t.Fatalf("dry run: %v", &dry)
	}

	var test consolev1.TestRuleResponse
	w.call(t, "/backplane.console.v1.RuleService/TestRule", &consolev1.TestRuleRequest{
		Id: w.ruleID, Event: `{"name":"t1"}`, Meta: &consolev1.RuleEventMeta{Id: "direct-test-" + m1Random(t)[:8]},
	}, &test)

	r := test.GetResult()
	if !test.GetMatched() || r == nil || r.GetError() != "" || !strings.HasPrefix(r.GetWorkflowId(), directRunPrefix+"test/rule/"+w.ruleID+"/") {
		t.Fatalf("TestRule: %v", &test)
	}

	m1Until(t, directWait, "ListRuleRuns(tests) lists "+r.GetWorkflowId(), func() (bool, string) {
		runs, err := w.ruleRuns(t, true)
		if err != nil {
			return false, err.Error()
		}

		return len(runs[r.GetWorkflowId()]) == 1, runIDs(runs)
	})

	w.runsAre(t, "after the test", runID)
}

func errString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}
