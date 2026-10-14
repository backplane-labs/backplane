package otlp_test

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/obs"
)

// checkObs reads the signals just accepted by the independent storage stack
// through the production ObsService. It uses no fixture query responses.
func checkObs(t *testing.T, logs, traces, metrics, id string, nanos int64) {
	t.Helper()

	svc, err := obs.New(obs.Config{LogsURL: logs, TracesURL: traces, MetricsURL: metrics, TraceQL: true})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(svc.Close)

	span := &consolev1.ObsTimeRange{StartUnixNano: nanos - int64(time.Second), EndUnixNano: nanos + int64(2*time.Second)}

	result, err := svc.QueryObs(t.Context(), &consolev1.QueryObsRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_LOGS, Language: consolev1.ObsLanguage_OBS_LANGUAGE_LOGSQL, Expression: fmt.Sprintf("acceptance.id:%q", id), Range: span})
	if err != nil || len(result.GetRows().GetRows()) != 1 {
		t.Fatal("Obs logs", result, err)
	}

	fields := result.GetRows().GetRows()[0].GetFields()
	if fields["count"] != "9007199254740993" || fields["_time"] != time.Unix(0, nanos).UTC().Format(time.RFC3339Nano) || fields["nested.message"] != "structured body" {
		t.Fatal("Obs log precision", fields)
	}

	for _, signal := range []consolev1.ObsSignal{consolev1.ObsSignal_OBS_SIGNAL_LOGS, consolev1.ObsSignal_OBS_SIGNAL_METRICS, consolev1.ObsSignal_OBS_SIGNAL_TRACES} {
		discovered, discoverErr := svc.ListObsSources(t.Context(), &consolev1.ListObsSourcesRequest{Signal: signal, Range: span, Limit: 1000})
		if discoverErr != nil || len(discovered.GetSources()) == 0 {
			t.Fatal("Obs sources", signal, discovered, discoverErr)
		}

		want := "undeclared-external"
		field := "service.name"

		if signal == consolev1.ObsSignal_OBS_SIGNAL_TRACES {
			want = "kratos"
			field = "resource_attr:service.name"
		}

		found := false

		for _, source := range discovered.GetSources() {
			if source.GetResource()["service.name"] == want {
				found = true
			}
		}

		if !found {
			t.Fatal("undeclared source missing", signal, discovered)
		}

		names, namesErr := svc.ListObsFields(t.Context(), &consolev1.ListObsFieldsRequest{Signal: signal, Range: span, Limit: 1000})
		if namesErr != nil || !slices.Contains(names.GetFields(), field) {
			t.Fatal("Obs field discovery", signal, names, namesErr)
		}

		values, valuesErr := svc.ListObsFieldValues(t.Context(), &consolev1.ListObsFieldValuesRequest{Signal: signal, Range: span, Field: field})
		if valuesErr != nil || !slices.Contains(values.GetValues(), want) {
			t.Fatal("Obs field values", signal, values, valuesErr)
		}
	}

	metricReq := &consolev1.QueryObsRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_METRICS, Language: consolev1.ObsLanguage_OBS_LANGUAGE_METRICSQL, Expression: fmt.Sprintf(`backplane_acceptance{acceptance_id=%q}`, id), Range: span}
	for _, step := range []int64{0, int64(time.Second)} {
		metricReq.StepNanos = step

		result, err = svc.QueryObs(t.Context(), metricReq)
		if err != nil || len(result.GetTimeSeries().GetSeries()) != 1 || result.GetTimeSeries().GetSeries()[0].GetSamples()[0].GetValue() != "42" {
			t.Fatal("Obs metrics", result, err)
		}
	}

	for _, step := range []int64{0, int64(time.Second)} {
		result, err = svc.QueryObs(t.Context(), &consolev1.QueryObsRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_LOGS, Language: consolev1.ObsLanguage_OBS_LANGUAGE_LOGSQL, Expression: fmt.Sprintf("acceptance.id:%q | stats count()", id), Range: span, Statistics: true, StepNanos: step})
		if err != nil || len(result.GetTimeSeries().GetSeries()) == 0 {
			t.Fatal("Obs log statistics", result, err)
		}
	}

	checkObsTraces(t, svc, span, id, nanos)
}

func checkObsTraces(t *testing.T, svc *obs.Service, span *consolev1.ObsTimeRange, id string, nanos int64) {
	t.Helper()

	for _, expression := range []string{
		fmt.Sprintf(`{ resource.acceptance.id = %q }`, id),
		fmt.Sprintf(`{ resource.acceptance.id = %q && resource.service.name = "wrapper" }`, id),
		fmt.Sprintf(`{ resource.acceptance.id = %q && duration > 100ms }`, id),
	} {
		result, err := svc.QueryObs(t.Context(), &consolev1.QueryObsRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_TRACES, Language: consolev1.ObsLanguage_OBS_LANGUAGE_TRACEQL, Expression: expression, Range: span})
		if err != nil || len(result.GetTraces().GetTraces()) != 1 || result.GetTraces().GetTraces()[0].GetTraceId() != id {
			t.Fatal("Obs TraceQL", expression, result, err)
		}
	}

	result, err := svc.QueryObs(t.Context(), &consolev1.QueryObsRequest{Signal: consolev1.ObsSignal_OBS_SIGNAL_TRACES, Language: consolev1.ObsLanguage_OBS_LANGUAGE_LOGSQL, Expression: fmt.Sprintf("trace_id:%q", id), Range: span})
	if err != nil || len(result.GetRows().GetRows()) != 2 {
		t.Fatal("Obs raw spans", result, err)
	}

	trace, err := svc.GetTrace(t.Context(), &consolev1.GetTraceRequest{TraceId: id})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"wrapper", "kratos", strconv.FormatInt(nanos, 10), strconv.FormatInt(nanos+123456789, 10)} {
		if !bytes.Contains(trace.GetTempoJson(), []byte(want)) {
			t.Fatalf("Obs trace missing %q", want)
		}
	}
}
