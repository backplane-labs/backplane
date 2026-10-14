package obs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"

	"google.golang.org/grpc/codes"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// GetTrace preserves the complete storage response, including backend extensions.
func (s *Service) GetTrace(ctx context.Context, req *consolev1.GetTraceRequest) (*consolev1.GetTraceResponse, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	id := strings.ToLower(req.GetTraceId())
	if len(id) != 32 || strings.Trim(id, "0") == "" {
		return nil, rpcError(codes.InvalidArgument, "invalid W3C trace ID")
	}

	if _, err := hex.DecodeString(id); err != nil {
		return nil, rpcError(codes.InvalidArgument, "invalid W3C trace ID")
	}

	body, info, err := s.fetch(ctx, consolev1.ObsSignal_OBS_SIGNAL_TRACES, "/select/tempo/api/v2/traces/"+id, url.Values{})
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Trace json.RawMessage `json:"trace"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Trace) == 0 || string(envelope.Trace) == "null" {
		return nil, malformed()
	}

	var trace struct {
		ResourceSpans []json.RawMessage `json:"resourceSpans"` //nolint:tagliatelle // Tempo JSON
	}
	if err := json.Unmarshal(envelope.Trace, &trace); err != nil {
		return nil, malformed()
	}

	if len(trace.ResourceSpans) == 0 {
		return nil, rpcError(codes.NotFound, "trace not found")
	}

	return &consolev1.GetTraceResponse{TraceId: id, TempoJson: body, Info: info}, nil
}
