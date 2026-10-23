package consul_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/consul"
)

// boundKV serves one KV key with blocking queries.
type boundKV struct {
	mu      sync.Mutex
	index   uint64
	value   []byte // nil: absent
	changed chan struct{}
}

func (b *boundKV) set(value []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.index++
	b.value = value
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *boundKV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	wait, _ := strconv.ParseUint(r.URL.Query().Get("index"), 10, 64)

	b.mu.Lock()
	if wait >= b.index {
		changed := b.changed
		b.mu.Unlock()

		select {
		case <-changed:
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}

		b.mu.Lock()
	}

	index, value := b.index, b.value
	b.mu.Unlock()

	w.Header().Set("X-Consul-Index", strconv.FormatUint(index, 10))

	if value == nil {
		http.NotFound(w, r)

		return
	}

	_, _ = w.Write([]byte(`[{"Key":"backplane/bindings/svc","Value":"` + base64.StdEncoding.EncodeToString(value) + `"}]`))
}

// The bound set is delivered at start, after every change and as empty
// when the key goes away; an unchanged poll delivers nothing.
func TestWatchBound(t *testing.T) {
	t.Parallel()

	kv := &boundKV{index: 1, value: []byte(`["A"]`), changed: make(chan struct{})}

	mux := http.NewServeMux()
	mux.Handle("GET /v1/kv/backplane/bindings/svc", kv)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api.NewClient(&api.Config{Address: srv.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan []string, 10)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = consul.NewDiscovery(client).WatchBound(ctx, "svc", func(hooks []string) { got <- hooks })
	}()

	expect := func(want ...string) {
		t.Helper()

		select {
		case hooks := <-got:
			if !slices.Equal(hooks, want) {
				t.Fatalf("hooks %v, want %v", hooks, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no update, want %v", want)
		}
	}

	expect("A")
	kv.set([]byte(`["A","B"]`))
	expect("A", "B")
	kv.set([]byte(`["A","B"]`))
	kv.set(nil)
	expect()

	select {
	case hooks := <-got:
		t.Fatalf("unexpected update %v", hooks)
	case <-time.After(300 * time.Millisecond):
	}
}

// A section's published set is delivered raw, nil when the key goes away.
func TestWatchSection(t *testing.T) {
	t.Parallel()

	kv := &boundKV{index: 1, value: []byte("v1"), changed: make(chan struct{})}

	mux := http.NewServeMux()
	mux.Handle("GET /v1/kv/backplane/sections/komeet.module", kv)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api.NewClient(&api.Config{Address: srv.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan []byte, 10)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = consul.NewDiscovery(client).WatchSection(ctx, "komeet.module", func(raw []byte) { got <- raw })
	}()

	expect := func(want []byte) {
		t.Helper()

		select {
		case raw := <-got:
			if !bytes.Equal(raw, want) || (raw == nil) != (want == nil) {
				t.Fatalf("raw %q, want %q", raw, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no update, want %q", want)
		}
	}

	expect([]byte("v1"))
	kv.set([]byte("v2"))
	expect([]byte("v2"))
	kv.set(nil)
	expect(nil)
}
