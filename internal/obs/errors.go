package obs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/obs/logsql"
)

// Stored fields of a log record as VictoriaLogs keeps them: the OTel log
// data model and the browser SDK's app.debug.*.
const (
	fTime    logsql.Field = "_time"
	fTrace   logsql.Field = "trace_id"
	fSpan    logsql.Field = "span_id"
	fService logsql.Field = "service.name"
	fRuntime logsql.Field = "app.debug.runtime.id"
)

const (
	defaultRelatedPage = 50
	maxRelatedPage     = 200
	maxRelatedRange    = 31 * 24 * time.Hour
	relatedWindow      = 15 * time.Minute
	statusAvailable    = "available"
	statusPartial      = "partial"
	statusNotFound     = "not_found"
	statusUnavailable  = "unavailable"
)

// Errors reads what the log store holds around an error occurrence kept
// in the durable error store: the logs of its span, trace, SDK runtime or
// service window.
type Errors struct {
	obs *Service
}

// Errors is the related-logs reader over s's stores.
func (s *Service) Errors() *Errors { return &Errors{obs: s} }

type row map[string]string

func invalid(message string) error { return rpcError(codes.InvalidArgument, message) }

// Available reports whether a log store is configured.
func (e *Errors) Available() bool { return e.obs.cfg.LogsURL != "" }

// Logs reads logs related to o: by the request's relation and range, else
// a window around when.
func (e *Errors) Logs(
	ctx context.Context, req *consolev1.RelatedLogsRequest, o *consolev1.ErrorOccurrence, when time.Time,
) (*consolev1.RelatedLogsResponse, error) {
	size := int(req.GetPageSize())
	if size == 0 {
		size = defaultRelatedPage
	}

	if size > maxRelatedPage {
		return nil, invalid("related page exceeds maximum")
	}

	start, end, err := relatedRange(req, when)
	if err != nil {
		return nil, err
	}

	key := relation(req.GetRelation(), o)
	if key.IsZero() {
		if req.GetRelation() == consolev1.ErrorRelation_ERROR_RELATION_UNSPECIFIED {
			return nil, invalid("unknown relation")
		}

		return &consolev1.RelatedLogsResponse{Status: statusNotFound, Reason: "correlation_key_absent"}, nil
	}

	related, partial, err := e.logs(ctx, logsql.From(logsql.And(logsql.Range(start, end), key)).
		Sort(size+1, logsql.Order{Field: fTime}), size+1)
	if err != nil {
		//nolint:nilerr // the log store's failure is the response's state
		return &consolev1.RelatedLogsResponse{Status: statusUnavailable, Reason: "log_store_unavailable"}, nil
	}

	return relatedResponse(related, partial, size), nil
}

// relatedRange is the request's range, else a window around when.
func relatedRange(req *consolev1.RelatedLogsRequest, when time.Time) (time.Time, time.Time, error) {
	if req.GetStart() == nil && req.GetEnd() == nil {
		return when.Add(-relatedWindow), when.Add(relatedWindow), nil
	}

	start, end := req.GetStart().AsTime(), req.GetEnd().AsTime()
	if req.GetStart().CheckValid() != nil || req.GetEnd().CheckValid() != nil || !end.After(start) ||
		end.Sub(start) > maxRelatedRange {
		return start, end, invalid("invalid related range")
	}

	return start, end, nil
}

func relation(kind consolev1.ErrorRelation, o *consolev1.ErrorOccurrence) logsql.Filter {
	switch kind {
	case consolev1.ErrorRelation_ERROR_RELATION_SAME_SPAN:
		if o.GetTraceId() != "" && o.GetSpanId() != "" {
			return logsql.And(fTrace.Eq(o.GetTraceId()), fSpan.Eq(o.GetSpanId()))
		}
	case consolev1.ErrorRelation_ERROR_RELATION_SAME_TRACE:
		if o.GetTraceId() != "" {
			return fTrace.Eq(o.GetTraceId())
		}
	case consolev1.ErrorRelation_ERROR_RELATION_SAME_RUNTIME:
		if o.GetRuntimeId() != "" {
			return fRuntime.Eq(o.GetRuntimeId())
		}
	case consolev1.ErrorRelation_ERROR_RELATION_TIME_WINDOW:
		if o.GetService() != "" {
			return fService.Eq(o.GetService())
		}

		return logsql.All()
	default:
	}

	return logsql.Filter{}
}

func relatedResponse(rows []row, partial bool, size int) *consolev1.RelatedLogsResponse {
	response := &consolev1.RelatedLogsResponse{Status: statusAvailable}
	if len(rows) == 0 {
		return &consolev1.RelatedLogsResponse{Status: statusNotFound}
	}

	if len(rows) > size {
		rows, partial = rows[:size], true
		response.Reason = "row_limit"
	}

	for _, r := range rows {
		when, err := time.Parse(time.RFC3339Nano, r[string(fTime)])
		if err != nil || when.UnixNano() < 0 {
			partial, response.Reason = true, "invalid_stored_time"

			continue
		}

		response.Logs = append(response.Logs, &consolev1.RelatedLog{Time: timestamppb.New(when), Fields: r})
	}

	if partial {
		response.Status = statusPartial
		if response.GetReason() == "" {
			response.Reason = "upstream_partial"
		}
	}

	return response
}

// logs runs query and reads at most limit rows; more is partial.
func (e *Errors) logs(ctx context.Context, query logsql.Query, limit int) ([]row, bool, error) {
	release, err := e.obs.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()

	body, info, err := e.obs.fetch(ctx, consolev1.ObsSignal_OBS_SIGNAL_LOGS, "/select/logsql/query",
		url.Values{"query": {query.String()}})
	if err != nil {
		return nil, false, err
	}

	rows, err := readRows(body, limit)
	if errors.Is(err, errRowBudget) {
		return rows, true, nil
	}

	return rows, info.GetPartial(), err
}

var errRowBudget = errors.New("row budget")

func readRows(body []byte, limit int) ([]row, error) {
	reader := bufio.NewReader(bytes.NewReader(body))
	rows := []row{}

	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var value row
			if json.Unmarshal(line, &value) != nil || value == nil {
				return nil, malformed()
			}

			if len(rows) == limit {
				return rows, errRowBudget
			}

			rows = append(rows, value)
		}

		if errors.Is(err, io.EOF) {
			return rows, nil
		}

		if err != nil {
			return nil, malformed()
		}
	}
}
