package rules_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/rules"
	"github.com/backplane-labs/backplane/internal/wire"
)

func idOf(n byte) uuid.UUID {
	var u uuid.UUID

	u[15] = n

	return u
}

func TestNames(t *testing.T) {
	t.Parallel()

	rule := uuid.MustParse("0b6f3b53-6d7c-4e27-9a55-5c8b0c2f4a10")
	if got := rules.DurableName(rule); got != "backplane__rule-0b6f3b53-6d7c-4e27-9a55-5c8b0c2f4a10" {
		t.Fatalf("durable %q", got)
	}

	if rules.RunID(rule.String(), "e1") != "rule/"+rule.String()+"/e1" ||
		rules.TestRunID("", "x") != "test/rule/draft/x" {
		t.Fatal("run ids")
	}

	if !rules.IsRunOf(rule, rules.RunID(rule.String(), "e1")) || !rules.IsRunOf(rule, rules.TestRunID(rule.String(), "x")) ||
		rules.IsRunOf(rule, "rule/"+rule.String()+"/") || rules.IsRunOf(rule, rules.RunID(idOf(1).String(), "e1")) {
		t.Fatal("IsRunOf")
	}
}

// plan compiles active rules only, keeps names for every live rule and
// says why a rule does not run.
func TestPlan(t *testing.T) {
	t.Parallel()

	active, paused, deleted, unknown := idOf(1), idOf(2), idOf(3), idOf(4)

	gone := entry(deleted, 2, bindings.Rule{})
	gone.Current.Deleted = true

	stopped := entry(paused, 1, proRule("iam.UserRegistered"))
	stopped.Paused = true

	want := rules.Plan([]bindings.RuleEntry{
		entry(active, 5, proRule("iam.UserRegistered")), stopped, gone, entry(unknown, 1, proRule("iam.Gone")),
	}, catalog(t))

	a := want[active]
	if !a.Runnable() || a.Prog() == nil || len(a.Spec()) == 0 || a.Version() != 5 || a.Stream() != "bp_iam" ||
		a.Subject() != "bp.iam.UserRegistered" || a.Durable() != rules.DurableName(active) || a.Service() != "iam" {
		t.Fatalf("active: %+v", a)
	}

	if p := want[paused]; p.Runnable() || p.Prog() != nil || p.Stream() != "bp_iam" || p.Err() != nil {
		t.Fatalf("paused: %+v", p)
	}

	if d := want[deleted]; !d.Deleted() || d.Runnable() {
		t.Fatalf("deleted: %+v", d)
	}

	var invalid *bindings.InvalidError
	if u := want[unknown]; u.Runnable() || !errors.As(u.Err(), &invalid) || u.Stream() != "bp_iam" {
		t.Fatalf("unknown event: %+v", u)
	}
}

func kinds(as []rules.Action) []string {
	out := make([]string, 0, len(as))
	for i := range as {
		out = append(out, as[i].String())
	}

	return out
}

// diff: start new active rules, update changed ones in place, stop paused
// or broken ones (consumer kept), drop deleted ones, move a rule whose
// event changed stream.
func TestDiff(t *testing.T) {
	t.Parallel()

	cat := catalog(t)
	ents := map[string]bindings.RuleEntry{}
	mk := func(name string, u uuid.UUID, version int64, r bindings.Rule, mut func(*bindings.RuleEntry)) {
		e := entry(u, version, r)
		if mut != nil {
			mut(&e)
		}

		ents[name] = e
	}

	iamRule := proRule("iam.UserRegistered")
	mk("new", idOf(1), 1, iamRule, nil)
	mk("same", idOf(2), 1, iamRule, nil)
	mk("bumped", idOf(3), 2, iamRule, nil)
	mk("paused", idOf(4), 1, iamRule, func(e *bindings.RuleEntry) { e.Paused = true })
	mk("deleted", idOf(5), 2, bindings.Rule{}, func(e *bindings.RuleEntry) { e.Current.Deleted = true })
	mk("moved", idOf(6), 2, proRule("crm.UserRegistered"), nil)
	mk("broken", idOf(7), 2, proRule("iam.Gone"), nil)
	mk("pausedIdle", idOf(8), 1, iamRule, func(e *bindings.RuleEntry) { e.Paused = true })

	list := make([]bindings.RuleEntry, 0, len(ents))
	for _, e := range ents {
		list = append(list, e)
	}

	want := rules.Plan(list, cat)

	runningAs := func(u uuid.UUID, version int64) rules.Running {
		return rules.RunningOf(rules.Plan([]bindings.RuleEntry{entry(u, version, iamRule)}, cat)[u])
	}

	cur := map[uuid.UUID]rules.Running{
		idOf(2): runningAs(idOf(2), 1),
		idOf(3): runningAs(idOf(3), 1),
		idOf(4): runningAs(idOf(4), 1),
		idOf(5): runningAs(idOf(5), 1),
		idOf(6): runningAs(idOf(6), 1),
		idOf(7): runningAs(idOf(7), 1),
		idOf(9): runningAs(idOf(9), 1), // no longer in the store
	}

	got := kinds(rules.Diff(cur, want))
	exp := []string{
		"start " + idOf(1).String(),
		"update " + idOf(3).String(),
		"stop " + idOf(4).String(),
		"drop " + idOf(5).String(),
		"drop " + idOf(6).String(), "start " + idOf(6).String(),
		"stop " + idOf(7).String(),
		"drop " + idOf(9).String(),
	}

	if !slices.Equal(got, exp) {
		t.Fatalf("diff:\n got %v\nwant %v", got, exp)
	}

	// The moved rule's drop names the old stream, its start the new one.
	actions := rules.Diff(cur, want)
	for i := range actions {
		a := &actions[i]
		if a.ID() != idOf(6) {
			continue
		}

		if a.Kind() == rules.ActDrop && a.Stream() != "bp_iam" || a.Kind() == rules.ActStart && a.Stream() != "bp_crm" {
			t.Fatalf("moved: %+v", *a)
		}
	}

	// Reconciled: nothing left to do.
	after := map[uuid.UUID]rules.Running{}

	for u, d := range want {
		if d.Runnable() {
			after[u] = rules.RunningOf(d)
		}
	}

	if rest := rules.Diff(after, want); len(rest) != 0 {
		t.Fatalf("second diff: %v", kinds(rest))
	}
}

// orphan deletes consumers of deleted rules and consumers on a stream the
// rule left — never of an unknown rule or one written by a newer version.
func TestOrphan(t *testing.T) {
	t.Parallel()

	want := map[uuid.UUID]rules.Desired{
		idOf(1): rules.NewDesired(idOf(1), 3, "bp_iam", false),
		idOf(2): rules.NewDesired(idOf(2), 3, "", true),
	}

	for _, tc := range []struct {
		name    string
		id      uuid.UUID
		stream  string
		version int64
		orphan  bool
	}{
		{"current stream", idOf(1), "bp_iam", 3, false},
		{"old stream", idOf(1), "bp_crm", 2, true},
		{"newer than seen", idOf(1), "bp_crm", 4, false},
		{"deleted", idOf(2), "bp_iam", 2, true},
		{"deleted, newer consumer", idOf(2), "bp_iam", 4, false},
		{"unknown rule", idOf(3), "bp_iam", 1, false},
	} {
		if got := rules.Orphan(want, tc.id, tc.stream, tc.version); got != tc.orphan {
			t.Errorf("%s: orphan = %v", tc.name, got)
		}
	}
}

func TestConsumerConfig(t *testing.T) {
	t.Parallel()

	d := rules.Plan([]bindings.RuleEntry{entry(idOf(1), 4, proRule("iam.UserRegistered"))}, catalog(t))[idOf(1)]
	cfg := rules.ConsumerConfig(d)

	if cfg.Durable != rules.DurableName(idOf(1)) || cfg.FilterSubject != wire.Subject("iam", "UserRegistered") ||
		cfg.MaxDeliver != -1 || cfg.Metadata[rules.MetaRule] != idOf(1).String() ||
		cfg.Metadata[rules.MetaVersion] != "4" || cfg.Metadata[wire.MetaService] != rules.Subscriber ||
		cfg.Metadata[wire.MetaEvent] != "iam.UserRegistered" {
		t.Fatalf("config: %+v", cfg)
	}

	u, v, ok := rules.RuleOf(cfg.Metadata)
	if !ok || u != idOf(1) || v != 4 {
		t.Fatalf("ruleOf: %v %v %v", u, v, ok)
	}

	if _, _, accepted := rules.RuleOf(map[string]string{rules.MetaRule: "x"}); accepted {
		t.Fatal("ruleOf accepted a bad id")
	}
}

func TestParseMeta(t *testing.T) {
	t.Parallel()

	m, err := rules.ParseMeta(headers("e1"))
	if err != nil || m.ID != "e1" || m.Source != "iam" || m.Subject != "user-1" || m.Time.IsZero() {
		t.Fatalf("meta %+v, %v", m, err)
	}

	h := headers("e2")
	h.Set(wire.HeaderTime, "yesterday")

	if badTime, err := rules.ParseMeta(h); err != nil || !badTime.Time.IsZero() {
		t.Fatalf("bad time: %+v, %v", badTime, err)
	}

	if _, err := rules.ParseMeta(headers("")); !errors.Is(err, rules.ErrNoID) {
		t.Fatalf("no id: %v", err)
	}

	if tr := rules.TraceOf(headers("e1")); len(tr) != 1 || tr["traceparent"] == "" {
		t.Fatalf("trace %v", tr)
	}

	if tr := rules.TraceOf(nats.Header{}); tr != nil {
		t.Fatalf("empty trace %v", tr)
	}
}

// A stream the engine creates for a rule is replicated as the
// installation's streams are (nats.replicas).
func TestEventStreamReplicas(t *testing.T) {
	t.Parallel()

	cfg := rules.Detached(nil).EventStream("iam")
	if cfg.Name != wire.StreamName("iam") || cfg.Replicas != 1 || cfg.Metadata[wire.MetaBy] != wire.BySubscriber {
		t.Fatalf("default: %+v", cfg)
	}

	if replicated := rules.Detached(nil, rules.StreamReplicas(3)).EventStream("iam"); replicated.Replicas != 3 {
		t.Fatalf("replicated: %+v", replicated)
	}
}
