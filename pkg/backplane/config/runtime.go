package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

//nolint:gochecknoinits // installs the private accessors for the SDK core and backplanetest
func init() {
	link.ConfigState = func(rt any) configrt.State {
		if s, ok := rt.(interface{ state() configrt.State }); ok {
			return s.state()
		}

		return nil
	}
	link.SetLive = func(live, v any) error {
		if l, ok := live.(interface{ setAny(v any) error }); ok {
			return l.setAny(v)
		}

		return nil
	}
}

// Runtime is a loaded, live configuration. Value is stable for the life of
// the runtime: static fields never change, Live fields change in place.
type Runtime[C any] struct {
	value  *C
	schema *sp.Schema
	live   [][]string
	rt     *xconf.TypedRuntime[C]
	// kv is the Consul layer's source; nil without Consul.
	kv   RemoteSource
	stop context.CancelFunc
	wg   sync.WaitGroup
	once sync.Once
	log  atomic.Pointer[xlog.Logger]

	mu sync.Mutex
	// applied is the snapshot whose values Value holds: a rejected update
	// becomes the xconf runtime's current snapshot but never this one.
	applied     *xconf.Snapshot
	revision    uint64
	rejected    error
	rejectedRev uint64
	degraded    bool
	changes     []func()
}

// Open loads the configuration and keeps it live: Consul KV (named by the
// embedded Backplane block) overrides Live fields only. The Consul layer is
// resilient: unreachable at start it is empty, failing later it keeps its
// last values; Degraded reports it and the layer recovers on its own. A
// required Live value that only Consul holds keeps Open waiting until
// Consul answers or ctx ends. The runtime outlives ctx; Close it when done.
//
// Every Validator in C (C itself or any section, value or pointer
// receiver) runs on the loaded configuration — its error fails Open — and
// on every live update: an update it rejects is not applied, and the
// instance state reports the error and the rejected revision.
func Open[C Backplaner](ctx context.Context, opts ...Option) (*Runtime[C], error) {
	st, err := newSettings(opts)
	if err != nil {
		return nil, err
	}

	if err := checkLive(reflect.TypeFor[C](), reflect.TypeFor[C]().Name(), map[reflect.Type]bool{}); err != nil {
		return nil, err
	}

	schema, err := reflectSchema[C](st.service)
	if err != nil {
		return nil, err
	}

	sources, err := st.baseSources(reflect.TypeFor[C]())
	if err != nil {
		return nil, err
	}

	// The first pass only finds Consul; Live fields may still be waiting in KV.
	first, err := xconf.LoadAs[C](ctx, configrt.WithoutLiveRequired(schema), sources...)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	live := configrt.LivePaths(schema)
	r := &Runtime[C]{schema: schema, live: live}

	if consul := first.BackplaneConfig().Consul; consul.Enabled() && !st.noConsul {
		if st.remote == nil {
			return nil, ErrRemoteDriver
		}

		r.kv, err = st.remote(first.BackplaneConfig(), st.service)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}

		if r.kv == nil {
			return nil, ErrRemoteSource
		}

		paths := make([]xconf.Path, len(live))
		for i, path := range live {
			paths[i] = path
		}

		var backoff []xconf.ResilientOption
		if st.backoffMin > 0 {
			backoff = append(backoff, xconf.Backoff(st.backoffMin, st.backoffMax))
		}

		src := xconf.Resilient(xconf.Optional(xconf.AllowPaths(r.kv, paths...)), backoff...)

		sources = append(sources, src)
	}

	return r.open(ctx, sources)
}

// open starts the xconf runtime (bounded by ctx, outliving it), takes the
// first value through Validate and starts forwarding updates.
func (r *Runtime[C]) open(ctx context.Context, sources []xconf.Source) (*Runtime[C], error) {
	runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	unbind := context.AfterFunc(ctx, stop)

	rt, err := xconf.OpenAs[C](runCtx, r.schema, sources...)

	unbind()

	if err != nil {
		stop()

		return nil, fmt.Errorf("config: %w", errors.Join(err, ctx.Err()))
	}

	value, err := rt.Current()
	if err == nil {
		if err = validate(&value); err != nil {
			err = fmt.Errorf("validate: %w", err)
		}
	}

	if err != nil {
		stop()

		return nil, errors.Join(fmt.Errorf("config: %w", err), rt.Close())
	}

	ensureLive(reflect.ValueOf(&value).Elem())

	r.value, r.rt, r.stop = &value, rt, stop
	r.applied = rt.Snapshot()
	r.revision = r.revisionOf(r.applied)

	if r.kv != nil {
		r.degraded = r.Degraded() != nil
		metrics.ConfigDegraded(ctx, r.degraded)
	}

	r.wg.Add(1)

	go r.forward(runCtx)

	return r, nil
}

// forward applies the runtime's snapshots to Value: decoded, validated, then
// published into the Live fields; a failing one is recorded as rejected.
func (r *Runtime[C]) forward(ctx context.Context) {
	defer r.wg.Done()

	for ev := range r.rt.Subscribe(ctx) {
		r.observeDegraded(ctx, ev.Snapshot)
		r.handle(ctx, ev)
		r.notify()
	}
}

func (r *Runtime[C]) handle(ctx context.Context, event xconf.Event) {
	if event.Err != nil {
		// The update never became a snapshot: its revision is the last read.
		r.reject(ctx, event.Err, r.lastRevision())

		return
	}

	r.mu.Lock()
	same := event.Snapshot == r.applied
	r.mu.Unlock()

	if same {
		// Back to the applied snapshot after a failed reload.
		r.clearRejection()

		return
	}

	rev := r.revisionOf(event.Snapshot)

	next, err := xconf.Decode[C](event.Snapshot)
	if err == nil {
		if err = validate(&next); err != nil {
			err = fmt.Errorf("validate: %w", err)
		}
	}

	if err == nil {
		err = apply(reflect.ValueOf(r.value).Elem(), reflect.ValueOf(&next).Elem())
	}

	if err != nil {
		r.reject(ctx, err, rev)

		return
	}

	// Record the snapshot first: watchers fired by transfer see Effective
	// (values, sources, revision) already matching what they are told.
	r.mu.Lock()
	update := len(event.Changed) > 0 || rev != r.revision
	r.applied, r.revision, r.rejected, r.rejectedRev = event.Snapshot, rev, nil, 0
	r.mu.Unlock()

	transfer(reflect.ValueOf(r.value).Elem(), reflect.ValueOf(&next).Elem())

	if update {
		metrics.ConfigUpdate(ctx, metrics.Applied)
	}
}

// reject records an update that is not applied; a repeat of the same
// rejection (the same revision failing the same way) is not logged again.
func (r *Runtime[C]) reject(ctx context.Context, err error, rev uint64) {
	r.mu.Lock()
	repeat := r.rejected != nil && r.rejectedRev == rev && r.rejected.Error() == err.Error()
	r.rejected, r.rejectedRev = err, rev
	r.mu.Unlock()

	if repeat {
		return
	}

	metrics.ConfigUpdate(ctx, metrics.Rejected)
	r.warn("config update rejected, keeping the applied values", err, "revision", rev)
}

func (r *Runtime[C]) clearRejection() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rejected, r.rejectedRev = nil, 0
}

// observeDegraded logs and records the Consul layer going stale and coming
// back.
func (r *Runtime[C]) observeDegraded(ctx context.Context, snap *xconf.Snapshot) {
	if r.kv == nil || snap == nil {
		return
	}

	err := consulDegraded(snap)

	r.mu.Lock()
	changed := r.degraded != (err != nil)
	r.degraded = err != nil
	r.mu.Unlock()

	if !changed {
		return
	}

	metrics.ConfigDegraded(ctx, err != nil)

	if err != nil {
		r.warn("config: consul layer unavailable, keeping its last values; retrying", err)
	} else {
		r.info("config: consul layer recovered")
	}
}

// revisionOf is the console revision the Consul layer of snap came from.
func (r *Runtime[C]) revisionOf(snap *xconf.Snapshot) uint64 {
	if r.kv == nil || snap == nil {
		return 0
	}

	return parseRevision(snap.Revisions()[r.kv.Name()])
}

func (r *Runtime[C]) lastRevision() uint64 {
	if r.kv == nil {
		return 0
	}

	return r.kv.LastRevision()
}

func (r *Runtime[C]) warn(msg string, err error, kv ...any) {
	if log := r.log.Load(); log != nil {
		fields := []xlog.Field{xlog.Err(err)}
		for i := 0; i+1 < len(kv); i += 2 {
			fields = append(fields, xlog.Any(fmt.Sprint(kv[i]), kv[i+1]))
		}

		log.Warn(msg, fields...)

		return
	}

	slog.Warn(msg, append([]any{"error", err.Error()}, kv...)...)
}

func (r *Runtime[C]) info(msg string) {
	if log := r.log.Load(); log != nil {
		log.Info(msg)

		return
	}

	slog.Info(msg)
}

// transfer publishes every Live field of next into dst, in place.
// ensureLive gives every Live field of v a cell before anyone copies v.
func ensureLive(v reflect.Value) {
	if v.CanAddr() {
		if l, ok := v.Addr().Interface().(liveField); ok {
			l.ensure()

			return
		}
	}

	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				ensureLive(v.Field(i))
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			ensureLive(v.Elem())
		}
	default:
	}
}

// apply runs the appliers (Live.Apply) of every Live field next changes,
// in field order; on an error the ones that succeeded are undone and the
// error names the field.
func apply(dst, next reflect.Value) error {
	var undos []func()

	var err error

	walkLivePairs(dst, next, "", func(l liveField, n any, path string) bool {
		run, undo := l.prepare(n)
		if run == nil {
			return true
		}

		if e := run(); e != nil {
			err = fmt.Errorf("apply %s: %w", path, e)

			return false
		}

		undos = append(undos, undo)

		return true
	})

	if err != nil {
		for _, undo := range slices.Backward(undos) {
			undo()
		}
	}

	return err
}

// walkLivePairs calls fn with every Live field of dst, next's counterpart
// and its path, until fn returns false.
func walkLivePairs(dst, next reflect.Value, path string, fn func(liveField, any, string) bool) bool {
	if dst.CanAddr() {
		if l, ok := dst.Addr().Interface().(liveField); ok {
			return fn(l, next.Addr().Interface(), path)
		}
	}

	switch dst.Kind() {
	case reflect.Struct:
		for i := range dst.NumField() {
			if f := dst.Type().Field(i); f.IsExported() {
				if !walkLivePairs(dst.Field(i), next.Field(i), join(path, fieldName(f)), fn) {
					return false
				}
			}
		}
	case reflect.Pointer:
		if !dst.IsNil() && !next.IsNil() {
			return walkLivePairs(dst.Elem(), next.Elem(), path, fn)
		}
	default:
	}

	return true
}

func transfer(dst, next reflect.Value) {
	if dst.CanAddr() {
		if l, ok := dst.Addr().Interface().(liveField); ok {
			l.replaceFrom(next.Addr().Interface())

			return
		}
	}

	switch dst.Kind() {
	case reflect.Struct:
		for i := range dst.NumField() {
			if dst.Type().Field(i).IsExported() {
				transfer(dst.Field(i), next.Field(i))
			}
		}
	case reflect.Pointer:
		if !dst.IsNil() && !next.IsNil() {
			transfer(dst.Elem(), next.Elem())
		}
	default:
	}
}

func (r *Runtime[C]) notify() {
	r.mu.Lock()
	changes := append([]func(){}, r.changes...)
	r.mu.Unlock()

	for _, fn := range changes {
		fn()
	}
}

// Value is the configuration. The pointer is stable; Live fields update in
// place.
func (r *Runtime[C]) Value() *C { return r.value }

// Degraded reports why the Consul layer is stale or empty; nil when healthy
// or not configured.
func (r *Runtime[C]) Degraded() error { return consulDegraded(r.rt.Snapshot()) }

func consulDegraded(snap *xconf.Snapshot) error {
	for name, err := range snap.Degraded() {
		if strings.HasPrefix(name, configrt.SourceConsul) {
			return err
		}
	}

	return nil
}

// Close stops watching; safe to call more than once.
func (r *Runtime[C]) Close() error {
	var err error

	r.once.Do(func() {
		r.stop()

		if cerr := r.rt.Close(); cerr != nil {
			err = fmt.Errorf("config: close: %w", cerr)
		}

		r.wg.Wait()
	})

	return err
}

func (r *Runtime[C]) state() configrt.State { return state[C]{r} }

// state is the SDK's view, unreachable for authors.
type state[C any] struct{ r *Runtime[C] }

func (s state[C]) Schema() *sp.Schema { return s.r.schema }

func (s state[C]) LivePaths() []string {
	out := make([]string, len(s.r.live))
	for i, p := range s.r.live {
		out[i] = strings.Join(p, ".")
	}

	return out
}

func (s state[C]) OnChange(fn func()) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()

	s.r.changes = append(s.r.changes, fn)
}

func (s state[C]) Degraded() error { return s.r.Degraded() }

func (s state[C]) SetLog(log *xlog.Logger) { s.r.log.Store(log) }

func (s state[C]) Close() error { return s.r.Close() }

func (s state[C]) Effective() configrt.Effective {
	s.r.mu.Lock()
	snap, rejected := s.r.applied, s.r.rejected
	eff := configrt.Effective{
		Sources: map[string]backplanev1.ConfigSource{}, Revision: s.r.revision, RejectedRevision: s.r.rejectedRev,
	}
	s.r.mu.Unlock()

	values, err := json.Marshal(snap.Baked().Masked().ToGo())
	eff.Values, eff.Err = values, errors.Join(rejected, err)

	for ptr, origins := range snap.Origins() {
		src := backplanev1.ConfigSource_CONFIG_SOURCE_DEFAULT
		for _, o := range origins {
			src = max(src, configrt.SourceOf(o.Source))
		}

		eff.Sources[strings.ReplaceAll(strings.TrimPrefix(ptr, "/"), "/", ".")] = src
	}

	return eff
}
