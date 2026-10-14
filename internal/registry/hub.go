package registry

import (
	"context"
	"maps"
	"slices"
	"sync"

	"google.golang.org/protobuf/proto"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// Hub holds the current snapshot and signals changes: the Source half of
// the Registry, and on its own the fake Source of tests — Publish what the
// test needs.
//
// A Hub is shared by pointer: it holds a lock and its subscribers.
type Hub struct {
	mu   sync.Mutex
	cur  Catalog
	subs map[chan struct{}]struct{}
}

var _ Source = (*Hub)(nil)

// NewHub is a Hub with the zero Catalog.
func NewHub() *Hub { return &Hub{subs: map[chan struct{}]struct{}{}} }

// Current is the latest snapshot. It is shared: never modify its maps,
// slices or messages.
func (h *Hub) Current() Catalog {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.cur
}

// Changes delivers one signal after each new snapshot (one pending signal
// at most: a slow reader sees the latest snapshot once) and is closed when
// ctx ends. Read Current after a signal.
func (h *Hub) Changes(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)

	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	context.AfterFunc(ctx, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
		close(ch)
	})

	return ch
}

// Publish makes services the current snapshot unless it equals the current
// one; the new snapshot's Index is one above the previous. It reports the
// snapshot and whether it is new. services is kept: do not modify it after.
func (h *Hub) Publish(services map[string]Service) (Catalog, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.cur.Index > 0 && equalServices(h.cur.Services, services) {
		return h.cur, false
	}

	h.cur = Catalog{Index: h.cur.Index + 1, Services: services}

	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}

	return h.cur, true
}

func equalServices(a, b map[string]Service) bool {
	return maps.EqualFunc(a, b, func(x, y Service) bool {
		return x.Name == y.Name &&
			maps.EqualFunc(x.Manifests, y.Manifests, equalManifest) &&
			slices.EqualFunc(x.Instances, y.Instances, equalInstance)
	})
}

func equalManifest(a, b *backplanev1.Manifest) bool { return a == b || proto.Equal(a, b) }

func equalInstance(a, b Instance) bool {
	return a.ID == b.ID && a.Registered == b.Registered && a.Healthy == b.Healthy &&
		a.Address == b.Address && a.Port == b.Port && slices.Equal(a.Tags, b.Tags) &&
		(a.State == b.State || proto.Equal(a.State, b.State))
}
