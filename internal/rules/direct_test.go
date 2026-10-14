package rules_test

import (
	"testing"

	"github.com/google/uuid"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/rules"
)

// Without Temporal, a rule on the temporal runtime is planned broken and
// says why; one on the direct runtime is runnable.
func TestPlanWithoutTemporal(t *testing.T) {
	t.Parallel()

	cat := bindings.Manifests{
		{Service: "iam", Version: "1.0.0", Events: []*backplanev1.Event{{Name: "UserRegistered"}}},
		{Service: "billing", Version: "1.0.0", Activities: []*backplanev1.Activity{
			{Name: "Charge", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY},
		}},
	}

	temporal, direct := uuid.New(), uuid.New()
	entries := []bindings.RuleEntry{
		{ID: temporal, Current: bindings.RuleVersion{RuleID: temporal, Version: 1, Rule: ruleOf(t,
			"event: iam.UserRegistered\nruntime: temporal\nsteps: {c: {activity: billing.Charge}}\n")}},
		{ID: direct, Current: bindings.RuleVersion{RuleID: direct, Version: 1, Rule: ruleOf(t,
			"event: iam.UserRegistered\nruntime: direct\nsteps: {c: {activity: billing.Charge}}\n")}},
	}

	with := rules.PlanWithTemporal(entries, cat, true)
	if with[temporal].Err() != nil || with[direct].Err() != nil {
		t.Fatalf("with temporal: %v %v", with[temporal].Err(), with[direct].Err())
	}

	without := rules.PlanWithTemporal(entries, cat, false)
	if without[temporal].Err() == nil || without[direct].Err() != nil {
		t.Fatalf("without temporal: %v %v", without[temporal].Err(), without[direct].Err())
	}

	if p := without[direct].Program(); p == nil || p.Runtime() != consolev1.Runtime_RUNTIME_DIRECT {
		t.Fatalf("direct program %v", p)
	}
}

func ruleOf(t *testing.T, text string) bindings.Rule {
	t.Helper()

	r, err := bindings.RuleYAML(text)
	if err != nil {
		t.Fatal(err)
	}

	return r
}
