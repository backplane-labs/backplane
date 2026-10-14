//nolint:tagliatelle // Native Victoria and Tempo JSON use camelCase.
package obs

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// ListObsSources never consults module registration or declarations.
func (s *Service) ListObsSources(
	ctx context.Context,
	req *consolev1.ListObsSourcesRequest,
) (*consolev1.ListObsSourcesResponse, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	params, limit, err := s.bounds(req.GetRange(), req.GetLimit())
	if err != nil {
		return nil, err
	}

	fields := sourceFields(req.GetSignal())
	path := "/select/logsql/query"

	params.Set("query", "* | stats by ("+strconv.Quote(fields["service.namespace"])+","+
		strconv.Quote(fields["service.name"])+","+strconv.Quote(fields["service.instance.id"])+") count()")

	if req.GetSignal() == consolev1.ObsSignal_OBS_SIGNAL_METRICS {
		path = "/api/v1/series"

		params.Del("query")
		params.Set("match[]", `{__name__!=""}`)
	}

	body, info, err := s.fetch(ctx, req.GetSignal(), path, params)
	if err != nil {
		return nil, err
	}

	rows, err := sourceRows(body, req.GetSignal(), limit, info)
	if err != nil {
		return nil, err
	}

	response := &consolev1.ListObsSourcesResponse{Info: info}

	seen := make(map[string]bool)
	for _, row := range rows {
		resource := make(map[string]string)

		for attribute, field := range fields {
			if value, ok := row[field]; ok && value != "" {
				resource[attribute] = value
			}
		}

		encoded, _ := json.Marshal(resource) //nolint:errchkjson // map of strings cannot fail
		if !seen[string(encoded)] {
			response.Sources = append(response.Sources, &consolev1.ObsSource{Resource: resource})
			seen[string(encoded)] = true
		}
	}

	return response, nil
}

func sourceRows(
	body []byte,
	signal consolev1.ObsSignal,
	limit uint32,
	info *consolev1.ObsResultInfo,
) ([]map[string]string, error) {
	if signal == consolev1.ObsSignal_OBS_SIGNAL_METRICS {
		var envelope struct {
			Status   string              `json:"status"`
			Data     []map[string]string `json:"data"`
			Partial  bool                `json:"isPartial"`
			Warnings []string            `json:"warnings"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Status != backendSuccess {
			return nil, malformed()
		}

		info.Partial = info.GetPartial() || envelope.Partial

		info.Warnings = append(info.GetWarnings(), envelope.Warnings...)
		if len(envelope.Data) > int(limit) {
			info.Truncated = true
			envelope.Data = envelope.Data[:limit]
		}

		return envelope.Data, nil
	}

	result, err := parseRows(body, limit, info)
	if err != nil {
		return nil, err
	}

	rows := make([]map[string]string, 0, len(result.GetRows()))
	for _, row := range result.GetRows() {
		rows = append(rows, row.GetFields())
	}

	return rows, nil
}

// ListObsFields lists native backend field names (not a closed OTel schema).
func (s *Service) ListObsFields(
	ctx context.Context,
	req *consolev1.ListObsFieldsRequest,
) (*consolev1.ListObsFieldsResponse, error) {
	values, info, err := s.discover(ctx, req.GetSignal(), req.GetRange(), req.GetLimit(), req.GetFilter(), "")
	if err != nil {
		return nil, err
	}

	return &consolev1.ListObsFieldsResponse{Fields: values, Info: info}, nil
}

// ListObsFieldValues returns a bounded suggestion set, not a pagination cursor.
func (s *Service) ListObsFieldValues(
	ctx context.Context,
	req *consolev1.ListObsFieldValuesRequest,
) (*consolev1.ListObsFieldValuesResponse, error) {
	if strings.TrimSpace(req.GetField()) == "" || len(req.GetField()) > s.cfg.ExpressionBytes {
		return nil, rpcError(codes.InvalidArgument, "field is empty or exceeds byte budget")
	}

	if req.GetSignal() == consolev1.ObsSignal_OBS_SIGNAL_METRICS &&
		(strings.ContainsAny(req.GetField(), `/\`) || req.GetField() == "." || req.GetField() == "..") {
		return nil, rpcError(codes.InvalidArgument, "metric field is not a valid label path")
	}

	values, info, err := s.discover(ctx, req.GetSignal(), req.GetRange(), req.GetLimit(), req.GetFilter(), req.GetField())
	if err != nil {
		return nil, err
	}

	return &consolev1.ListObsFieldValuesResponse{Values: values, Info: info}, nil
}

func (s *Service) discover(
	ctx context.Context,
	signal consolev1.ObsSignal,
	span *consolev1.ObsTimeRange,
	limit uint32,
	filter, field string,
) ([]string, *consolev1.ObsResultInfo, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer release()

	params, limit, err := s.bounds(span, limit)
	if err != nil {
		return nil, nil, err
	}

	if len(filter) > s.cfg.ExpressionBytes {
		return nil, nil, rpcError(codes.InvalidArgument, "filter exceeds byte budget")
	}

	path := discoveryPath(signal, filter, field, params)

	body, info, err := s.fetch(ctx, signal, path, params)
	if err != nil {
		return nil, nil, err
	}

	values, err := parseDiscovery(body, signal, info)
	if err != nil {
		return nil, nil, err
	}

	if len(values) > int(limit) {
		info.Truncated = true
		values = values[:limit]
	}

	slices.Sort(values)

	return values, info, nil
}

func discoveryPath(signal consolev1.ObsSignal, filter, field string, params url.Values) string {
	if signal == consolev1.ObsSignal_OBS_SIGNAL_METRICS {
		if filter == "" {
			filter = `{__name__!=""}`
		}

		params.Set("match[]", filter)

		if field != "" {
			return "/api/v1/label/" + url.PathEscape(field) + "/values"
		}

		return "/api/v1/labels"
	}

	if filter == "" {
		filter = "*"
	}

	params.Set("query", filter)

	if field != "" {
		params.Set("field", field)
		return "/select/logsql/field_values"
	}

	return "/select/logsql/field_names"
}

func parseDiscovery(body []byte, signal consolev1.ObsSignal, info *consolev1.ObsResultInfo) ([]string, error) {
	if signal == consolev1.ObsSignal_OBS_SIGNAL_METRICS {
		var envelope struct {
			Status   string   `json:"status"`
			Data     []string `json:"data"`
			Warnings []string `json:"warnings"`
			Partial  bool     `json:"isPartial"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Status != backendSuccess {
			return nil, malformed()
		}

		info.Partial = info.GetPartial() || envelope.Partial
		info.Warnings = append(info.GetWarnings(), envelope.Warnings...)

		return envelope.Data, nil
	}

	var envelope struct {
		Values []struct {
			Value string `json:"value"`
		} `json:"values"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Values == nil {
		return nil, malformed()
	}

	values := make([]string, 0, len(envelope.Values))
	for _, value := range envelope.Values {
		values = append(values, value.Value)
	}

	return values, nil
}
