package obs

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// GetObsCapabilities describes enabled integrations, never their credentials.
func (s *Service) GetObsCapabilities(
	context.Context, *consolev1.GetObsCapabilitiesRequest,
) (*consolev1.GetObsCapabilitiesResponse, error) {
	response := &consolev1.GetObsCapabilitiesResponse{
		MaxLimit: s.cfg.MaxLimit, MaxPoints: s.cfg.MaxPoints, MaxRangeNanos: int64(s.cfg.MaxRange),
	}
	for _, signal := range []consolev1.ObsSignal{
		consolev1.ObsSignal_OBS_SIGNAL_LOGS, consolev1.ObsSignal_OBS_SIGNAL_METRICS, consolev1.ObsSignal_OBS_SIGNAL_TRACES,
	} {
		if _, _, err := s.endpoint(signal); err != nil {
			continue
		}

		capability := &consolev1.ObsCapability{
			Signal: signal, SourceFields: sourceFields(signal),
			Languages: []consolev1.ObsLanguage{consolev1.ObsLanguage_OBS_LANGUAGE_LOGSQL},
		}
		if signal == consolev1.ObsSignal_OBS_SIGNAL_METRICS {
			capability.Languages = []consolev1.ObsLanguage{
				consolev1.ObsLanguage_OBS_LANGUAGE_METRICSQL, consolev1.ObsLanguage_OBS_LANGUAGE_PROMQL,
			}
		}

		if signal == consolev1.ObsSignal_OBS_SIGNAL_TRACES {
			capability.TraceLookup = true
			if s.cfg.TraceQL {
				capability.Languages = append(capability.Languages, consolev1.ObsLanguage_OBS_LANGUAGE_TRACEQL)
				response.TraceqlProfile = "victoriatraces-0.12-basic-search"
			}
		}

		response.Signals = append(response.Signals, capability)
	}

	return response, nil
}

func sourceFields(signal consolev1.ObsSignal) map[string]string {
	prefix := ""
	if signal == consolev1.ObsSignal_OBS_SIGNAL_TRACES {
		prefix = "resource_attr:"
	}

	return map[string]string{
		"service.namespace": prefix + "service.namespace", "service.name": prefix + "service.name",
		"service.instance.id": prefix + "service.instance.id",
	}
}

func (s *Service) bounds(span *consolev1.ObsTimeRange, limit uint32) (url.Values, uint32, error) {
	if span == nil || span.GetStartUnixNano() < 0 || span.GetEndUnixNano() <= span.GetStartUnixNano() ||
		span.GetEndUnixNano()-span.GetStartUnixNano() > int64(s.cfg.MaxRange) {
		return nil, 0, rpcError(codes.InvalidArgument, "invalid telemetry time range")
	}

	if limit == 0 {
		limit = s.cfg.DefaultLimit
	}

	if limit > s.cfg.MaxLimit {
		return nil, 0, rpcError(codes.InvalidArgument, "telemetry limit exceeds maximum")
	}

	return url.Values{
		"start":                 {time.Unix(0, span.GetStartUnixNano()).UTC().Format(time.RFC3339Nano)},
		"end":                   {time.Unix(0, span.GetEndUnixNano()).UTC().Format(time.RFC3339Nano)},
		"limit":                 {strconv.FormatUint(uint64(limit)+1, 10)},
		"timeout":               {s.cfg.Timeout.String()},
		"deny_partial_response": {"1"}, "allow_partial_response": {"0"},
	}, limit, nil
}

func (s *Service) validateQuery(req *consolev1.QueryObsRequest) error {
	if strings.TrimSpace(req.GetExpression()) == "" || len(req.GetExpression()) > s.cfg.ExpressionBytes {
		return rpcError(codes.InvalidArgument, "query expression is empty or exceeds byte budget")
	}

	if _, _, err := s.endpoint(req.GetSignal()); err != nil {
		return err
	}

	if err := s.validateLanguage(req); err != nil {
		return err
	}

	if req.GetStepNanos() < 0 || (req.GetStepNanos() != 0 &&
		req.GetSignal() != consolev1.ObsSignal_OBS_SIGNAL_METRICS && !req.GetStatistics()) {
		return rpcError(codes.InvalidArgument, "step requires a metrics or LogsQL statistics query")
	}

	return nil
}

func (s *Service) validateLanguage(req *consolev1.QueryObsRequest) error {
	switch req.GetLanguage() {
	case consolev1.ObsLanguage_OBS_LANGUAGE_LOGSQL:
		if req.GetSignal() == consolev1.ObsSignal_OBS_SIGNAL_METRICS {
			return unsupported()
		}
	case consolev1.ObsLanguage_OBS_LANGUAGE_METRICSQL, consolev1.ObsLanguage_OBS_LANGUAGE_PROMQL:
		if req.GetSignal() != consolev1.ObsSignal_OBS_SIGNAL_METRICS || req.GetStatistics() {
			return unsupported()
		}
	case consolev1.ObsLanguage_OBS_LANGUAGE_TRACEQL:
		if req.GetSignal() != consolev1.ObsSignal_OBS_SIGNAL_TRACES || req.GetStatistics() {
			return unsupported()
		}

		if !s.cfg.TraceQL {
			return rpcError(codes.FailedPrecondition, "TraceQL is disabled")
		}
	default:
		return unsupported()
	}

	return nil
}

func unsupported() error {
	return rpcError(codes.InvalidArgument, "unsupported telemetry signal/language combination")
}

// QueryObs does not translate expressions between languages.
func (s *Service) QueryObs(ctx context.Context, req *consolev1.QueryObsRequest) (*consolev1.QueryObsResponse, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := s.validateQuery(req); err != nil {
		return nil, err
	}

	params, limit, err := s.bounds(req.GetRange(), req.GetLimit())
	if err != nil {
		return nil, err
	}

	path, err := s.queryPath(req, params)
	if err != nil {
		return nil, err
	}

	body, info, err := s.fetch(ctx, req.GetSignal(), path, params)
	if err != nil {
		return nil, err
	}

	response := &consolev1.QueryObsResponse{Info: info}

	switch {
	case req.GetSignal() == consolev1.ObsSignal_OBS_SIGNAL_METRICS || req.GetStatistics():
		series, parseErr := parseSeries(body, limit, s.cfg.MaxPoints, info)
		if parseErr != nil {
			return nil, parseErr
		}

		response.Result = &consolev1.QueryObsResponse_TimeSeries{TimeSeries: series}
	case req.GetLanguage() == consolev1.ObsLanguage_OBS_LANGUAGE_TRACEQL:
		traces, parseErr := parseTraces(body, limit, info)
		if parseErr != nil {
			return nil, parseErr
		}

		response.Result = &consolev1.QueryObsResponse_Traces{Traces: traces}
	default:
		rows, parseErr := parseRows(body, limit, info)
		if parseErr != nil {
			return nil, parseErr
		}

		response.Result = &consolev1.QueryObsResponse_Rows{Rows: rows}
	}

	return response, nil
}

func (s *Service) queryPath(req *consolev1.QueryObsRequest, params url.Values) (string, error) {
	params.Set("query", req.GetExpression())

	if req.GetLanguage() == consolev1.ObsLanguage_OBS_LANGUAGE_TRACEQL {
		params.Del("query")
		params.Set("q", req.GetExpression())
		// Tempo accepts Unix seconds; retain fractional precision in the request.
		params.Set("start", seconds(req.GetRange().GetStartUnixNano()))
		params.Set("end", seconds(req.GetRange().GetEndUnixNano()))

		return "/select/tempo/api/search", nil
	}

	path := "/select/logsql/query"
	if req.GetSignal() == consolev1.ObsSignal_OBS_SIGNAL_METRICS {
		path = "/api/v1/query"
	}

	if req.GetStatistics() {
		path = "/select/logsql/stats_query"
	}

	if req.GetSignal() != consolev1.ObsSignal_OBS_SIGNAL_METRICS && !req.GetStatistics() {
		return path, nil
	}

	params.Set("time", params.Get("end"))

	if req.GetStepNanos() == 0 {
		return path, nil
	}

	points := (req.GetRange().GetEndUnixNano()-req.GetRange().GetStartUnixNano())/req.GetStepNanos() + 1
	if points > int64(s.cfg.MaxPoints) {
		return "", rpcError(codes.InvalidArgument, "range query exceeds point budget; increase step")
	}

	params.Set("step", seconds(req.GetStepNanos()))

	if req.GetStatistics() {
		params.Set("step", time.Duration(req.GetStepNanos()).String())
	}

	path += "_range"

	return path, nil
}

// seconds formats integer nanoseconds without a float64 round trip.
func seconds(nanos int64) string {
	decimal := fmt.Sprintf("%d.%09d", nanos/int64(time.Second), nanos%int64(time.Second))
	return strings.TrimRight(strings.TrimRight(decimal, "0"), ".")
}
