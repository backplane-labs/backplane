package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/jackc/pgx/v5"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/store/db"
)

// Errors of saving.
var (
	ErrNotSynced    = errors.New("config: the registry has not synced with consul yet")
	ErrNoService    = errors.New("config: unknown service")
	ErrNoManifest   = errors.New("config: the service has no manifest")
	ErrNoRevision   = errors.New("config: no such revision")
	errSyncAttempts = errors.New("config: consul kv kept changing")
)

const (
	// maxSyncAttempts bounds Sync's retries after a lost CAS.
	maxSyncAttempts = 3
	// deliveryDeadline bounds the delivery that follows a save.
	deliveryDeadline = 10 * time.Second
)

// Revision is one saved override.
type Revision struct {
	Service  string
	Revision int64
	// Values: Live path -> JSON value, as saved.
	Values map[string]json.RawMessage
	// KV: Live path -> the string delivered to Consul KV.
	KV         map[string]string
	Author     string
	Comment    string
	CreatedAt  time.Time
	RollbackOf int64 // 0: not a rollback
}

// row is the shape every revision query returns.
type row struct {
	Service    string
	Revision   int64
	Overrides  json.RawMessage
	Kv         json.RawMessage
	Author     string
	Comment    string
	CreatedAt  time.Time
	RollbackOf *int64
}

func (r row) revision() (Revision, error) {
	out := Revision{
		Service: r.Service, Revision: r.Revision, Author: r.Author, Comment: r.Comment, CreatedAt: r.CreatedAt,
	}

	if r.RollbackOf != nil {
		out.RollbackOf = *r.RollbackOf
	}

	if err := json.Unmarshal(r.Overrides, &out.Values); err != nil {
		return Revision{}, fmt.Errorf("config: revision %s/%d: overrides: %w", r.Service, r.Revision, err)
	}

	if err := json.Unmarshal(r.Kv, &out.KV); err != nil {
		return Revision{}, fmt.Errorf("config: revision %s/%d: kv: %w", r.Service, r.Revision, err)
	}

	return out, nil
}

// Saved is the outcome of a save: the revision, or the violations it was
// rejected with; DeliveryErr says why a saved revision is not in KV yet.
type Saved struct {
	Revision    Revision
	Violations  []Violation
	DeliveryErr error
}

// Current is the service's current revision; ok is false before the first.
func (m *Manager) Current(ctx context.Context, service string) (Revision, bool, error) {
	r, err := m.store.Get().Q.GetCurrentConfigRevision(ctx, service)
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, false, nil
	}

	if err != nil {
		return Revision{}, false, fmt.Errorf("config: current revision of %s: %w", service, err)
	}

	rev, err := row(r).revision()

	return rev, err == nil, err
}

// Get is one revision of the service.
func (m *Manager) Get(ctx context.Context, service string, revision int64) (Revision, error) {
	r, err := m.store.Get().Q.GetConfigRevision(ctx, db.GetConfigRevisionParams{Service: service, Revision: revision})
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, fmt.Errorf("%w: %s/%d", ErrNoRevision, service, revision)
	}

	if err != nil {
		return Revision{}, fmt.Errorf("config: revision %s/%d: %w", service, revision, err)
	}

	return row(r).revision()
}

// List pages through the service's revisions, newest first: at most size
// below before (0: from the newest).
func (m *Manager) List(ctx context.Context, service string, before int64, size int) ([]Revision, error) {
	if before <= 0 {
		before = math.MaxInt64
	}

	rows, err := m.store.Get().Q.ListConfigRevisions(ctx, db.ListConfigRevisionsParams{
		Service: service, Before: before, PageSize: int64(size),
	})
	if err != nil {
		return nil, fmt.Errorf("config: revisions of %s: %w", service, err)
	}

	out := make([]Revision, 0, len(rows))

	for i := range rows {
		rev, err := row(rows[i]).revision()
		if err != nil {
			return nil, err
		}

		out = append(out, rev)
	}

	return out, nil
}

// service is the service and its latest manifest from the current
// snapshot.
func (m *Manager) service(name string) (registry.Service, error) {
	cat := m.src.Current()
	if cat.Index == 0 {
		return registry.Service{}, ErrNotSynced
	}

	svc, ok := cat.Services[name]
	if !ok {
		return registry.Service{}, fmt.Errorf("%w %q", ErrNoService, name)
	}

	if svc.Latest() == nil {
		return registry.Service{}, fmt.Errorf("%w: %q", ErrNoManifest, name)
	}

	return svc, nil
}

// Validate checks an override (Live path -> JSON text) without saving it.
func (m *Manager) Validate(service string, values map[string]string) ([]Violation, error) {
	_, violations, err := m.check(service, values)

	return violations, err
}

func (m *Manager) check(service string, values map[string]string) (override, []Violation, error) {
	svc, err := m.service(service)
	if err != nil {
		return override{}, nil, err
	}

	return m.engines.check(svc, values)
}

// Save validates the override and saves it as the service's next
// revision, then delivers it to Consul KV. Rejected: nothing is saved.
func (m *Manager) Save(ctx context.Context, service string, values map[string]string, comment string) (Saved, error) {
	return m.save(ctx, service, values, comment, 0)
}

// Rollback saves the values of an older revision as the next one.
func (m *Manager) Rollback(ctx context.Context, service string, revision int64, comment string) (Saved, error) {
	old, err := m.Get(ctx, service, revision)
	if err != nil {
		return Saved{}, err
	}

	values := make(map[string]string, len(old.Values))
	for p, v := range old.Values {
		values[p] = string(v)
	}

	return m.save(ctx, service, values, comment, revision)
}

func (m *Manager) save(
	ctx context.Context, service string, values map[string]string, comment string, rollbackOf int64,
) (Saved, error) {
	o, violations, err := m.check(service, values)
	if err != nil {
		return Saved{}, err
	}

	author := m.author(ctx)

	if len(violations) > 0 {
		m.metrics.revision(ctx, "rejected")
		m.Log().Info("config override rejected", xlog.String("svc", service), xlog.String("author", author),
			xlog.Int("violations", len(violations)), xlog.String("first", violations[0].String()))

		return Saved{Violations: violations}, nil
	}

	rev, err := m.insert(ctx, service, o, author, comment, rollbackOf)
	if err != nil {
		return Saved{}, err
	}

	m.metrics.revision(ctx, "saved")
	m.Log().Info("config revision saved", xlog.String("svc", service), xlog.Int64("revision", rev.Revision),
		xlog.String("author", author), xlog.Int("paths", len(rev.Values)), xlog.Int64("rollback_of", rollbackOf))

	out := Saved{Revision: rev}

	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryDeadline)
	defer cancel()

	if err := m.Sync(dctx, service); err != nil {
		m.metrics.failure(ctx)
		m.Log().Warn("config revision saved, delivery to consul pending", xlog.String("svc", service),
			xlog.Int64("revision", rev.Revision), xlog.Err(err))

		out.DeliveryErr = err
	}

	m.changes.notify()

	return out, nil
}

// insert stores the next revision and makes it current, in one
// serializable transaction (concurrent saves of a service retry).
func (m *Manager) insert(
	ctx context.Context, service string, o override, author, comment string, rollbackOf int64,
) (Revision, error) {
	values, err := json.Marshal(o.values)
	if err != nil {
		return Revision{}, fmt.Errorf("config: encode: %w", err)
	}

	kv, err := json.Marshal(o.kv)
	if err != nil {
		return Revision{}, fmt.Errorf("config: encode: %w", err)
	}

	var rollback *int64
	if rollbackOf > 0 {
		rollback = &rollbackOf
	}

	st := m.store.Get()

	var saved db.InsertConfigRevisionRow

	err = st.InTx(ctx, func(ctx context.Context) error {
		next, err := st.Q.NextConfigRevision(ctx, service)
		if err != nil {
			return fmt.Errorf("next revision: %w", err)
		}

		saved, err = st.Q.InsertConfigRevision(ctx, db.InsertConfigRevisionParams{
			Service: service, Revision: next.Revision, Overrides: values, Kv: kv,
			Author: author, Comment: comment, RollbackOf: rollback,
		})
		if err != nil {
			return fmt.Errorf("insert revision: %w", err)
		}

		current := db.SetConfigCurrentParams{Service: service, Revision: next.Revision}
		if err := st.Q.SetConfigCurrent(ctx, current); err != nil {
			return fmt.Errorf("set current: %w", err)
		}

		action := "config.save"
		if rollbackOf > 0 {
			action = "config.rollback"
		}

		return st.AuditControl(ctx, author, action, service, service, store.AuditDetail{
			Revision: next.Revision, RollbackOf: rollbackOf, Keys: slices.Sorted(maps.Keys(o.values)),
		})
	})
	if err != nil {
		return Revision{}, fmt.Errorf("config: save %s: %w", service, err)
	}

	return row(saved).revision()
}

// Sync makes config/<service>/ equal to the service's current revision:
// KV is read first, PostgreSQL second, and the write is conditional on
// _revision being what was read — a concurrent writer (another replica, a
// save) makes it start over.
func (m *Manager) Sync(ctx context.Context, service string) error {
	for range maxSyncAttempts {
		pairs, _, err := m.consul.KV().List(prefix(service), (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			return fmt.Errorf("config: consul kv %s: %w", prefix(service), err)
		}

		cur, ok, err := m.Current(ctx, service)
		if err != nil || !ok {
			return err
		}

		err = m.apply(ctx, target{service: service, revision: cur.Revision, kv: cur.KV}, pairsOf(service, pairs))
		if !errors.Is(err, errConflict) {
			return err
		}
	}

	return errSyncAttempts
}

// apply rewrites KV when it drifted from t, logging why.
func (m *Manager) apply(ctx context.Context, t target, pairs map[string]*api.KVPair) error {
	reason, keys := t.drift(pairs)
	if reason == "" {
		return nil
	}

	if err := write(ctx, m.consul, t.txns(pairs)); err != nil {
		return err
	}

	m.metrics.rewrite(ctx, reason)

	fields := []xlog.Field{
		xlog.String("svc", t.service), xlog.Int64("revision", t.revision), xlog.String("reason", reason),
	}

	if prev := pairs[t.revisionKey()]; prev != nil {
		fields = append(fields, xlog.String("kv_revision", string(prev.Value)))
	}

	if reason == reasonEdited {
		m.Log().Warn("config/"+t.service+"/ was edited outside backplane: overwritten from postgres",
			append(fields, xlog.String("keys", strings.Join(keys, ",")))...)
	} else {
		m.Log().Info("config delivered to consul kv", fields...)
	}

	return nil
}
