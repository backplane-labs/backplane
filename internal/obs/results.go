//nolint:tagliatelle // Native Victoria and Tempo JSON use camelCase.
package obs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"google.golang.org/grpc/codes"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

const (
	backendSuccess = "success"
	sampleFields   = 2
)

func malformed() error { return rpcError(codes.Unavailable, "invalid telemetry backend response") }

func parseRows(body []byte, limit uint32, info *consolev1.ObsResultInfo) (*consolev1.ObsRows, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	result := &consolev1.ObsRows{}

	for {
		var row map[string]string
		if err := decoder.Decode(&row); errors.Is(err, io.EOF) {
			break
		} else if err != nil || row == nil {
			return nil, malformed()
		}

		if len(result.GetRows()) == int(limit) {
			info.Truncated = true
			break
		}

		result.Rows = append(result.Rows, &consolev1.ObsRow{Fields: row})
	}

	return result, nil
}

type seriesEnvelope struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
	Warnings []string `json:"warnings"`
	Partial  bool     `json:"isPartial"`
}

type rawSeries struct {
	Metric map[string]string   `json:"metric"`
	Value  []json.RawMessage   `json:"value"`
	Values [][]json.RawMessage `json:"values"`
}

func parseSeries(body []byte, limit, points uint32, info *consolev1.ObsResultInfo) (*consolev1.ObsTimeSeries, error) {
	var envelope seriesEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Status != backendSuccess {
		return nil, malformed()
	}

	info.Partial = info.GetPartial() || envelope.Partial
	info.Warnings = append(info.GetWarnings(), envelope.Warnings...)
	result := &consolev1.ObsTimeSeries{ResultType: envelope.Data.ResultType}

	var series []rawSeries

	switch envelope.Data.ResultType {
	case "matrix", "vector":
		if err := json.Unmarshal(envelope.Data.Result, &series); err != nil {
			return nil, malformed()
		}
	case "scalar", "string":
		var value []json.RawMessage
		if err := json.Unmarshal(envelope.Data.Result, &value); err != nil {
			return nil, malformed()
		}

		series = []rawSeries{{Value: value}}
	default:
		return nil, malformed()
	}

	if len(series) > int(limit) {
		info.Truncated = true
		series = series[:limit]
	}

	for _, item := range series {
		if len(item.Value) != 0 {
			item.Values = append(item.Values, item.Value)
		}

		if len(item.Values) > int(points) {
			return nil, rpcError(codes.ResourceExhausted, "telemetry response exceeds point budget")
		}

		points -= uint32(len(item.Values)) //nolint:gosec // bounded above

		output := &consolev1.ObsSeries{Labels: item.Metric}
		for _, pair := range item.Values {
			sample, err := parseSample(pair)
			if err != nil {
				return nil, err
			}

			output.Samples = append(output.Samples, sample)
		}

		result.Series = append(result.Series, output)
	}

	return result, nil
}

func parseSample(pair []json.RawMessage) (*consolev1.ObsSample, error) {
	if len(pair) != sampleFields {
		return nil, malformed()
	}

	var (
		timestamp json.Number
		value     string
	)

	if err := json.Unmarshal(pair[0], &timestamp); err != nil || timestamp == "" {
		return nil, malformed()
	}

	if err := json.Unmarshal(pair[1], &value); err != nil {
		return nil, malformed()
	}

	return &consolev1.ObsSample{TimestampSeconds: timestamp.String(), Value: value}, nil
}

func parseTraces(body []byte, limit uint32, info *consolev1.ObsResultInfo) (*consolev1.ObsTraceSearch, error) {
	var envelope struct {
		Traces []json.RawMessage `json:"traces"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || !bytes.Contains(body, []byte(`"traces"`)) {
		return nil, malformed()
	}

	if len(envelope.Traces) > int(limit) {
		info.Truncated = true
		envelope.Traces = envelope.Traces[:limit]
	}

	result := &consolev1.ObsTraceSearch{}

	for _, raw := range envelope.Traces {
		var trace struct {
			ID       string      `json:"traceID"`
			Service  string      `json:"rootServiceName"`
			Name     string      `json:"rootTraceName"`
			Start    json.Number `json:"startTimeUnixNano"`
			Duration json.Number `json:"durationMs"`
		}
		if err := json.Unmarshal(raw, &trace); err != nil || trace.ID == "" {
			return nil, malformed()
		}

		result.Traces = append(result.Traces, &consolev1.ObsTraceSummary{
			TraceId:     trace.ID,
			RootService: trace.Service, RootName: trace.Name,
			StartUnixNano: trace.Start.String(), DurationMillis: trace.Duration.String(), Json: raw,
		})
	}

	return result, nil
}
