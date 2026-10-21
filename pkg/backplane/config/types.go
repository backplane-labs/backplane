package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	sp "github.com/gopherex/schemapb/go/schemapb"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
)

// Live is a configuration field that changes at runtime: set from the
// console through Consul KV, applied in place. Every copy of the enclosing
// section reads the same current value.
//
// Live fields must sit in structs, not in slices or maps: Open rejects
// those. A zero Live reads the zero value and never fires; LiveOf makes one
// with a value. Its methods take a pointer (loading fills it in place), so
// encoding/json and fmt see the current value only through a pointer to the
// enclosing struct.
type Live[T any] struct {
	c *cell[T]
}

type cell[T any] struct {
	value    atomic.Pointer[T]
	mu       sync.Mutex
	watchers []func(T)
	appliers []func(T) error
	storage  T // decode target; published by value.Store
}

// LiveOf returns a Live holding v: defaults in code, tests.
func LiveOf[T any](v T) Live[T] {
	c := &cell[T]{}
	c.value.Store(&v)

	return Live[T]{c: c}
}

// Get returns the current value.
func (l *Live[T]) Get() T {
	if l.c == nil {
		var zero T

		return zero
	}

	return *l.c.value.Load()
}

// Watch calls fn with every new value applied after loading. Callbacks run
// in order on the configuration's update goroutine: keep them short and do
// not close the service from one.
func (l *Live[T]) Watch(fn func(T)) {
	if l.c == nil {
		return
	}

	l.c.mu.Lock()
	defer l.c.mu.Unlock()

	l.c.watchers = append(l.c.watchers, fn)
}

// Apply calls fn with every new value before it is published, to carry it
// to something outside the process (a pool, a client, a limiter). An error
// (or a panic) rejects the whole update: no Live field changes, Watch
// callbacks do not fire, appliers that already took the update are called
// again with the value they had, and the instance reports the revision
// rejected with the error, as a failed Validate does. Appliers run in
// order on the configuration's update goroutine, after Validate; the value
// loaded at start is the component's to apply.
func (l *Live[T]) Apply(fn func(T) error) {
	if l.c == nil {
		return
	}

	l.c.mu.Lock()
	defer l.c.mu.Unlock()

	l.c.appliers = append(l.c.appliers, fn)
}

// prepare is the application of next's value: nil when it does not change
// this field or nothing applies it; undo gives the appliers the current
// value back once apply succeeded.
func (l *Live[T]) prepare(next any) (func() error, func()) {
	n, _ := next.(*Live[T])
	if n == nil || l.c == nil {
		return nil, nil
	}

	var v T
	if n.c != nil {
		v = *n.c.value.Load()
	}

	cur := l.Get()
	if reflect.DeepEqual(cur, v) {
		return nil, nil
	}

	l.c.mu.Lock()
	appliers := append([]func(T) error{}, l.c.appliers...)
	l.c.mu.Unlock()

	if len(appliers) == 0 {
		return nil, nil
	}

	apply := func() error {
		for i, fn := range appliers {
			if err := applyOne(fn, v); err != nil {
				for _, done := range slices.Backward(appliers[:i]) {
					_ = applyOne(done, cur)
				}

				return err
			}
		}

		return nil
	}

	undo := func() {
		for _, fn := range slices.Backward(appliers) {
			_ = applyOne(fn, cur)
		}
	}

	return apply, undo
}

// applyOne runs one applier; a panic is its error.
func applyOne[T any](fn func(T) error, v T) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", errApplierPanic, r)
		}
	}()

	return fn(v)
}

var errApplierPanic = errors.New("applier panicked")

// MarshalJSON writes the current value.
func (l *Live[T]) MarshalJSON() ([]byte, error) { return json.Marshal(l.Get()) } //nolint:wrapcheck // plain value

// replaceFrom publishes next's value into this field and notifies watchers
// when it differs.
func (l *Live[T]) replaceFrom(next any) {
	n, _ := next.(*Live[T])
	if n == nil {
		return
	}

	if n.c == nil {
		// The update carries no value for this field (no layer sets it):
		// it reads the zero value.
		var zero T

		l.set(zero)

		return
	}

	l.set(*n.c.value.Load())
}

// value is an addressable copy of the current value (validation).
func (l *Live[T]) value() reflect.Value {
	v := l.Get()

	return reflect.ValueOf(&v).Elem()
}

// ensure gives a field no layer set its cell, so every copy of the
// enclosing section shares it and later values reach them all.
func (l *Live[T]) ensure() {
	if l.c == nil {
		l.c = &cell[T]{}
		l.c.value.Store(&l.c.storage)
	}
}

// setAny applies and publishes v as an update would (backplanetest.SetLive):
// an applier's error leaves the field as it was.
func (l *Live[T]) setAny(v any) error {
	t, ok := v.(T)
	if !ok {
		return nil
	}

	l.ensure()

	next := LiveOf(t)
	if apply, _ := l.prepare(&next); apply != nil {
		if err := apply(); err != nil {
			return err
		}
	}

	l.set(t)

	return nil
}

func (l *Live[T]) set(v T) {
	if l.c == nil {
		l.c = &cell[T]{}
		l.c.value.Store(&l.c.storage)
	}

	if reflect.DeepEqual(*l.c.value.Load(), v) {
		return
	}

	l.c.value.Store(&v)
	l.c.mu.Lock()
	watchers := append([]func(T){}, l.c.watchers...)
	l.c.mu.Unlock()

	for _, fn := range watchers {
		notify(fn, v)
	}
}

// notify runs one watcher; a panicking watcher is logged, not fatal.
func notify[T any](fn func(T), v T) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("config: Live watcher panicked", "panic", fmt.Sprint(r))
		}
	}()

	fn(v)
}

// SchemaInner makes the field's schema the schema of T.
func (*Live[T]) SchemaInner() reflect.Type { return reflect.TypeFor[T]() }

// SchemaField marks the field live.
func (*Live[T]) SchemaField(f *sp.Schema_Field) error {
	if f.Annotations == nil {
		f.Annotations = map[string]*sp.Value{}
	}

	f.Annotations[configrt.LiveAnnotation] = sp.BoolV(true)

	return nil
}

// SchemaDecodeTarget gives the decoder a fresh private cell to fill.
func (l *Live[T]) SchemaDecodeTarget() any {
	l.c = &cell[T]{}
	l.c.value.Store(&l.c.storage)

	return &l.c.storage
}

type liveField interface {
	replaceFrom(next any)
	ensure()
	value() reflect.Value
	prepare(next any) (apply func() error, undo func())
}

// Secret is a string that never prints: every fmt verb, text, JSON and logs
// show "***". The schema marks it secret, so the console and the instance
// state mask it too. fmt cannot see Secrets in unexported struct fields
// printed with %v: keep them exported or never print the struct.
type Secret string

const masked = "***"

// Reveal returns the value.
func (s Secret) Reveal() string { return string(s) }

// String masks the value.
func (Secret) String() string { return masked }

// GoString masks the value.
func (Secret) GoString() string { return masked }

// Format masks the value for every verb.
func (Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(masked)) }

// MarshalText masks the value (slog text, YAML, XML).
func (Secret) MarshalText() ([]byte, error) { return []byte(masked), nil }

// MarshalJSON masks the value.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + masked + `"`), nil }

// SchemaInner makes the field a plain string in the schema (MarshalJSON
// would otherwise turn it into a JSON field).
func (Secret) SchemaInner() reflect.Type { return reflect.TypeFor[string]() }

// SchemaField marks the field secret.
func (Secret) SchemaField(f *sp.Schema_Field) error {
	f.Secret = true

	return nil
}
