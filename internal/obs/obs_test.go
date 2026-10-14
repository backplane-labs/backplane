package obs_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/obs"
)

func service(t *testing.T, handler http.HandlerFunc, edit func(*obs.Config)) *obs.Service {
	t.Helper()

	backend := httptest.NewServer(handler)
	t.Cleanup(backend.Close)

	cfg := obs.Config{LogsURL: backend.URL, MetricsURL: backend.URL, TracesURL: backend.URL, TraceQL: true}
	if edit != nil {
		edit(&cfg)
	}

	svc, err := obs.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(svc.Close)

	return svc
}

func query() *consolev1.QueryObsRequest {
	now := time.Now().UnixNano()

	return &consolev1.QueryObsRequest{
		Signal:   consolev1.ObsSignal_OBS_SIGNAL_LOGS,
		Language: consolev1.ObsLanguage_OBS_LANGUAGE_LOGSQL, Expression: "*", Limit: 2,
		Range: &consolev1.ObsTimeRange{StartUnixNano: now - int64(time.Minute), EndUnixNano: now},
	}
}

func TestQueryPrecisionAndIsolation(t *testing.T) {
	t.Parallel()
	svc := service(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer deployment" || r.Header.Get("Cookie") != "" || r.Header.Get("Baggage") != "" {
			t.Error("caller metadata leaked or deployment credential absent")
		}

		if r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("query") != "count:9007199254740993" || r.URL.Query().Get("allow_partial_response") != "0" {
			t.Error("query contract changed", r.URL.Query())
		}

		fmt.Fprintln(w, `{"_time":"2026-09-29T10:00:00.123456789Z","count":"9007199254740993","nested.value":"preserved"}`)
		fmt.Fprintln(w, `{"_msg":"second"}`)
		fmt.Fprintln(w, `{"_msg":"third"}`)
	}, func(cfg *obs.Config) { cfg.LogsAuthorization = "Bearer deployment" })
	req := query()
	req.Expression = "count:9007199254740993"
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "caller-secret", "cookie", "session-secret", "baggage", "private"))

	result, err := svc.QueryObs(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	rows := result.GetRows().GetRows()
	if len(rows) != 2 || !result.GetInfo().GetTruncated() || rows[0].GetFields()["count"] != "9007199254740993" || rows[0].GetFields()["_time"] != "2026-09-29T10:00:00.123456789Z" {
		t.Fatal(result)
	}
}

func TestMetricAndTracePrecision(t *testing.T) {
	t.Parallel()
	svc := service(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/query_range":
			if r.URL.Query().Get("step") != "0.123456789" {
				t.Error("step lost precision")
			}

			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"service.name":"external"},"values":[[1790680000.123456789,"9007199254740993"],[1790680000.246913578,"+Inf"]]}]}}`)
		case "/select/tempo/api/search":
			fmt.Fprint(w, `{"traces":[{"traceID":"abc","rootServiceName":"external","startTimeUnixNano":1790680000123456789,"durationMs":123,"extension":9007199254740993}]}`)
		case "/select/tempo/api/v2/traces/11111111111111111111111111111111":
			w.WriteHeader(http.StatusPartialContent)
			fmt.Fprint(w, `{"trace":{"resourceSpans":[{"scopeSpans":[]}]},"extension":9007199254740993}`)
		default:
			t.Error(r.URL.Path)
		}
	}, nil)
	req := query()
	req.Signal = consolev1.ObsSignal_OBS_SIGNAL_METRICS
	req.Language = consolev1.ObsLanguage_OBS_LANGUAGE_METRICSQL
	req.StepNanos = 123456789

	result, err := svc.QueryObs(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}

	samples := result.GetTimeSeries().GetSeries()[0].GetSamples()
	if samples[0].GetTimestampSeconds() != "1790680000.123456789" || samples[0].GetValue() != "9007199254740993" || samples[1].GetValue() != "+Inf" {
		t.Fatal(samples)
	}

	req.Signal = consolev1.ObsSignal_OBS_SIGNAL_TRACES
	req.Language = consolev1.ObsLanguage_OBS_LANGUAGE_TRACEQL
	req.StepNanos = 0

	result, err = svc.QueryObs(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}

	trace := result.GetTraces().GetTraces()[0]
	if trace.GetStartUnixNano() != "1790680000123456789" || !strings.Contains(string(trace.GetJson()), "9007199254740993") {
		t.Fatal(trace)
	}

	lookup, err := svc.GetTrace(t.Context(), &consolev1.GetTraceRequest{TraceId: strings.Repeat("1", 32)})
	if err != nil || !lookup.GetInfo().GetPartial() || !strings.Contains(string(lookup.GetTempoJson()), "9007199254740993") {
		t.Fatal(lookup, err)
	}
}

func TestQueryRejection(t *testing.T) {
	t.Parallel()
	svc := service(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request reached backend") }, nil)

	cases := map[string]func(*consolev1.QueryObsRequest){
		"no range":          func(r *consolev1.QueryObsRequest) { r.Range = nil },
		"negative time":     func(r *consolev1.QueryObsRequest) { r.Range.StartUnixNano = -1 },
		"reversed":          func(r *consolev1.QueryObsRequest) { r.Range.EndUnixNano = r.GetRange().GetStartUnixNano() },
		"range budget":      func(r *consolev1.QueryObsRequest) { r.Range.StartUnixNano = 0 },
		"limit":             func(r *consolev1.QueryObsRequest) { r.Limit = 999999 },
		"language":          func(r *consolev1.QueryObsRequest) { r.Language = consolev1.ObsLanguage_OBS_LANGUAGE_PROMQL },
		"empty expression":  func(r *consolev1.QueryObsRequest) { r.Expression = "" },
		"expression budget": func(r *consolev1.QueryObsRequest) { r.Expression = strings.Repeat("*", 33000) },
		"step":              func(r *consolev1.QueryObsRequest) { r.StepNanos = 1 },
		"negative step":     func(r *consolev1.QueryObsRequest) { r.StepNanos = -1 },
		"point budget": func(r *consolev1.QueryObsRequest) {
			r.Signal = consolev1.ObsSignal_OBS_SIGNAL_METRICS
			r.Language = consolev1.ObsLanguage_OBS_LANGUAGE_PROMQL
			r.StepNanos = 1
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := query()
			edit(req)

			_, err := svc.QueryObs(t.Context(), req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatal(err)
			}
		})
	}
}

func TestBackendFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		code int
		body string
		want codes.Code
	}{
		{"invalid query", 400, "secret backend error", codes.InvalidArgument},
		{"bad credentials", 401, "secret", codes.Unavailable},
		{"unavailable", 503, "secret", codes.Unavailable},
		{"saturated", 429, "secret", codes.ResourceExhausted},
		{"timeout", 504, "secret", codes.DeadlineExceeded},
		{"oversized", 200, strings.Repeat("a", 1025), codes.ResourceExhausted},
		{"malformed", 200, "secret non-json", codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := service(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.code); io.WriteString(w, tc.body) }, func(c *obs.Config) { c.ResponseBytes = 1024 })

			_, err := svc.QueryObs(t.Context(), query())
			if status.Code(err) != tc.want || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
		})
	}
}

func TestCancellationAndSaturation(t *testing.T) {
	t.Parallel()

	started, stopped := make(chan struct{}), make(chan struct{})
	svc := service(t, func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(stopped) }, func(c *obs.Config) { c.Concurrent = 1 })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() { _, err := svc.QueryObs(ctx, query()); done <- err }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}

	_, err := svc.QueryObs(t.Context(), query())
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}

	cancel()

	if err = <-done; status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream was not canceled")
	}
}

func TestDisabledAndRedirect(t *testing.T) {
	t.Parallel()

	disabled, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(disabled.Close)

	caps, err := disabled.GetObsCapabilities(t.Context(), nil)
	if err != nil || len(caps.GetSignals()) != 0 {
		t.Fatal(caps, err)
	}

	_, err = disabled.QueryObs(t.Context(), query())
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}

	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed redirect") }))
	defer target.Close()

	svc := service(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}, nil)

	_, err = svc.QueryObs(t.Context(), query())
	if status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
}

func TestSourceAndFieldDiscovery(t *testing.T) {
	t.Parallel()
	svc := service(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/select/logsql/query":
			fmt.Fprintln(w, `{"resource_attr:service.name":"kratos","resource_attr:service.namespace":"identity"}`)
		case "/api/v1/series":
			fmt.Fprint(w, `{"status":"success","data":[{"service.name":"external","__name__":"a"},{"service.name":"external","__name__":"b"},{"service.name":"other","__name__":"c"}]}`)
		case "/select/logsql/field_names":
			fmt.Fprint(w, `{"values":[{"value":"nested.body","hits":9007199254740993}]}`)
		case "/select/logsql/field_values":
			if r.URL.Query().Get("field") != "service.name" {
				t.Error("field parameter missing")
			}

			fmt.Fprint(w, `{"values":[{"value":"wrapper"},{"value":"kratos"},{"value":"external"}]}`)
		default:
			t.Error(r.URL.Path)
		}
	}, nil)
	span := query().GetRange()

	sources, err := svc.ListObsSources(t.Context(), &consolev1.ListObsSourcesRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_TRACES, Range: span})
	if err != nil || len(sources.GetSources()) != 1 || sources.GetSources()[0].GetResource()["service.name"] != "kratos" {
		t.Fatal(sources, err)
	}

	if _, ok := sources.GetSources()[0].GetResource()["service.instance.id"]; ok {
		t.Fatal("invented missing instance")
	}

	sources, err = svc.ListObsSources(t.Context(), &consolev1.ListObsSourcesRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_METRICS, Range: span, Limit: 2})
	if err != nil || len(sources.GetSources()) != 1 || !sources.GetInfo().GetTruncated() {
		t.Fatal(sources, err)
	}

	fields, err := svc.ListObsFields(t.Context(), &consolev1.ListObsFieldsRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_LOGS, Range: span})
	if err != nil || len(fields.GetFields()) != 1 || fields.GetFields()[0] != "nested.body" {
		t.Fatal(fields, err)
	}

	values, err := svc.ListObsFieldValues(t.Context(), &consolev1.ListObsFieldValuesRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_LOGS, Range: span, Field: "service.name", Limit: 2})
	if err != nil || !values.GetInfo().GetTruncated() || len(values.GetValues()) != 2 {
		t.Fatal(values, err)
	}
}

func TestMissingTraceAndTimeout(t *testing.T) {
	t.Parallel()
	svc := service(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/traces/") {
			fmt.Fprint(w, `{"trace":{"resourceSpans":[]}}`)
			return
		}

		<-r.Context().Done()
	}, func(c *obs.Config) { c.Timeout = time.Second })

	_, err := svc.GetTrace(t.Context(), &consolev1.GetTraceRequest{TraceId: strings.Repeat("1", 32)})
	if status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}

	_, err = svc.QueryObs(t.Context(), query())
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
}

func TestTotalPointBudget(t *testing.T) {
	t.Parallel()
	svc := service(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"name":"a"},"values":[[1,"1"],[2,"2"]]},{"metric":{"name":"b"},"values":[[1,"3"],[2,"4"]]}]}}`)
	}, func(c *obs.Config) { c.MaxPoints = 3 })
	req := query()
	req.Signal, req.Language = consolev1.ObsSignal_OBS_SIGNAL_METRICS, consolev1.ObsLanguage_OBS_LANGUAGE_PROMQL

	_, err := svc.QueryObs(t.Context(), req)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
}
