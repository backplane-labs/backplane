// Package formatter is an independent service capability. It does not import
// hello: bindings adapt its activity payloads, and its reactor owns a local
// copy of the event fields it needs.
package formatter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
)

const (
	remembered       = 4096
	operationTimeout = 5 * time.Second
	maxDeliver       = 3
)

var ErrName = errors.New("formatter: name must not be empty")

type Config struct {
	Prefix config.Live[string] `json:"prefix" schemapb:"default=Welcome"`
	Suffix config.Live[string] `json:"suffix" schemapb:"default=!"`
}
type Input struct {
	Name string `json:"name"`
}
type Output struct {
	Text string `json:"text"`
}

// Greeting is this service's additive view of hello.Greeted, with no source import.
type Greeting struct {
	Name string `json:"name"`
	Text string `json:"text"`
}
type Receipt struct {
	Count uint64 `json:"count"`
	Key   string `json:"key"`
}
type Stats struct {
	Formatted uint64 `json:"formatted"`
	Recorded  uint64 `json:"recorded"`
	Observed  uint64 `json:"observed"`
	LastText  string `json:"last_text"`
}

// Formatter records process-local example statistics. Deduplication retains
// the last 4096 keys; a real business store persists them transactionally.
type Formatter struct {
	deps.Component
	cfg   *Config
	mu    sync.Mutex
	stats Stats
	seen  map[string]bool
	order []string
}

func New(parent deps.Scope, cfg *Config) *Formatter {
	f := &Formatter{Component: deps.NewComponent(parent, "formatter"), cfg: cfg, seen: map[string]bool{}}
	activity.Handle(f, "Format", f.Format, activity.StartToClose(operationTimeout),
		activity.Describe("formats a greeting"))
	activity.Handle(f, "Record", f.Record, activity.StartToClose(operationTimeout),
		activity.Describe("records a greeting from a rule"))
	event.React(f, "hello.Greeted", f.Observe, event.Consumer("formatter-greeted"),
		event.StartAt(event.StartNew), event.MaxDeliver(maxDeliver), event.Timeout(operationTimeout))

	return f
}

func (f *Formatter) Format(_ context.Context, in Input) (Output, error) {
	if in.Name == "" {
		return Output{}, activity.NonRetryable(ErrName) //nolint:wrapcheck // preserves the non-retryable marker
	}

	text := fmt.Sprintf("%s, %s%s", f.cfg.Prefix.Get(), in.Name, f.cfg.Suffix.Get())
	f.mu.Lock()
	f.stats.Formatted++
	f.mu.Unlock()

	return Output{Text: text}, nil
}

func (f *Formatter) Record(ctx context.Context, in Greeting) (Receipt, error) {
	if in.Name == "" {
		return Receipt{}, activity.NonRetryable(ErrName) //nolint:wrapcheck // preserves the non-retryable marker
	}

	info, _ := activity.InfoOf(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.remember("activity/" + info.Key) {
		f.stats.Recorded++
		f.stats.LastText = in.Text
	}

	return Receipt{Count: f.stats.Recorded, Key: info.Key}, nil
}

func (f *Formatter) Observe(ctx context.Context, in Greeting) error {
	if in.Name == "" {
		return event.Terminal(ErrName) //nolint:wrapcheck // preserves the terminal marker
	}

	delivery, _ := event.DeliveryOf(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.remember("event/" + delivery.ID) {
		f.stats.Observed++
		f.stats.LastText = in.Text
	}

	return nil
}

func (f *Formatter) remember(key string) bool {
	if f.seen[key] {
		return false
	}

	f.seen[key] = true

	f.order = append(f.order, key)
	if len(f.order) > remembered {
		delete(f.seen, f.order[0])
		f.order = f.order[1:]
	}

	return true
}

func (f *Formatter) Snapshot() Stats {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.stats
}

// HTTP exposes the example's counters, including the bound greeting's text.
func (f *Formatter) HTTP() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.Snapshot())
	})
}
