package obs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/registry"
)

// Service owns a bounded HTTP connection pool, not telemetry storage/lifecycle.
type Service struct {
	consolev1.UnimplementedObsServiceServer
	cfg      Config
	client   *http.Client
	slots    chan struct{}
	registry registry.Source
}

// New does not contact storage; its outage never gates platform startup.
func New(cfg Config, options ...Option) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	cfg = cfg.defaults()

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("%w: default HTTP transport is not configurable", errConfig)
	}

	transport := base.Clone()
	transport.MaxConnsPerHost = cfg.Concurrent
	transport.MaxIdleConnsPerHost = cfg.Concurrent

	svc := &Service{cfg: cfg, slots: make(chan struct{}, cfg.Concurrent), client: &http.Client{
		Transport: transport, Timeout: cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	for _, option := range options {
		option(svc)
	}

	return svc, nil
}

// Register is shared by the authenticated console and SDK internal listener.
func (s *Service) Register(registrar grpc.ServiceRegistrar) {
	consolev1.RegisterObsServiceServer(registrar, s)
}

// Close releases idle HTTP connections.
func (s *Service) Close() { s.client.CloseIdleConnections() }

func (s *Service) endpoint(signal consolev1.ObsSignal) (string, string, error) {
	var endpoint, authorization string

	switch signal {
	case consolev1.ObsSignal_OBS_SIGNAL_LOGS:
		endpoint, authorization = s.cfg.LogsURL, s.cfg.LogsAuthorization.Reveal()
	case consolev1.ObsSignal_OBS_SIGNAL_METRICS:
		endpoint, authorization = s.cfg.MetricsURL, s.cfg.MetricsAuthorization.Reveal()
	case consolev1.ObsSignal_OBS_SIGNAL_TRACES:
		endpoint, authorization = s.cfg.TracesURL, s.cfg.TracesAuthorization.Reveal()
	default:
		return "", "", rpcError(codes.InvalidArgument, "unknown telemetry signal")
	}

	if endpoint == "" {
		return "", "", rpcError(codes.FailedPrecondition, "telemetry backend is not configured")
	}

	return strings.TrimRight(endpoint, "/"), authorization, nil
}

func (s *Service) fetch(
	ctx context.Context,
	signal consolev1.ObsSignal,
	path string,
	params url.Values,
) ([]byte, *consolev1.ObsResultInfo, error) {
	endpoint, authorization, err := s.endpoint(signal)
	if err != nil {
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+path+"?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, nil, rpcError(codes.Internal, "cannot construct telemetry request")
	}

	req.Header.Set("Accept", "application/json")

	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, transportError(ctx, err)
	}
	defer resp.Body.Close()

	if err := responseStatus(resp.StatusCode); err != nil {
		return nil, nil, err
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, s.cfg.ResponseBytes+1))
	if err != nil {
		return nil, nil, transportError(ctx, err)
	}

	if int64(len(body)) > s.cfg.ResponseBytes {
		return nil, nil, rpcError(codes.ResourceExhausted, "telemetry response exceeds byte budget")
	}

	return body, &consolev1.ObsResultInfo{Partial: resp.StatusCode == http.StatusPartialContent}, nil
}

func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return rpcError(status.Code(status.FromContextError(ctx.Err()).Err()), ctx.Err().Error())
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return rpcError(codes.DeadlineExceeded, "telemetry query timed out")
	}

	return rpcError(codes.Unavailable, "telemetry backend request failed")
}

// Backend response bodies may include secrets, URLs or internal topology. They
// are deliberately not echoed into RPC errors (including parser diagnostics).
func responseStatus(code int) error {
	switch code {
	case http.StatusOK, http.StatusPartialContent:
		return nil
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return rpcError(codes.InvalidArgument, "backend rejected telemetry query")
	case http.StatusNotFound:
		return rpcError(codes.NotFound, "telemetry resource not found")
	case http.StatusTooManyRequests:
		return rpcError(codes.ResourceExhausted, "telemetry backend is saturated")
	case http.StatusGatewayTimeout, http.StatusRequestTimeout:
		return rpcError(codes.DeadlineExceeded, "telemetry backend timed out")
	default:
		return rpcError(codes.Unavailable, "telemetry backend is unavailable")
	}
}

// RPC boundary errors must retain their transport status.
func rpcError(code codes.Code, message string) error {
	return status.Error(code, message) //nolint:wrapcheck // transport status is the public error contract
}

// acquire covers HTTP and decoding, so large responses cannot leave an unbounded
// number of parsers outside the per-replica concurrency budget.
func (s *Service) acquire(ctx context.Context) (func(), error) {
	if ctx.Err() != nil {
		return nil, transportError(ctx, ctx.Err())
	}

	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, nil
	default:
		return nil, rpcError(codes.ResourceExhausted, "telemetry query capacity reached")
	}
}
