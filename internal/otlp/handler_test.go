package otlp_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/internal/otlp"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

func settings(url string) otlp.Config {
	return otlp.Config{
		URL: url, BodyBytes: 1024, Concurrent: 2, RatePerMinute: 6000,
		Burst: 100, MaxIPs: 16, IdleTTL: time.Minute, Timeout: time.Second,
	}
}

func admission(t *testing.T, cfg otlp.Config) *otlp.Handler {
	t.Helper()

	handler, err := otlp.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(handler.Close)

	return handler
}

func send(handler http.Handler, path, body string, headers ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	for i := 0; i < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

func TestForwardSignalsAndPartialSuccess(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		body, _ := io.ReadAll(r.Body)
		if string(body) != `{}` || r.URL.RawQuery != "" || !strings.HasPrefix(r.URL.Path, "/collector/v1/") {
			t.Errorf("upstream payload/path: %s %s", r.URL, body)
		}

		if r.Header.Get(otlp.IngestHeader) != otlp.IngestProxy {
			t.Errorf("ingest header %q", r.Header.Get(otlp.IngestHeader))
		}

		for _, header := range []string{"Authorization", "Cookie", "Baggage", "Proxy-Authorization", "X-Forwarded-For", "X-Api-Key", "Content-Encoding"} {
			if r.Header.Get(header) != "" {
				t.Errorf("forwarded %s", header)
			}
		}

		w.Header().Set("Set-Cookie", "must-not-forward=1")
		_, _ = io.WriteString(w, `{"partialSuccess":{"rejectedLogRecords":"1","errorMessage":"one rejected"}}`)
	}))
	t.Cleanup(upstream.Close)

	handler := admission(t, settings(upstream.URL+"/collector"))
	for _, signal := range []string{"logs", "metrics", "traces"} {
		result := send(handler, "/v1/"+signal+"?token=discard", `{}`,
			"Authorization", "Bearer browser", "Cookie", "bp_session=discard", "Baggage", "discard", "X-Api-Key", "discard",
			otlp.IngestHeader, "direct")
		if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "rejectedLogRecords") || result.Header().Get("Set-Cookie") != "" {
			t.Fatalf("response: %d %s", result.Code, result.Body)
		}
	}

	if calls.Load() != 3 {
		t.Fatal("signal was retried or lost")
	}
}

func TestOpaqueSignalForwarding(t *testing.T) {
	t.Parallel()

	// This body exceeds the removed per-record limit and contains caller-owned
	// attributes plus unknown fields. Neither JSON spelling nor protobuf unknown
	// fields may change at the proxy boundary.
	large := strings.Repeat("x", 64<<10)

	payloads := map[string]string{
		"logs":    fmt.Sprintf(`{ "resourceLogs": [{"scopeLogs":[{"logRecords":[{"body":{"stringValue":%q},"attributes":[{"key":"__backplane_otlp_v1","value":{"stringValue":"caller-owned"}}]}]}]}], "futureField":9007199254740993 }`, large),
		"traces":  fmt.Sprintf(`{ "resourceSpans": [{"scopeSpans":[{"spans":[{"traceId":"ABCDEF0123456789ABCDEF0123456789","spanId":"ABCDEF0123456789","name":%q}]}]}], "futureField":9007199254740993 }`, large),
		"metrics": fmt.Sprintf(`{ "resourceMetrics": [{"scopeMetrics":[{"metrics":[{"name":"author.custom_gauge","description":%q,"gauge":{"dataPoints":[{"asInt":"9007199254740993"}]}}]}]}], "futureField":9007199254740993 }`, large),
	}
	for signal, payload := range payloads {
		for _, contentType := range []string{"application/json", "application/x-protobuf"} {
			for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusServiceUnavailable} {
				t.Run(fmt.Sprintf("%s/%s/%d", signal, contentType, status), func(t *testing.T) {
					t.Parallel()

					body := payload
					responseBody := `{"partialSuccess":{"errorMessage":"collector diagnostic","futureField":1}}`

					if contentType == "application/x-protobuf" {
						// Export request: empty resource group and unknown field 127.
						body = "\x0a\x00\xf8\x07\x01"
						responseBody = "\x0a\x02\x08\x01\xf8\x07\x01"
					}

					var calls atomic.Int64

					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)

						got, err := io.ReadAll(r.Body)
						if err != nil || string(got) != body || r.Header.Get("Content-Type") != contentType {
							t.Errorf("proxy changed %s payload or content type: %v", signal, err)
						}

						w.Header().Set("Retry-After", "5")
						w.WriteHeader(status)
						_, _ = io.WriteString(w, responseBody)
					}))
					t.Cleanup(upstream.Close)
					cfg := settings(upstream.URL)
					cfg.BodyBytes = 128 << 10

					result := send(admission(t, cfg), "/v1/"+signal, body, "Content-Type", contentType)
					if result.Code != status || result.Body.String() != responseBody || result.Header().Get("Retry-After") != "5" || calls.Load() != 1 {
						t.Fatalf("Collector response changed or request retried: status %d, calls %d", result.Code, calls.Load())
					}
				})
			}
		}
	}
}

//nolint:paralleltest,tparallel // subtests share a counted upstream
func TestBodyBoundsAndCompression(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		body, _ := io.ReadAll(r.Body)
		if string(body) != "{}" || r.Header.Get("Content-Encoding") != "" {
			t.Error("gzip not decoded")
		}

		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(upstream.Close)
	handler := admission(t, settings(upstream.URL))

	compressed := func(body string) string {
		var buffer bytes.Buffer

		writer := gzip.NewWriter(&buffer)
		_, _ = io.WriteString(writer, body)
		_ = writer.Close()

		return buffer.String()
	}
	for _, test := range []struct {
		name, body, encoding string
		code                 int
	}{
		{"oversized chunked", strings.Repeat("x", 2048), "", 413},
		{"compression bomb", compressed(strings.Repeat("x", 8192)), "gzip", 413},
		{"bad gzip", "invalid", "gzip", 400},
		{"unsupported encoding", "{}", "br", 415},
		{"valid gzip", compressed("{}"), "gzip", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(test.body))
			request.ContentLength = -1
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Content-Encoding", test.encoding)

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.code {
				t.Fatalf("status %d, want %d: %s", response.Code, test.code, response.Body)
			}
		})
	}

	if calls.Load() != 1 {
		t.Fatalf("invalid requests reached Collector: %d", calls.Load())
	}
}

func TestConcurrencyAndCancellation(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		entered <- struct{}{}

		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)
	cfg := settings(upstream.URL)
	cfg.Concurrent = 1
	handler := admission(t, cfg)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	request := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("{}")).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})

	go func() { defer close(done); handler.ServeHTTP(httptest.NewRecorder(), request) }()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Collector never received first request")
	}

	response := send(handler, "/v1/logs", "{}")
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" {
		t.Fatalf("overload did not fail promptly: %d", response.Code)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled request retained its slot")
	}
	// A subsequent attempt reaches Collector and times out, proving slot release.
	response = send(handler, "/v1/logs", "{}")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("timeout: %d", response.Code)
	}

	select {
	case <-entered:
	default:
		t.Fatal("cancelled request leaked the concurrency slot")
	}
}

func TestIPBoundsAndTrustedForwarding(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }))
	t.Cleanup(upstream.Close)
	cfg := settings(upstream.URL)
	cfg.RatePerMinute, cfg.Burst, cfg.MaxIPs = 1, 1, 2
	cfg.TrustedProxies = []string{"10.0.0.0/8"}
	handler := admission(t, cfg)

	request := func(remote, forwarded string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader("{}"))
		req.RemoteAddr = remote
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", forwarded)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		return rec.Code
	}
	if request("192.0.2.1:1234", "198.51.100.1") != 200 || request("192.0.2.1:1234", "198.51.100.2") != 429 {
		t.Fatal("untrusted XFF bypassed rate limit")
	}

	if request("10.0.0.1:1234", "forged, 192.0.2.2, 10.0.0.2") != 200 || request("10.0.0.1:1234", "different, 192.0.2.2, 10.0.0.2") != 429 {
		t.Fatal("trusted proxy chain was not processed from the right")
	}

	if request("192.0.2.3:1234", "") != 503 || request("192.0.2.1:1234", "") != 429 {
		t.Fatal("address churn bypassed the bounded table")
	}
}

func TestKeysAndProtocolAllowlist(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }))
	t.Cleanup(upstream.Close)
	cfg := settings(upstream.URL)
	cfg.Keys = []config.Secret{"previous-ingest-key", "current-ingest-key"}

	handler := admission(t, cfg)
	if send(handler, "/v1/logs", "{}").Code != 401 || send(handler, "/v1/logs", "{}", "Authorization", "Bearer invalid").Code != 401 {
		t.Fatal("key requirement bypassed")
	}

	for _, key := range cfg.Keys {
		if send(handler, "/v1/logs", "{}", "Authorization", "Bearer "+key.Reveal()).Code != 200 {
			t.Fatal("rotation overlap rejected")
		}
	}

	if send(handler, "/admin", "{}").Code != 404 || send(handler, "/v1/logs", "{}", "Authorization", "Bearer current-ingest-key", "Content-Type", "text/plain").Code != 415 {
		t.Fatal("protocol allowlist bypassed")
	}

	preflight := httptest.NewRecorder()
	handler.ServeHTTP(preflight, httptest.NewRequest(http.MethodOptions, "/v1/logs", http.NoBody))

	if preflight.Code != 204 || preflight.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("anonymous browser preflight rejected")
	}
}

func TestSlowBodyDeadline(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("incomplete body reached Collector")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	cfg := settings(upstream.URL)
	cfg.Timeout = 40 * time.Millisecond
	server := httptest.NewServer(admission(t, cfg))
	t.Cleanup(server.Close)

	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(conn, "POST /v1/logs HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{")

	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("slow body: %s", response.Status)
	}
}

func TestExpiredIPCapacityIsReclaimed(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }))
	t.Cleanup(upstream.Close)
	cfg := settings(upstream.URL)
	cfg.Burst, cfg.MaxIPs, cfg.IdleTTL = 1, 1, 10*time.Millisecond
	handler := admission(t, cfg)

	request := func(remote string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader("{}"))
		req.RemoteAddr = remote
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		return rec.Code
	}
	if request("192.0.2.1:1234") != 200 {
		t.Fatal("first address rejected")
	}

	time.Sleep(25 * time.Millisecond)

	if request("192.0.2.2:1234") != 200 {
		t.Fatal("expired table entry retained its slot")
	}
}
