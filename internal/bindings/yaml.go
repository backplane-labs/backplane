package bindings

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

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

// shortRuntime lets YAML say `runtime: direct` for RUNTIME_DIRECT, and
// `history: {record: run, retention: 24h}` for HISTORY_RECORD_RUN and a
// Go duration.
func shortRuntime(tree any) {
	m, ok := tree.(map[string]any)
	if !ok {
		return
	}

	if rt, isText := m["runtime"].(string); isText && rt != "" && !strings.HasPrefix(rt, "RUNTIME_") {
		m["runtime"] = "RUNTIME_" + strings.ToUpper(rt)
	}

	h, ok := m["history"].(map[string]any)
	if !ok {
		return
	}

	if rec, isText := h["record"].(string); isText && rec != "" && !strings.HasPrefix(rec, "HISTORY_RECORD_") {
		h["record"] = "HISTORY_RECORD_" + strings.ToUpper(rec)
	}

	// protojson takes "86400s"; a Go duration ("24h", "90m") is converted.
	if ret, isText := h["retention"].(string); isText {
		if d, err := time.ParseDuration(ret); err == nil {
			h["retention"] = strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
		}
	}
}

func decodeYAML(text string, m proto.Message) error {
	var tree any
	if err := yaml.Unmarshal([]byte(text), &tree); err != nil {
		return fmt.Errorf("bindings: yaml: %w", err)
	}

	return decodeTree(tree, m)
}

// decodeTree reads m from a YAML tree.
func decodeTree(tree any, m proto.Message) error {
	shortRuntime(tree)

	data, err := json.Marshal(tree)
	if err != nil {
		return fmt.Errorf("bindings: yaml: %w", err)
	}

	if err := protojson.Unmarshal(data, m); err != nil {
		return fmt.Errorf("bindings: yaml: %w", err)
	}

	return nil
}
