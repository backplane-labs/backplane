// Package upstream forwards the exception records an installation accepted
// to a configured OTLP/HTTP logs endpoint: typically a service of the
// installation, reached over the mesh's mutual TLS, that passes them on to
// the vendor (where errors group across installations by tenant.id).
// Records leave as they came (resource, scope and log record unchanged),
// through a bounded queue that never blocks local ingestion; what cannot
// be delivered is dropped and counted.
package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/tracehttp"
)

// Settings of the forwarding: BACKPLANE_ERRORS_UPSTREAM_*.
type Settings struct {
	// URL is the receiver's OTLP/HTTP logs endpoint
	// (https://core.service.consul:8443/v1/logs). Empty: nothing is
	// forwarded and no forwarder runs (an air-gapped installation leaves it
	// empty).
	URL string `json:"url,omitempty"`
	// Service is the receiver's mesh identity: set, the request goes over
	// mutual TLS (this installation's certificate, the server verified as
	// that service) and never through the environment's proxy; empty, a
	// plain HTTP(S) request.
	Service string `json:"service,omitempty"`
	// Enabled is the operator's switch, live from the console: off drops
	// the queue and forwards nothing until switched on again.
	Enabled config.Live[bool] `json:"enabled" schemapb:"default=true"`
	// Queue bounds the records waiting to be sent; past it new ones are
	// dropped and counted.
	Queue int `json:"queue" schemapb:"default=10000;gt=0"`
	// Batch is the most records one request carries.
	Batch int `json:"batch" schemapb:"default=512;gt=0"`
	// Flush is how long a record waits for a fuller batch.
	Flush time.Duration `json:"flush" schemapb:"default=5s;gt=0"`
	// Timeout bounds one request.
	Timeout time.Duration `json:"timeout" schemapb:"default=10s;gt=0"`
	// GiveUp is how long a batch is retried (network failures, 408, 429,
	// 5xx; Retry-After is honored) before it is dropped.
	GiveUp time.Duration `json:"give_up" schemapb:"default=15m;gt=0"`
}

// Errors of Validate.
var (
	ErrURL     = errors.New("errors.upstream.url: want an absolute http(s) URL")
	ErrMeshURL = errors.New("errors.upstream.url: want https with errors.upstream.service (mutual TLS)")
)

// Validate checks what the schema cannot.
func (s *Settings) Validate() error {
	if s.URL == "" {
		return nil
	}

	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ErrURL
	}

	if s.Service != "" && u.Scheme != "https" {
		return ErrMeshURL
	}

	return nil
}

// Record is one accepted exception record with where it came from: the
// messages as received, never modified.
type Record struct {
	Resource          *resourcepb.Resource
	ResourceSchemaURL string
	Scope             *commonpb.InstrumentationScope
	ScopeSchemaURL    string
	Log               *logspb.LogRecord
}

// Drop reasons, the bounded values of the reason label.
const (
	reasonFull    = "full"
	reasonOff     = "off"
	reasonRefused = "refused"
	reasonExpired = "expired"
)

const (
	contentType   = "application/x-protobuf"
	responseBytes = 64 << 10
	minRetry      = time.Second
	maxRetry      = time.Minute
)

// Forwarder sends accepted records to the upstream.
type Forwarder struct {
	deps.Component

	settings Settings
	queue    chan Record
	client   *http.Client
	records  metric.Int64Counter
	// refused remembers the last refusing status, logged once per change.
	refused int
}

// Option configures New.
type Option func(*Forwarder)

// WithTransport sets the transport: the mesh's mutual TLS when
// settings.Service names the receiver; http.DefaultTransport otherwise.
func WithTransport(rt http.RoundTripper) Option {
	return func(f *Forwarder) { f.client.Transport = rt }
}

// New creates the forwarder of settings under parent; settings.URL must be
// set (an installation without one assembles None).
func New(parent deps.Scope, settings Settings, opts ...Option) *Forwarder {
	f := &Forwarder{
		Component: deps.NewComponent(parent, "errors-upstream"),
		settings:  settings,
		queue:     make(chan Record, settings.Queue),
		client:    &http.Client{Transport: http.DefaultTransport, Timeout: settings.Timeout},
	}
	for _, o := range opts {
		o(f)
	}

	// Each export is a client span carrying its trace context: the upstream
	// is the operator's own receiver, part of the installation's tracing.
	f.client.Transport = tracehttp.NewTransport(f.client.Transport,
		tracehttp.WithPropagator(otel.GetTextMapPropagator()))

	f.records, _ = f.Meter().Int64Counter("backplane.errors.upstream.records",
		metric.WithDescription("Error records forwarded upstream, by outcome (sent, dropped) and drop reason"))
	_, _ = f.Meter().Int64ObservableGauge("backplane.errors.upstream.queue",
		metric.WithDescription("Error records waiting to be forwarded upstream"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(len(f.queue)))

			return nil
		}))

	f.settings.Enabled.Watch(func(on bool) {
		if !on {
			f.drain(context.Background(), reasonOff)
		}
	})

	f.Go(f.run)

	return f
}

// Offer queues accepted records without blocking: when the switch is off
// or the queue is full they are dropped and counted.
func (f *Forwarder) Offer(ctx context.Context, records ...Record) {
	if !f.settings.Enabled.Get() {
		f.drop(ctx, reasonOff, len(records))

		return
	}

	for _, r := range records {
		select {
		case f.queue <- r:
		default:
			f.drop(ctx, reasonFull, 1)
		}
	}
}

// run batches the queue and sends each batch until the node stops; what
// is left is dropped then.
func (f *Forwarder) run(ctx context.Context) error {
	timer := time.NewTimer(f.settings.Flush)
	defer timer.Stop()

	batch := make([]Record, 0, f.settings.Batch)

	for {
		select {
		case <-ctx.Done():
			f.flush(context.WithoutCancel(ctx), batch)

			return nil
		case r := <-f.queue:
			batch = append(batch, r)
			if len(batch) < f.settings.Batch {
				continue
			}
		case <-timer.C:
		}

		if len(batch) > 0 {
			f.send(ctx, batch)
			batch = batch[:0]
		}

		timer.Reset(f.settings.Flush)
	}
}

// flush makes one attempt, within one request timeout, to deliver what is
// left when the node stops; what does not make it is dropped.
func (f *Forwarder) flush(ctx context.Context, batch []Record) {
	ctx, cancel := context.WithTimeout(ctx, f.settings.Timeout)
	defer cancel()

	for {
		for len(batch) < f.settings.Batch {
			select {
			case r := <-f.queue:
				batch = append(batch, r)

				continue
			default:
			}

			break
		}

		if len(batch) == 0 {
			return
		}

		if ctx.Err() != nil {
			f.drop(ctx, reasonExpired, len(batch))
			f.drain(ctx, reasonExpired)

			return
		}

		f.send(ctx, batch)
		batch = batch[:0]
	}
}

// drain drops what is queued.
func (f *Forwarder) drain(ctx context.Context, reason string) {
	for {
		select {
		case <-f.queue:
			f.drop(ctx, reason, 1)
		default:
			return
		}
	}
}

// send delivers one batch with retries; a batch that cannot be delivered
// is dropped and counted.
func (f *Forwarder) send(ctx context.Context, batch []Record) {
	body, err := encode(batch)
	if err != nil {
		f.Log().Error("errors upstream: encode", xlog.Err(err))
		f.drop(ctx, reasonRefused, len(batch))

		return
	}

	_, err = backoff.Retry(ctx, func() (struct{}, error) {
		if !f.settings.Enabled.Get() {
			return struct{}{}, fmt.Errorf("errors upstream: %w", backoff.Permanent(errOff))
		}

		return struct{}{}, f.post(ctx, body)
	},
		backoff.WithBackOff(&backoff.ExponentialBackOff{
			InitialInterval: minRetry, RandomizationFactor: backoff.DefaultRandomizationFactor,
			Multiplier: backoff.DefaultMultiplier, MaxInterval: maxRetry,
		}),
		backoff.WithMaxElapsedTime(f.settings.GiveUp),
	)

	var refused *refusedError

	switch {
	case err == nil:
		f.count(ctx, "sent", "", len(batch))
	case errors.Is(err, errOff):
		f.drop(ctx, reasonOff, len(batch))
	case errors.As(err, &refused):
		if refused.status != f.refused {
			f.refused = refused.status
			f.Log().Warn("errors upstream refused records", xlog.Int("status", refused.status),
				xlog.String("detail", refused.detail))
		}

		f.drop(ctx, reasonRefused, len(batch))
	default:
		f.Log().Warn("errors upstream unavailable, batch dropped", xlog.Err(err))
		f.drop(ctx, reasonExpired, len(batch))
	}
}

var (
	errOff    = errors.New("errors upstream switched off")
	errStatus = errors.New("errors upstream: status")
)

// refusedError is a status the upstream will answer again: not retried.
type refusedError struct {
	status int
	detail string
}

func (e *refusedError) Error() string { return "upstream refused: " + strconv.Itoa(e.status) }

// post sends the body once: nil on 2xx, a retryable error on network
// failures, 408, 429 and 5xx (with Retry-After), permanent otherwise.
func (f *Forwarder) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.settings.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("errors upstream request: %w", backoff.Permanent(err))
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("errors upstream: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	detail, _ := io.ReadAll(io.LimitReader(resp.Body, responseBytes))

	switch status := resp.StatusCode; {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return nil
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError:
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			return fmt.Errorf("%w %d: %w", errStatus, status, backoff.RetryAfter(seconds))
		}

		return fmt.Errorf("%w %d", errStatus, status)
	default:
		return fmt.Errorf("errors upstream: %w", backoff.Permanent(&refusedError{status: status, detail: string(detail)}))
	}
}

// encode is the batch as one gzipped OTLP export request: consecutive
// records of one resource and scope share their ResourceLogs and
// ScopeLogs, every message as received.
func encode(batch []Record) ([]byte, error) {
	export := &collogspb.ExportLogsServiceRequest{}

	var (
		resource *logspb.ResourceLogs
		scope    *logspb.ScopeLogs
	)

	for _, r := range batch {
		if resource == nil || resource.GetResource() != r.Resource || resource.GetSchemaUrl() != r.ResourceSchemaURL {
			resource = &logspb.ResourceLogs{Resource: r.Resource, SchemaUrl: r.ResourceSchemaURL}
			export.ResourceLogs = append(export.ResourceLogs, resource)
			scope = nil
		}

		if scope == nil || scope.GetScope() != r.Scope || scope.GetSchemaUrl() != r.ScopeSchemaURL {
			scope = &logspb.ScopeLogs{Scope: r.Scope, SchemaUrl: r.ScopeSchemaURL}
			resource.ScopeLogs = append(resource.ScopeLogs, scope)
		}

		scope.LogRecords = append(scope.LogRecords, r.Log)
	}

	raw, err := proto.Marshal(export)
	if err != nil {
		return nil, fmt.Errorf("marshal export: %w", err)
	}

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, fmt.Errorf("gzip export: %w", err)
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip export: %w", err)
	}

	return buf.Bytes(), nil
}

func (f *Forwarder) drop(ctx context.Context, reason string, n int) {
	f.count(ctx, "dropped", reason, n)
}

func (f *Forwarder) count(ctx context.Context, outcome, reason string, n int) {
	if n <= 0 || f.records == nil {
		return
	}

	attrs := []attribute.KeyValue{attribute.String("outcome", outcome)}
	if reason != "" {
		attrs = append(attrs, attribute.String("reason", reason))
	}

	f.records.Add(ctx, int64(n), metric.WithAttributes(attrs...))
}

// None forwards nothing: the installation has no upstream.
type None struct{}

// Offer drops the records.
func (None) Offer(context.Context, ...Record) {}
