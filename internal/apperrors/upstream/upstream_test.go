package upstream_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/backplane-labs/backplane/internal/apperrors/upstream"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

const eventually = 5 * time.Second

// receiver is the upstream double: an OTLP/HTTP logs endpoint over TLS
// that checks every request's shape and keeps the exports.
type receiver struct {
	t      *testing.T
	server *httptest.Server
	// answer is the status of each request in turn; past its end 200.
	answer []int
	// hold blocks the first request until closed (nil: no hold).
	hold chan struct{}

	mu      sync.Mutex
	exports []*collogspb.ExportLogsServiceRequest
	calls   int
	bodies  [][]byte
	got     chan struct{}
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()

	v := &receiver{t: t, got: make(chan struct{}, 64)}
	v.server = httptest.NewTLSServer(http.HandlerFunc(v.serve))
	t.Cleanup(v.server.Close)

	return v
}

func (v *receiver) serve(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	call := v.calls
	v.calls++
	hold := v.hold
	v.mu.Unlock()

	if call == 0 && hold != nil {
		<-hold
	}

	if r.Method != http.MethodPost || r.URL.Path != "/v1/logs" {
		v.t.Errorf("request %d: %s %s", call, r.Method, r.URL.Path)
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		v.t.Errorf("read body: %v", err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil || r.Header.Get("Content-Encoding") != "gzip" || r.Header.Get("Content-Type") != "application/x-protobuf" {
		v.t.Errorf("request %d: encoding %q type %q: %v", call, r.Header.Get("Content-Encoding"),
			r.Header.Get("Content-Type"), err)
	}

	plain, _ := io.ReadAll(zr)
	export := &collogspb.ExportLogsServiceRequest{}

	if err := proto.Unmarshal(plain, export); err != nil {
		v.t.Errorf("request %d: %v", call, err)
	}

	status := http.StatusOK
	if call < len(v.answer) {
		status = v.answer[call]
	}

	v.mu.Lock()
	v.bodies = append(v.bodies, raw)
	if status == http.StatusOK {
		v.exports = append(v.exports, export)
	}
	v.mu.Unlock()

	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}

	w.WriteHeader(status)
	v.got <- struct{}{}
}

// wait waits for n more requests.
func (v *receiver) wait(t *testing.T, n int) {
	t.Helper()

	for range n {
		select {
		case <-v.got:
		case <-time.After(eventually):
			t.Fatalf("upstream got %d requests", v.requests())
		}
	}
}

func (v *receiver) requests() int {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.calls
}

// logs are the record bodies the upstream accepted, in order.
func (v *receiver) logs() []string {
	v.mu.Lock()
	defer v.mu.Unlock()

	var out []string

	for _, export := range v.exports {
		for _, rl := range export.GetResourceLogs() {
			for _, sl := range rl.GetScopeLogs() {
				for _, lr := range sl.GetLogRecords() {
					out = append(out, lr.GetBody().GetStringValue())
				}
			}
		}
	}

	return out
}

type fixture struct {
	h        *backplanetest.Harness
	settings upstream.Settings
	receiver *receiver
	fwd      *upstream.Forwarder
}

// start runs a forwarder to a receiver double; edit adjusts the settings.
func start(t *testing.T, edit func(*upstream.Settings, *receiver)) *fixture {
	t.Helper()

	f := &fixture{receiver: newReceiver(t)}
	f.settings = upstream.Settings{
		URL: f.receiver.server.URL + "/v1/logs", Service: "core",
		Queue: 100, Batch: 10, Flush: 20 * time.Millisecond, Timeout: 2 * time.Second, GiveUp: time.Minute,
	}

	if err := backplanetest.SetLive(&f.settings.Enabled, true); err != nil {
		t.Fatal(err)
	}

	if edit != nil {
		edit(&f.settings, f.receiver)
	}

	f.h = backplanetest.New(t, backplanetest.Name("backplane"))
	f.fwd = upstream.New(f.h.Root(), f.settings, upstream.WithTransport(f.receiver.server.Client().Transport))
	f.h.Start()

	return f
}

func record(resource *resourcepb.Resource, scope *commonpb.InstrumentationScope, body string) upstream.Record {
	return upstream.Record{
		Resource: resource, ResourceSchemaURL: "https://opentelemetry.io/schemas/1.26.0", Scope: scope,
		Log: &logspb.LogRecord{
			TimeUnixNano: 1, Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
			Attributes: []*commonpb.KeyValue{{Key: "exception.type", Value: &commonpb.AnyValue{
				Value: &commonpb.AnyValue_StringValue{StringValue: "TypeError"},
			}}},
		},
	}
}

func resource(service string) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name", Value: &commonpb.AnyValue{
		Value: &commonpb.AnyValue_StringValue{StringValue: service},
	}}}}
}

func TestForwardsRecordsAsReceived(t *testing.T) {
	t.Parallel()

	f := start(t, nil)

	web, api := resource("shop-web"), resource("shop-api")
	scope := &commonpb.InstrumentationScope{Name: "@backplane-labs/backplane-errors", Version: "1.0.0"}
	records := []upstream.Record{record(web, scope, "a"), record(web, scope, "b"), record(api, nil, "c")}

	f.fwd.Offer(t.Context(), records...)
	f.receiver.wait(t, 1)

	want := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{
		{Resource: web, SchemaUrl: records[0].ResourceSchemaURL, ScopeLogs: []*logspb.ScopeLogs{
			{Scope: scope, LogRecords: []*logspb.LogRecord{records[0].Log, records[1].Log}},
		}},
		{Resource: api, SchemaUrl: records[2].ResourceSchemaURL, ScopeLogs: []*logspb.ScopeLogs{
			{LogRecords: []*logspb.LogRecord{records[2].Log}},
		}},
	}}

	f.receiver.mu.Lock()
	defer f.receiver.mu.Unlock()

	if len(f.receiver.exports) != 1 || !proto.Equal(f.receiver.exports[0], want) {
		t.Fatalf("upstream got %v, want %v", f.receiver.exports, want)
	}
}

func TestDelivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		answer []int
		// requests the upstream sees for the first record, then the
		// records it accepted after a second one.
		requests int
		want     []string
	}{
		{
			name: "a transient failure is retried", answer: []int{http.StatusServiceUnavailable}, requests: 2,
			want: []string{"first", "second"},
		},
		{
			name: "too many requests is retried", answer: []int{http.StatusTooManyRequests}, requests: 2,
			want: []string{"first", "second"},
		},
		{
			name: "a refusal is dropped, the next batch goes", answer: []int{http.StatusBadRequest}, requests: 1,
			want: []string{"second"},
		},
		{
			name: "an unknown installation is dropped", answer: []int{http.StatusForbidden}, requests: 1,
			want: []string{"second"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := start(t, func(_ *upstream.Settings, v *receiver) { v.answer = tc.answer })

			f.fwd.Offer(t.Context(), record(resource("web"), nil, "first"))
			f.receiver.wait(t, tc.requests)

			f.fwd.Offer(t.Context(), record(resource("web"), nil, "second"))
			f.receiver.wait(t, 1)

			f.receiver.mu.Lock()
			retried := len(f.receiver.bodies) > 1 && tc.requests == 2 && !bytes.Equal(f.receiver.bodies[0], f.receiver.bodies[1])
			f.receiver.mu.Unlock()

			if retried {
				t.Error("a retry sent a different body")
			}

			if got := f.receiver.logs(); !equal(got, tc.want) || f.receiver.requests() != tc.requests+1 {
				t.Errorf("accepted %v in %d requests, want %v in %d", got, f.receiver.requests(), tc.want, tc.requests+1)
			}
		})
	}
}

func TestSwitchOffForwardsNothing(t *testing.T) {
	t.Parallel()

	f := start(t, nil)

	if err := backplanetest.SetLive(&f.settings.Enabled, false); err != nil {
		t.Fatal(err)
	}

	f.fwd.Offer(t.Context(), record(resource("web"), nil, "while off"))

	if err := backplanetest.SetLive(&f.settings.Enabled, true); err != nil {
		t.Fatal(err)
	}

	f.fwd.Offer(t.Context(), record(resource("web"), nil, "back on"))
	f.receiver.wait(t, 1)

	if got := f.receiver.logs(); !equal(got, []string{"back on"}) || f.receiver.requests() != 1 {
		t.Errorf("accepted %v in %d requests", got, f.receiver.requests())
	}
}

func TestFullQueueDropsNewRecords(t *testing.T) {
	t.Parallel()

	hold := make(chan struct{})
	f := start(t, func(s *upstream.Settings, v *receiver) {
		s.Queue, s.Batch = 1, 1
		v.hold = hold
	})

	f.fwd.Offer(t.Context(), record(resource("web"), nil, "in flight"))

	deadline := time.Now().Add(eventually)
	for f.receiver.requests() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	// The first request is held: one record fits the queue, the rest drop
	// without blocking the caller.
	f.fwd.Offer(t.Context(), record(resource("web"), nil, "queued"), record(resource("web"), nil, "dropped"))
	close(hold)
	f.receiver.wait(t, 2)

	if got := f.receiver.logs(); !equal(got, []string{"in flight", "queued"}) {
		t.Errorf("accepted %v", got)
	}
}

func TestStopFlushesTheQueue(t *testing.T) {
	t.Parallel()

	f := start(t, func(s *upstream.Settings, _ *receiver) { s.Flush = time.Hour })

	f.fwd.Offer(t.Context(), record(resource("web"), nil, "last words"))
	backplanetest.Stop(f.h, f.fwd)

	if got := f.receiver.logs(); !equal(got, []string{"last words"}) {
		t.Errorf("accepted %v", got)
	}
}

func TestNoneForwardsNothing(t *testing.T) {
	t.Parallel()

	upstream.None{}.Offer(t.Context(), record(resource("web"), nil, "x"))
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings upstream.Settings
		want     error
	}{
		{name: "no upstream", settings: upstream.Settings{}},
		{name: "mesh", settings: upstream.Settings{URL: "https://core.service.consul:8443/v1/logs", Service: "core"}},
		{name: "plain http", settings: upstream.Settings{URL: "http://collector:4318/v1/logs"}},
		{
			name: "mesh needs https", settings: upstream.Settings{URL: "http://core:8080/v1/logs", Service: "core"},
			want: upstream.ErrMeshURL,
		},
		{name: "relative", settings: upstream.Settings{URL: "/v1/logs"}, want: upstream.ErrURL},
		{name: "other scheme", settings: upstream.Settings{URL: "ftp://example.com"}, want: upstream.ErrURL},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.settings.Validate(); !errors.Is(err, tc.want) {
				t.Errorf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
