// Package redact hides the secret fields of payloads before the console
// sees them: the input and output of hooks, activities and workflows and
// the payloads of events, by the schemas their manifests declare, and the
// inputs of binding steps by the schemas the compiler derives from what
// their expressions read.
//
// Masking is for display. It changes JSON text going to the console and
// nothing stored: Temporal history, NATS streams and the services' own
// logs hold the payloads as they were.
package redact

import (
	"bytes"
	"encoding/json"

	sp "github.com/gopherex/schemapb/go/schemapb"
)

// Hidden replaces a secret value.
const Hidden = "***"

// hidden is Hidden as JSON text.
var hidden = []byte(`"` + Hidden + `"`) //nolint:gochecknoglobals // constant

// All is data hidden whole: Hidden as a JSON string. Empty stays empty:
// there was nothing to show.
func All(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}

	return bytes.Clone(hidden)
}

// Mask is data (JSON) with every field schema marks secret replaced by
// Hidden, through objects, refs, lists, maps and one-ofs. Without a
// schema, or when data is not a JSON object, data returns as is: nothing
// says what is secret. The result is JSON with sorted keys.
func Mask(schema *sp.Schema, data []byte) []byte {
	if schema == nil || !hasSecret(schema, schema.GetDefs(), map[*sp.Schema]bool{}) || len(data) == 0 {
		return data
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return data
	}

	obj, ok := v.(map[string]any)
	if !ok {
		return data
	}

	maskObject(schema, obj, schema.GetDefs())

	out, err := marshal(obj)
	if err != nil {
		return data
	}

	return out
}

// MaskString is Mask over JSON text.
func MaskString(schema *sp.Schema, text string) string {
	if text == "" {
		return ""
	}

	return string(Mask(schema, []byte(text)))
}

// hasSecret reports whether any field of the schema, or of what it
// refers to, is secret: a schema without one leaves data untouched.
func hasSecret(s *sp.Schema, defs map[string]*sp.Schema, seen map[*sp.Schema]bool) bool {
	if s == nil || seen[s] {
		return false
	}

	seen[s] = true

	for _, f := range s.GetFields() {
		if fieldHasSecret(f, defs, seen) {
			return true
		}
	}

	return false
}

func fieldHasSecret(f *sp.Schema_Field, defs map[string]*sp.Schema, seen map[*sp.Schema]bool) bool {
	if f.GetSecret() {
		return true
	}

	switch {
	case f.GetObject() != nil:
		return hasSecret(f.GetObject().GetSchema(), defs, seen)
	case f.GetRef() != nil:
		return hasSecret(defs[refKey(f.GetRef())], defs, seen)
	case f.GetOneOf() != nil:
		for _, v := range f.GetOneOf().GetVariants() {
			if hasSecret(v, defs, seen) {
				return true
			}
		}
	case f.GetList() != nil:
		for _, item := range f.GetList().GetItems() {
			if fieldHasSecret(item, defs, seen) {
				return true
			}
		}
	case f.GetMap() != nil:
		m := f.GetMap()

		if hasSecret(m.GetValueSchema(), defs, seen) {
			return true
		}

		return m.GetValueField() != nil && fieldHasSecret(m.GetValueField(), defs, seen)
	}

	return false
}

func maskObject(s *sp.Schema, obj map[string]any, defs map[string]*sp.Schema) {
	for _, f := range s.GetFields() {
		if v, present := obj[f.GetName()]; present {
			obj[f.GetName()] = maskValue(f, v, defs)
		}
	}
}

func maskValue(f *sp.Schema_Field, v any, defs map[string]*sp.Schema) any {
	if f.GetSecret() {
		return Hidden
	}

	if v == nil {
		return nil
	}

	switch {
	case f.GetObject() != nil:
		return maskNested(f.GetObject().GetSchema(), v, defs)
	case f.GetRef() != nil:
		return maskNested(defs[refKey(f.GetRef())], v, defs)
	case f.GetOneOf() != nil:
		return maskOneOf(f.GetOneOf(), v, defs)
	case f.GetList() != nil:
		return maskList(f.GetList(), v, defs)
	case f.GetMap() != nil:
		return maskMap(f.GetMap(), v, defs)
	default:
		return v
	}
}

// maskNested masks an object value by its schema; a value that is not an
// object, or a schema that is unknown, stays as is: nothing of it is
// declared secret.
func maskNested(s *sp.Schema, v any, defs map[string]*sp.Schema) any {
	obj, ok := v.(map[string]any)
	if !ok || s == nil {
		return v
	}

	maskObject(s, obj, defs)

	return obj
}

// maskOneOf masks by the variant the discriminator selects; a value whose
// variant cannot be told is hidden whole when any variant has a secret.
func maskOneOf(oo *sp.Schema_Field_OneOf, v any, defs map[string]*sp.Schema) any {
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}

	disc, _ := obj[oo.GetDiscriminator()].(string)

	variant, known := oo.GetVariants()[disc]
	if !known {
		for _, candidate := range oo.GetVariants() {
			if hasSecret(candidate, defs, map[*sp.Schema]bool{}) {
				return Hidden
			}
		}

		return v
	}

	maskObject(variant, obj, defs)

	return obj
}

func maskList(l *sp.Schema_Field_List, v any, defs map[string]*sp.Schema) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}

	items := l.GetItems()

	for i, item := range list {
		var def *sp.Schema_Field

		switch {
		case len(items) == 1:
			def = items[0]
		case i < len(items):
			def = items[i]
		}

		if def != nil {
			list[i] = maskValue(def, item, defs)
		}
	}

	return list
}

func maskMap(m *sp.Schema_Field_Map, v any, defs map[string]*sp.Schema) any {
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}

	for key, item := range obj {
		switch {
		case m.GetValueField() != nil:
			obj[key] = maskValue(m.GetValueField(), item, defs)
		case m.GetValueSchema() != nil:
			obj[key] = maskNested(m.GetValueSchema(), item, defs)
		}
	}

	return obj
}

// refKey is the defs key of a ref, as schemapb composes it.
func refKey(r *sp.Schema_Field_Ref) string {
	if id := r.GetId(); id != nil {
		return id.GetNamespace() + "\x00" + id.GetName() + "\x00" + id.GetVersion()
	}

	return r.GetName()
}

// marshal is JSON with sorted keys and no HTML escaping, as the programs
// of bindings write their values.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, err //nolint:wrapcheck // callers fall back to the input
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
