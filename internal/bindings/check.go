package bindings

import (
	"cmp"
	"fmt"
	"slices"
)

// FileViolation is a violation of one binding or rule of a wiring directory.
type FileViolation struct {
	// File is the YAML file, relative to the directory.
	File string
	// Kind is "binding" or "rule"; Name is the binding's hook or the rule's name.
	Kind, Name string
	Violation
}

// CheckDir validates every binding and rule of the wiring directory dir
// against cat, as backplane does when it keeps the directory in force. A
// directory that does not read (a missing file, YAML or a field the
// definitions do not have, a duplicate) is the error.
func CheckDir(dir string, cat Catalog) ([]FileViolation, error) {
	want, err := readWiring(dir)
	if err != nil {
		return nil, err
	}

	var out []FileViolation

	for _, hook := range inOrder(want.bindings) {
		binding := want.bindings[hook]

		vs, err := ValidateBinding(binding.binding, cat)
		if err != nil {
			return nil, fmt.Errorf("%s: binding %s: %w", binding.file, hook, err)
		}

		for _, v := range vs {
			out = append(out, FileViolation{File: binding.file, Kind: "binding", Name: hook, Violation: v})
		}
	}

	for _, id := range inOrder(want.rules) {
		rule := want.rules[id]

		vs, err := ValidateRule(rule.rule, cat)
		if err != nil {
			return nil, fmt.Errorf("%s: rule %s: %w", rule.file, rule.name, err)
		}

		for _, v := range vs {
			out = append(out, FileViolation{File: rule.file, Kind: "rule", Name: rule.name, Violation: v})
		}
	}

	slices.SortStableFunc(out, func(a, b FileViolation) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})

	return out, nil
}
