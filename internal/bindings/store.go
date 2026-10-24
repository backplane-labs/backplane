package bindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/store/db"
)

// bindingRow is the shape of every binding version query.
type bindingRow struct {
	Hook       string
	Version    int64
	Definition json.RawMessage
	Author     string
	Comment    string
	CreatedAt  time.Time
	RollbackOf *int64
	ManagedBy  *string
}

func (r bindingRow) version() (BindingVersion, error) {
	out := BindingVersion{
		Hook: r.Hook, Version: r.Version, Author: r.Author, Comment: r.Comment, CreatedAt: r.CreatedAt,
		Deleted: r.Definition == nil, ManagedBy: deref(r.ManagedBy),
	}

	if r.RollbackOf != nil {
		out.RollbackOf = *r.RollbackOf
	}

	if !out.Deleted {
		b, err := decodeBinding(r.Definition)
		if err != nil {
			return BindingVersion{}, fmt.Errorf("%s/%d: %w", r.Hook, r.Version, err)
		}

		out.Binding = b
	}

	return out, nil
}

// ruleVersionRow is the shape of every rule version query.
type ruleVersionRow struct {
	RuleID     uuid.UUID
	Version    int64
	Name       string
	Definition json.RawMessage
	Author     string
	Comment    string
	CreatedAt  time.Time
	RollbackOf *int64
	ManagedBy  *string
}

func (r ruleVersionRow) version() (RuleVersion, error) {
	out := RuleVersion{
		RuleID: r.RuleID, Version: r.Version, Name: r.Name, Author: r.Author, Comment: r.Comment,
		CreatedAt: r.CreatedAt, Deleted: r.Definition == nil, ManagedBy: deref(r.ManagedBy),
	}

	if r.RollbackOf != nil {
		out.RollbackOf = *r.RollbackOf
	}

	if !out.Deleted {
		rule, err := decodeRule(r.Definition)
		if err != nil {
			return RuleVersion{}, fmt.Errorf("rule %s/%d: %w", r.RuleID, r.Version, err)
		}

		out.Rule = rule
	}

	return out, nil
}

// ruleRow is the shape of the current-rule queries.
type ruleRow struct {
	ID               uuid.UUID
	Paused           bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Version          int64
	Name             string
	Definition       json.RawMessage
	Author           string
	Comment          string
	VersionCreatedAt time.Time
	RollbackOf       *int64
	ManagedBy        *string
}

func (r ruleRow) entry() (RuleEntry, error) {
	cur, err := ruleVersionRow{
		RuleID: r.ID, Version: r.Version, Name: r.Name, Definition: r.Definition, Author: r.Author,
		Comment: r.Comment, CreatedAt: r.VersionCreatedAt, RollbackOf: r.RollbackOf, ManagedBy: r.ManagedBy,
	}.version()
	if err != nil {
		return RuleEntry{}, err
	}

	return RuleEntry{ID: r.ID, Paused: r.Paused, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Current: cur}, nil
}

// Saved is the outcome of a save: the version, or the violations the
// definition was rejected with.
type Saved struct {
	Version    BindingVersion
	Violations []Violation
}

// SavedRule is the outcome of a rule's save.
type SavedRule struct {
	Version    RuleVersion
	Violations []Violation
}

// Base is the version an edit started from. The zero Base skips the
// check; At(v) refuses the edit with ErrConflict when the current version
// is another one (0: none yet).
type Base struct {
	version int64
	set     bool
}

// At is the base version v.
func At(v int64) Base { return Base{version: v, set: true} }

// check is ErrConflict when the edit started from another version than
// current.
func (b Base) check(current int64) error {
	if !b.set || b.version == current {
		return nil
	}

	return fmt.Errorf("%w: the current version is %d, the edit started from %d", ErrConflict, current, b.version)
}

// writer is who writes a version: the console (the zero writer), refused
// while the current version is managed by a wiring file, or the wiring
// files (files), which write over anything; file is the new version's
// managed_by ("" for the tombstone of an entry a file no longer holds).
type writer struct {
	files bool
	file  string
}

// managedBy is the managed_by column of w's version.
func (w writer) managedBy() *string {
	if w.file == "" {
		return nil
	}

	return &w.file
}

// allowed is ErrManaged when w is the console and current is managed.
func (w writer) allowed(what string, current *string) error {
	if w.files || deref(current) == "" {
		return nil
	}

	return fmt.Errorf("%w: %s is defined in %s; change the file", ErrManaged, what, *current)
}

// authorOf is the author of w's version.
func (m *Manager) authorOf(ctx context.Context, w writer) string {
	if w.files {
		return FilesAuthor
	}

	return m.author(ctx)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

func rollbackOf(v int64) *int64 {
	if v <= 0 {
		return nil
	}

	return &v
}

func pageBefore(before int64) int64 {
	if before <= 0 {
		return math.MaxInt64
	}

	return before
}

// ---- bindings ---------------------------------------------------------------

// Current is the hook's current version; ok is false before the first
// save. A tombstone is current after a delete.
func (m *Manager) Current(ctx context.Context, hook string) (BindingVersion, bool, error) {
	r, err := m.store.Get().Q.GetCurrentBinding(ctx, hook)
	if errors.Is(err, pgx.ErrNoRows) {
		return BindingVersion{}, false, nil
	}

	if err != nil {
		return BindingVersion{}, false, fmt.Errorf("bindings: current of %s: %w", hook, err)
	}

	v, err := bindingRow(r).version()

	return v, err == nil, err
}

// Active is the binding in force for the hook and its version:
// ErrNoBinding when never saved or deleted. The executor's lookup: it
// answers from the snapshot, which a save on this replica refreshes before
// it returns and one on another replica within its notification (the poll
// interval at worst); the store is read only before the first snapshot.
func (m *Manager) Active(ctx context.Context, hook string) (Binding, int64, error) {
	var (
		v   BindingVersion
		ok  bool
		err error
	)

	if s := m.snap.Load(); s != nil {
		v, ok = s.byHook[hook]
	} else {
		v, ok, err = m.Current(ctx, hook)
	}

	if err != nil {
		return Binding{}, 0, err
	}

	if !ok || v.Deleted {
		return Binding{}, 0, fmt.Errorf("%w for %s", ErrNoBinding, hook)
	}

	return v.Binding, v.Version, nil
}

// Version is one version of the hook's binding; 0: the current one.
func (m *Manager) Version(ctx context.Context, hook string, version int64) (BindingVersion, error) {
	if version == 0 {
		v, ok, err := m.Current(ctx, hook)
		if err == nil && !ok {
			err = fmt.Errorf("%w for %s", ErrNoBinding, hook)
		}

		return v, err
	}

	r, err := m.store.Get().Q.GetBindingVersion(ctx, db.GetBindingVersionParams{Hook: hook, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return BindingVersion{}, fmt.Errorf("%w: %s/%d", ErrNoVersion, hook, version)
	}

	if err != nil {
		return BindingVersion{}, fmt.Errorf("bindings: %s/%d: %w", hook, version, err)
	}

	return bindingRow(r).version()
}

// Versions pages through the hook's versions, newest first: at most size
// below before (0: from the newest).
func (m *Manager) Versions(ctx context.Context, hook string, before int64, size int) ([]BindingVersion, error) {
	rows, err := m.store.Get().Q.ListBindingVersions(ctx, db.ListBindingVersionsParams{
		Hook: hook, Before: pageBefore(before), PageSize: int64(size),
	})
	if err != nil {
		return nil, fmt.Errorf("bindings: versions of %s: %w", hook, err)
	}

	out := make([]BindingVersion, 0, len(rows))

	for i := range rows {
		v, err := bindingRow(rows[i]).version()
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

// CurrentAll is the current version of every hook ever bound, by hook.
func (m *Manager) CurrentAll(ctx context.Context) ([]BindingVersion, error) {
	rows, err := m.store.Get().Q.ListCurrentBindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("bindings: current: %w", err)
	}

	out := make([]BindingVersion, 0, len(rows))

	for i := range rows {
		v, err := bindingRow(rows[i]).version()
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

// Validate checks b against the latest manifests without saving it.
func (m *Manager) Validate(b Binding) ([]Violation, error) {
	cat, err := m.Catalog()
	if err != nil {
		return nil, err
	}

	return ValidateBinding(b, cat)
}

// Save validates b and saves it as the next version of its hook's
// binding. Rejected: nothing is saved.
func (m *Manager) Save(ctx context.Context, b Binding, comment string, base Base) (Saved, error) {
	return m.save(ctx, b, comment, 0, base, writer{})
}

// Rollback saves the definition of an older version as the next one
// (validated again: manifests may have changed); rolling back to a
// tombstone deletes.
func (m *Manager) Rollback(
	ctx context.Context, hook string, version int64, comment string, base Base,
) (Saved, error) {
	old, err := m.Version(ctx, hook, version)
	if err != nil {
		return Saved{}, err
	}

	if old.Deleted {
		v, err := m.insertBinding(ctx, hook, nil, m.author(ctx), comment, version, base, writer{})

		return Saved{Version: v}, err
	}

	return m.save(ctx, old.Binding, comment, version, base, writer{})
}

// Delete saves a tombstone: the hook becomes unbound. ErrNoBinding when it
// has no binding in force.
func (m *Manager) Delete(ctx context.Context, hook, comment string, base Base) (BindingVersion, error) {
	if _, _, err := m.Active(ctx, hook); err != nil {
		return BindingVersion{}, err
	}

	author := m.author(ctx)

	v, err := m.insertBinding(ctx, hook, nil, author, comment, 0, base, writer{})
	if err != nil {
		return BindingVersion{}, err
	}

	m.Log().Info("binding deleted", xlog.String("hook", hook), xlog.Int64("version", v.Version),
		xlog.String("author", author))

	return v, nil
}

func (m *Manager) save(
	ctx context.Context, b Binding, comment string, rollback int64, base Base, w writer,
) (Saved, error) {
	violations, err := m.Validate(b)
	if err != nil {
		return Saved{}, err
	}

	author := m.authorOf(ctx, w)

	if len(violations) > 0 {
		m.Log().Info("binding rejected", xlog.String("hook", b.Hook), xlog.String("author", author),
			xlog.Int("violations", len(violations)), xlog.String("first", violations[0].String()))

		return Saved{Violations: violations}, nil
	}

	def, err := encodeBinding(b)
	if err != nil {
		return Saved{}, err
	}

	v, err := m.insertBinding(ctx, b.Hook, def, author, comment, rollback, base, w)
	if err != nil {
		return Saved{}, err
	}

	m.Log().Info("binding saved", xlog.String("hook", b.Hook), xlog.Int64("version", v.Version),
		xlog.String("author", author), xlog.Int("steps", len(b.Steps)), xlog.Int64("rollback_of", rollback))

	return Saved{Version: v}, nil
}

// insertBinding stores the next version (definition nil: a tombstone) and
// makes it current, in one serializable transaction.
func (m *Manager) insertBinding(
	ctx context.Context, hook string, def json.RawMessage, author, comment string, rollback int64, base Base,
	w writer,
) (BindingVersion, error) {
	st := m.store.Get()

	var saved db.InsertBindingVersionRow

	err := st.InAuditedTx(ctx, func(ctx context.Context) error {
		current, err := st.Q.GetCurrentBinding(ctx, hook)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("current version: %w", err)
		}

		if err := w.allowed("the binding of "+hook, current.ManagedBy); err != nil {
			return err
		}

		next, err := st.Q.NextBindingVersion(ctx, hook)
		if err != nil {
			return fmt.Errorf("next version: %w", err)
		}

		if err := base.check(next.Version - 1); err != nil {
			return err
		}

		saved, err = st.Q.InsertBindingVersion(ctx, db.InsertBindingVersionParams{
			Hook: hook, Version: next.Version, Definition: def, Author: author, Comment: comment,
			RollbackOf: rollbackOf(rollback), ManagedBy: w.managedBy(),
		})
		if err != nil {
			return fmt.Errorf("insert version: %w", err)
		}

		if err := st.Q.SetBindingCurrent(ctx, db.SetBindingCurrentParams{Hook: hook, Version: next.Version}); err != nil {
			return fmt.Errorf("set current: %w", err)
		}

		action := versionAction("binding", def == nil, rollback)

		return st.AuditControl(ctx, author, action, hook, store.ServiceOf(hook), store.AuditDetail{
			Revision:   next.Version,
			RollbackOf: rollback,
		})
	})
	if err != nil {
		return BindingVersion{}, fmt.Errorf("bindings: save %s: %w", hook, err)
	}

	m.changed(ctx)

	return bindingRow(saved).version()
}

// ---- rules ------------------------------------------------------------------

// Rule is the rule with its current version: ErrNoRule when unknown.
func (m *Manager) Rule(ctx context.Context, id uuid.UUID) (RuleEntry, error) {
	r, err := m.store.Get().Q.GetCurrentRule(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuleEntry{}, fmt.Errorf("%w: %s", ErrNoRule, id)
	}

	if err != nil {
		return RuleEntry{}, fmt.Errorf("bindings: rule %s: %w", id, err)
	}

	return ruleRow(r).entry()
}

// Rules is every rule with its current version (deleted ones too), by
// name.
func (m *Manager) Rules(ctx context.Context) ([]RuleEntry, error) {
	rows, err := m.store.Get().Q.ListCurrentRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("bindings: rules: %w", err)
	}

	out := make([]RuleEntry, 0, len(rows))

	for i := range rows {
		e, err := ruleRow(rows[i]).entry()
		if err != nil {
			return nil, err
		}

		out = append(out, e)
	}

	return out, nil
}

// ActiveRules are the rules that run: not deleted, not paused. The rules
// engine's lookup (with Changes to follow it).
func (m *Manager) ActiveRules(ctx context.Context) ([]RuleEntry, error) {
	all, err := m.Rules(ctx)
	if err != nil {
		return nil, err
	}

	out := all[:0]

	for i := range all {
		if all[i].Active() {
			out = append(out, all[i])
		}
	}

	return out, nil
}

// RuleVersion is one version of a rule; 0: the current one.
func (m *Manager) RuleVersion(ctx context.Context, id uuid.UUID, version int64) (RuleVersion, error) {
	if version == 0 {
		e, err := m.Rule(ctx, id)

		return e.Current, err
	}

	r, err := m.store.Get().Q.GetRuleVersion(ctx, db.GetRuleVersionParams{RuleID: id, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return RuleVersion{}, fmt.Errorf("%w: rule %s/%d", ErrNoVersion, id, version)
	}

	if err != nil {
		return RuleVersion{}, fmt.Errorf("bindings: rule %s/%d: %w", id, version, err)
	}

	return ruleVersionRow(r).version()
}

// RuleVersions pages through a rule's versions, newest first.
func (m *Manager) RuleVersions(ctx context.Context, id uuid.UUID, before int64, size int) ([]RuleVersion, error) {
	rows, err := m.store.Get().Q.ListRuleVersions(ctx, db.ListRuleVersionsParams{
		RuleID: id, Before: pageBefore(before), PageSize: int64(size),
	})
	if err != nil {
		return nil, fmt.Errorf("bindings: versions of rule %s: %w", id, err)
	}

	out := make([]RuleVersion, 0, len(rows))

	for i := range rows {
		v, err := ruleVersionRow(rows[i]).version()
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

// ValidateRule checks r (and its name) against the latest manifests
// without saving it.
func (m *Manager) ValidateRule(name string, r Rule) ([]Violation, error) {
	cat, err := m.Catalog()
	if err != nil {
		return nil, err
	}

	vs, err := ValidateRule(r, cat)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(name) == "" {
		vs = append([]Violation{{Path: "/name", Code: CodeInvalidName, Message: "a rule needs a name"}}, vs...)
	}

	return vs, nil
}

// SaveRule validates r and saves it as the next version of the rule;
// uuid.Nil creates a rule. Rejected: nothing is saved.
func (m *Manager) SaveRule(
	ctx context.Context, id uuid.UUID, name string, r Rule, comment string, base Base,
) (SavedRule, error) {
	return m.saveRule(ctx, id, name, r, comment, 0, base, writer{})
}

// RollbackRule saves the name and definition of an older version as the
// next one; rolling back to a tombstone deletes.
func (m *Manager) RollbackRule(
	ctx context.Context, id uuid.UUID, version int64, comment string, base Base,
) (SavedRule, error) {
	old, err := m.RuleVersion(ctx, id, version)
	if err != nil {
		return SavedRule{}, err
	}

	if old.Deleted {
		v, err := m.insertRule(ctx, id, old.Name, nil, m.author(ctx), comment, version, base, writer{})

		return SavedRule{Version: v}, err
	}

	return m.saveRule(ctx, id, old.Name, old.Rule, comment, version, base, writer{})
}

// DeleteRule saves a tombstone: the rule stops. ErrDeleted when it is
// deleted already.
func (m *Manager) DeleteRule(ctx context.Context, id uuid.UUID, comment string, base Base) (RuleVersion, error) {
	e, err := m.Rule(ctx, id)
	if err != nil {
		return RuleVersion{}, err
	}

	if e.Current.Deleted {
		return RuleVersion{}, fmt.Errorf("%w: rule %s", ErrDeleted, id)
	}

	author := m.author(ctx)

	v, err := m.insertRule(ctx, id, e.Current.Name, nil, author, comment, 0, base, writer{})
	if err != nil {
		return RuleVersion{}, err
	}

	m.Log().Info("rule deleted", xlog.String("rule", id.String()), xlog.Int64("version", v.Version),
		xlog.String("author", author))

	return v, nil
}

// PauseRule sets the rule's pause: paused rules start no runs.
func (m *Manager) PauseRule(ctx context.Context, id uuid.UUID, paused bool, base Base) (RuleEntry, error) {
	st := m.store.Get()

	err := st.InAuditedTx(ctx, func(ctx context.Context) error {
		if base.set {
			current, err := st.Q.GetCurrentRule(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrNoRule, id)
			}

			if err != nil {
				return fmt.Errorf("bindings: rule %s: %w", id, err)
			}

			if err := base.check(current.Version); err != nil {
				return err
			}
		}

		if _, updateErr := st.Q.SetRulePaused(ctx, db.SetRulePausedParams{Paused: paused, ID: id}); updateErr != nil {
			if errors.Is(updateErr, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrNoRule, id)
			}

			return fmt.Errorf("bindings: pause rule %s: %w", id, updateErr)
		}

		service, err := ruleService(ctx, st, id)
		if err != nil {
			return err
		}

		return st.AuditControl(ctx, m.author(ctx), "rule.pause", id.String(), service, store.AuditDetail{Paused: &paused})
	})
	if err != nil {
		return RuleEntry{}, fmt.Errorf("bindings: pause transaction: %w", err)
	}

	m.Log().Info("rule paused", xlog.String("rule", id.String()), xlog.Bool("paused", paused),
		xlog.String("author", m.author(ctx)))
	m.changed(ctx)

	return m.Rule(ctx, id)
}

func (m *Manager) saveRule(
	ctx context.Context, id uuid.UUID, name string, r Rule, comment string, rollback int64, base Base, w writer,
) (SavedRule, error) {
	if id != uuid.Nil && !w.files {
		if _, err := m.Rule(ctx, id); err != nil {
			return SavedRule{}, err
		}
	}

	violations, err := m.ValidateRule(name, r)
	if err != nil {
		return SavedRule{}, err
	}

	author := m.authorOf(ctx, w)

	if len(violations) > 0 {
		m.Log().Info("rule rejected", xlog.String("event", r.Event), xlog.String("author", author),
			xlog.Int("violations", len(violations)), xlog.String("first", violations[0].String()))

		return SavedRule{Violations: violations}, nil
	}

	def, err := encodeRule(r)
	if err != nil {
		return SavedRule{}, err
	}

	v, err := m.insertRule(ctx, id, strings.TrimSpace(name), def, author, comment, rollback, base, w)
	if err != nil {
		return SavedRule{}, err
	}

	m.Log().Info("rule saved", xlog.String("rule", v.RuleID.String()), xlog.String("event", r.Event),
		xlog.Int64("version", v.Version), xlog.String("author", author), xlog.Int64("rollback_of", rollback))

	return SavedRule{Version: v}, nil
}

// ruleWritable creates the rule a wiring file names (in the caller's
// transaction) and refuses the console a rule a file manages.
func ruleWritable(ctx context.Context, st *store.Store, id uuid.UUID, w writer) error {
	if w.files {
		if err := st.Q.EnsureRule(ctx, id); err != nil {
			return fmt.Errorf("ensure rule: %w", err)
		}
	}

	if id == uuid.Nil {
		return nil
	}

	current, err := st.Q.GetCurrentRule(ctx, id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("current version: %w", err)
	}

	return w.allowed("rule "+current.Name, current.ManagedBy)
}

// insertRule stores the next version of the rule (creating the rule for
// uuid.Nil, or for the wiring files under the id they name; definition
// nil: a tombstone) and makes it current.
func (m *Manager) insertRule(
	ctx context.Context, id uuid.UUID, name string, def json.RawMessage, author, comment string, rollback int64,
	base Base, w writer,
) (RuleVersion, error) {
	st := m.store.Get()

	var saved db.InsertRuleVersionRow

	err := st.InAuditedTx(ctx, func(ctx context.Context) error {
		ruleID := id

		if err := ruleWritable(ctx, st, ruleID, w); err != nil {
			return err
		}

		if ruleID == uuid.Nil {
			created, err := st.Q.CreateRule(ctx)
			if err != nil {
				return fmt.Errorf("create rule: %w", err)
			}

			ruleID = created.ID
		} else if err := st.Q.TouchRule(ctx, ruleID); err != nil {
			return fmt.Errorf("touch rule: %w", err)
		}

		next, err := st.Q.NextRuleVersion(ctx, ruleID)
		if err != nil {
			return fmt.Errorf("next version: %w", err)
		}

		if id != uuid.Nil {
			if err := base.check(next.Version - 1); err != nil {
				return err
			}
		}

		saved, err = st.Q.InsertRuleVersion(ctx, db.InsertRuleVersionParams{
			RuleID: ruleID, Version: next.Version, Name: name, Definition: def, Author: author, Comment: comment,
			RollbackOf: rollbackOf(rollback), ManagedBy: w.managedBy(),
		})
		if err != nil {
			return fmt.Errorf("insert version: %w", err)
		}

		if err := st.Q.SetRuleCurrent(ctx, db.SetRuleCurrentParams{RuleID: ruleID, Version: next.Version}); err != nil {
			return fmt.Errorf("set current: %w", err)
		}

		service, err := ruleService(ctx, st, ruleID)
		if err != nil {
			return err
		}

		action := versionAction("rule", def == nil, rollback)

		return st.AuditControl(ctx, author, action, ruleID.String(), service, store.AuditDetail{
			Revision:   next.Version,
			RollbackOf: rollback,
		})
	})
	if err != nil {
		return RuleVersion{}, fmt.Errorf("bindings: save rule: %w", err)
	}

	m.changed(ctx)

	return ruleVersionRow(saved).version()
}

// ruleService is the service whose event the rule reacts to: the owner of the
// rule's latest definition's event.
func ruleService(ctx context.Context, st *store.Store, id uuid.UUID) (string, error) {
	event, err := st.Q.GetRuleEvent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("rule event: %w", err)
	}

	return store.ServiceOf(event.Event), nil
}

func versionAction(kind string, deleted bool, rollback int64) string {
	if rollback > 0 {
		return kind + ".rollback"
	}

	if deleted {
		return kind + ".delete"
	}

	return kind + ".save"
}
