package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// Codes of violations found before the schema: the rest are schemapb's
// ErrorCode names without the ERROR_CODE_ prefix.
const (
	CodeNotLive     = "NOT_LIVE"
	CodeInvalidJSON = "INVALID_JSON"
	CodeNull        = "NULL_VALUE"
	CodeUndeclared  = "UNDECLARED"
)

// Violation is one reason an override is rejected: at a path, on an
// instance's effective configuration ("" when it does not depend on one or
// no instance is live).
type Violation struct {
	Path     string
	Instance string
	Code     string
	Message  string
}

func (v Violation) String() string {
	var b strings.Builder

	if v.Instance != "" {
		b.WriteString(v.Instance + ": ")
	}

	if v.Path != "" {
		b.WriteString(v.Path + ": ")
	}

	b.WriteString(v.Message)

	return b.String()
}

// override is an override parsed against a manifest: the JSON of every
// path (as saved) and its Consul KV encoding (as delivered).
type override struct {
	// values: Live path -> compact JSON text.
	values map[string]json.RawMessage
	// kv: Live path -> the string written to config/<service>/<key(path)>.
	kv map[string]string
}

// parse checks the paths and values of an override against the manifest's
// configuration section and encodes them for Consul KV. in maps a Live path
// to the JSON text of its value.
func parse(section *backplanev1.ConfigSection, in map[string]string) (override, []Violation) {
	live := map[string]bool{}
	for _, p := range section.GetLive() {
		live[p] = true
	}

	out := override{values: map[string]json.RawMessage{}, kv: map[string]string{}}

	var violations []Violation

	for _, path := range slices.Sorted(maps.Keys(in)) {
		if !live[path] {
			violations = append(violations, Violation{
				Path: path, Code: CodeNotLive,
				Message: "not a Live field: only " + strings.Join(section.GetLive(), ", ") + " may be overridden",
			})

			continue
		}

		value, err := decodeJSON([]byte(in[path]))
		if err != nil {
			violations = append(violations, Violation{Path: path, Code: CodeInvalidJSON, Message: err.Error()})

			continue
		}

		if value == nil {
			violations = append(violations, Violation{
				Path: path, Code: CodeNull, Message: "null: leave the path out to drop its override",
			})

			continue
		}

		if keys := section.GetKeys(); len(keys) > 0 && !slices.Contains(keys, strings.Split(path, ".")[0]) {
			violations = append(violations, Violation{Path: path, Code: CodeUndeclared, Message: "not a declared key"})

			continue
		}

		raw, err := json.Marshal(value)
		if err != nil {
			violations = append(violations, Violation{Path: path, Code: CodeInvalidJSON, Message: err.Error()})

			continue
		}

		enc, err := encodeKV(fieldAt(section.GetSchema(), strings.Split(path, ".")), value)
		if err != nil {
			violations = append(violations, Violation{Path: path, Code: CodeInvalidJSON, Message: err.Error()})

			continue
		}

		out.values[path], out.kv[path] = raw, enc
	}

	return out, violations
}

// errTrailing: more than one JSON value.
var errTrailing = errors.New("trailing data after the JSON value")

// decodeJSON decodes one JSON value, numbers as json.Number.
func decodeJSON(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()

	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errTrailing
	}

	return v, nil
}

// encodeKV is the Consul KV value of a Live field as the SDK's Consul
// source decodes it (xconf's env encoding): a container field (object,
// map, list, one-of, JSON) is its JSON text, a scalar is its text — a
// string as is, a number in decimal (integers without exponent), a bool as
// true/false, a duration as Go's duration text ("1m30s"; a number is
// nanoseconds). Without a schema the value's shape decides: objects and
// lists are JSON, the rest is text.
func encodeKV(f *sp.Schema_Field, v any) (string, error) {
	container := container(f)
	if f == nil {
		switch v.(type) {
		case map[string]any, []any:
			container = true
		}
	}

	if container {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("encode: %w", err)
		}

		return string(raw), nil
	}

	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		return scalarNumber(f, x), nil
	case map[string]any, []any:
		// A scalar field given a container: its JSON text fails the
		// schema's type check the same way the SDK would.
		raw, err := json.Marshal(x)
		if err != nil {
			return "", fmt.Errorf("encode: %w", err)
		}

		return string(raw), nil
	}

	return fmt.Sprint(v), nil
}

// scalarNumber writes an integral number of an integer field without
// fraction or exponent (1e3 -> 1000) and a number of a duration field as
// that many nanoseconds; everything else keeps its JSON text.
func scalarNumber(f *sp.Schema_Field, n json.Number) string {
	integral := f.GetInt32() != nil || f.GetInt64() != nil || f.GetUint32() != nil || f.GetUint64() != nil
	duration := f.GetDuration() != nil

	if !integral && !duration {
		return n.String()
	}

	r, ok := new(big.Rat).SetString(n.String())
	if !ok || !r.IsInt() {
		return n.String()
	}

	if duration {
		if !r.Num().IsInt64() {
			return n.String()
		}

		return time.Duration(r.Num().Int64()).String()
	}

	return r.Num().String()
}

// container reports whether the SDK's Consul source reads f's value as
// JSON (xconf env encoding).
func container(f *sp.Schema_Field) bool {
	return f.GetObject() != nil || f.GetRef() != nil || f.GetMap() != nil || f.GetList() != nil ||
		f.GetOneOf() != nil || f.GetJson() != nil
}

// fieldAt is the field of schema at path, through objects and refs; nil
// when there is none.
func fieldAt(schema *sp.Schema, path []string) *sp.Schema_Field {
	cur := schema

	for i, name := range path {
		var f *sp.Schema_Field

		for _, c := range cur.GetFields() {
			if c.GetName() == name {
				f = c

				break
			}
		}

		if f == nil {
			return nil
		}

		if i == len(path)-1 {
			return f
		}

		cur = subSchema(schema, f)
		if cur == nil {
			return nil
		}
	}

	return nil
}

// subSchema is the object schema of f: inline or by reference.
func subSchema(root *sp.Schema, f *sp.Schema_Field) *sp.Schema {
	if o := f.GetObject(); o != nil {
		return o.GetSchema()
	}

	if ref := f.GetRef(); ref != nil {
		if id := ref.GetId(); id != nil {
			return root.GetDefs()[id.GetNamespace()+"\x00"+id.GetName()+"\x00"+id.GetVersion()]
		}

		return root.GetDefs()[ref.GetName()]
	}

	return nil
}
