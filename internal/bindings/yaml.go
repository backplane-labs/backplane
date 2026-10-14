package bindings

import (
	"encoding/json"
	"fmt"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// BindingYAML reads a binding definition from YAML: its protojson form
// written as YAML, as the console edits it.
//
//	hook: hello.Greet
//	steps:
//	  formatted:
//	    activity: formatter.Format
//	    input: {name: req.name}
//	result: {text: formatted.text}
func BindingYAML(text string) (Binding, error) {
	var m consolev1.BindingDefinition
	if err := decodeYAML(text, &m); err != nil {
		return Binding{}, err
	}

	return BindingFromPB(&m), nil
}

// RuleYAML reads a rule definition from YAML, as BindingYAML.
func RuleYAML(text string) (Rule, error) {
	var m consolev1.RuleDefinition
	if err := decodeYAML(text, &m); err != nil {
		return Rule{}, err
	}

	return RuleFromPB(&m), nil
}

func decodeYAML(text string, m proto.Message) error {
	var tree any
	if err := yaml.Unmarshal([]byte(text), &tree); err != nil {
		return fmt.Errorf("bindings: yaml: %w", err)
	}

	data, err := json.Marshal(tree)
	if err != nil {
		return fmt.Errorf("bindings: yaml: %w", err)
	}

	if err := protojson.Unmarshal(data, m); err != nil {
		return fmt.Errorf("bindings: yaml: %w", err)
	}

	return nil
}
