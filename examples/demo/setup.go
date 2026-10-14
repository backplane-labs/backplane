// Package demo installs the example's cross-service binding and event rule
// through the public console API. It is shared by the setup command and the
// two-replica conformance test.
package demo

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
)

// The example's definitions, as the console's YAML editor shows them.
const (
	Binding = `hook: hello.Greet
description: Greets through the formatter service.
steps:
  formatted:
    activity: formatter.Format
    input: {name: req.name}
result: {text: formatted.text}
`
	Rule = `event: hello.Greeted
description: Records every greeting except "skip".
when: event.name != "skip"
steps:
  recorded:
    activity: formatter.Record
    input: {name: event.name, text: event.text}
`
	RuleName = "hello-to-formatter"
)

var ErrDefinition = errors.New("example definition rejected")

// Call is a unary console RPC.
type Call func(context.Context, string, proto.Message, proto.Message) error

// Install saves the example binding and creates or updates its named rule.
// Re-running records new versions but never adds another active example rule.
func Install(ctx context.Context, call Call) (string, error) {
	binding, err := bindings.BindingYAML(Binding)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDefinition, err)
	}

	var saved consolev1.SaveBindingResponse
	if err := call(ctx, "/backplane.console.v1.BindingService/SaveBinding",
		&consolev1.SaveBindingRequest{Definition: binding.PB(), Comment: "hello + formatter example"},
		&saved); err != nil {
		return "", fmt.Errorf("save binding: %w", err)
	}

	if len(saved.GetViolations()) > 0 || saved.GetVersion() == nil {
		return "", fmt.Errorf("%w: binding validation: %v", ErrDefinition, saved.GetViolations())
	}

	return installRule(ctx, call)
}

func installRule(ctx context.Context, call Call) (string, error) {
	rule, err := bindings.RuleYAML(Rule)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDefinition, err)
	}

	var listed consolev1.ListRulesResponse
	if err := call(ctx, "/backplane.console.v1.RuleService/ListRules",
		&consolev1.ListRulesRequest{}, &listed); err != nil {
		return "", fmt.Errorf("list rules: %w", err)
	}

	var id string

	for _, r := range listed.GetRules() {
		if r.GetCurrent().GetName() == RuleName {
			id = r.GetId()
			break
		}
	}

	var saved consolev1.SaveRuleResponse
	if err := call(ctx, "/backplane.console.v1.RuleService/SaveRule", &consolev1.SaveRuleRequest{
		Id: id, Name: RuleName, Definition: rule.PB(), Comment: "hello + formatter example",
	}, &saved); err != nil {
		return "", fmt.Errorf("save rule: %w", err)
	}

	if len(saved.GetViolations()) > 0 || saved.GetVersion() == nil {
		return "", fmt.Errorf("%w: rule validation: %v", ErrDefinition, saved.GetViolations())
	}

	id = saved.GetVersion().GetRuleId()

	var resumed consolev1.ResumeRuleResponse
	if err := call(ctx, "/backplane.console.v1.RuleService/ResumeRule",
		&consolev1.ResumeRuleRequest{Id: id}, &resumed); err != nil {
		return "", fmt.Errorf("resume rule: %w", err)
	}

	return id, nil
}
