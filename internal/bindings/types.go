package bindings

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/google/cel-go/common/types"

	sp "github.com/gopherex/schemapb/go/schemapb"
)

// kind is what a JSON value is, as far as CEL cares.
type kind int

const (
	kindDyn kind = iota
	kindInt
	kindUint
	kindDouble
	kindBool
	kindString
	kindBytes
	kindObject
	kindList
	kindMap
)

// shape is the CEL view of a schema: its type for checking and the
// conversion of JSON values to it. Durations, timestamps, choices of mixed
// values, unions, computed and free JSON are dyn: their JSON form depends
// on the author's encoder (a Go duration is nanoseconds, a proto one "1s").
// Shapes are built once and never modified.
type shape struct {
	kind kind
	// name of an object's CEL struct type.
	name     string
	fields   map[string]*shape
	required []string // sorted
	elem     *shape   // list, map
	// secret: the schema marks the field secret; the value is hidden whole
	// and anything computed from it is secret.
	secret bool
}

//nolint:gochecknoglobals // immutable
var dynShape = &shape{kind: kindDyn}

// shapes builds the shapes of schemas, naming object types under a prefix
// and sharing defs (a recursive def is one shape).
type shapes struct {
	objects map[string]*shape
}

func newShapes() *shapes { return &shapes{objects: map[string]*shape{}} }

// of is the shape of a schema; nil schema: dyn.
func (b *shapes) of(s *sp.Schema, name string) *shape {
	if s == nil {
		return dynShape
	}

	return b.object(s, name, &defs{root: s, done: map[string]*shape{}, prefix: name})
}

type defs struct {
	root   *sp.Schema
	done   map[string]*shape
	prefix string
}

func (b *shapes) object(s *sp.Schema, name string, d *defs) *shape {
	out := &shape{kind: kindObject, name: name, fields: map[string]*shape{}}
	b.objects[name] = out

	for _, f := range s.GetFields() {
		out.fields[f.GetName()] = b.field(f, name+"."+f.GetName(), d)
		if f.GetRequired() {
			out.required = append(out.required, f.GetName())
		}
	}

	slices.Sort(out.required)

	return out
}

// field is the shape of a field; a secret field's shape is a copy marked
// secret, so a shared def stays clean for the fields that are not.
func (b *shapes) field(f *sp.Schema_Field, name string, d *defs) *shape {
	s := b.fieldKind(f, name, d)
	if !f.GetSecret() {
		return s
	}

	marked := *s
	marked.secret = true

	return &marked
}

func (b *shapes) fieldKind(f *sp.Schema_Field, name string, d *defs) *shape {
	switch {
	case f.GetFloat() != nil, f.GetDouble() != nil:
		return &shape{kind: kindDouble}
	case f.GetInt32() != nil, f.GetInt64() != nil:
		return &shape{kind: kindInt}
	case f.GetUint32() != nil, f.GetUint64() != nil:
		return &shape{kind: kindUint}
	case f.GetBool() != nil:
		return &shape{kind: kindBool}
	case f.GetString_() != nil:
		return &shape{kind: kindString}
	case f.GetBytes() != nil:
		return &shape{kind: kindBytes}
	case f.GetChoice() != nil:
		return choiceShape(f.GetChoice())
	case f.GetList() != nil:
		items := f.GetList().GetItems()
		if len(items) != 1 {
			return &shape{kind: kindList, elem: dynShape}
		}

		return &shape{kind: kindList, elem: b.field(items[0], name+"[]", d)}
	case f.GetObject() != nil:
		if f.GetObject().GetSchema() == nil {
			return &shape{kind: kindMap, elem: dynShape}
		}

		return b.object(f.GetObject().GetSchema(), name, d)
	case f.GetMap() != nil:
		return b.mapShape(f.GetMap(), name, d)
	case f.GetRef() != nil:
		return b.ref(f.GetRef(), d)
	default:
		return dynShape
	}
}

func (b *shapes) mapShape(m *sp.Schema_Field_Map, name string, d *defs) *shape {
	switch {
	case m.GetValueSchema() != nil:
		return &shape{kind: kindMap, elem: b.object(m.GetValueSchema(), name+"{}", d)}
	case m.GetValueField() != nil:
		return &shape{kind: kindMap, elem: b.field(m.GetValueField(), name+"{}", d)}
	default:
		return &shape{kind: kindMap, elem: dynShape}
	}
}

func (b *shapes) ref(r *sp.Schema_Field_Ref, d *defs) *shape {
	key := r.GetName()
	if id := r.GetId(); id != nil {
		key = id.GetNamespace() + "\x00" + id.GetName() + "\x00" + id.GetVersion()
	}

	if s, ok := d.done[key]; ok {
		return s
	}

	def := d.root.GetDefs()[key]
	if def == nil {
		return dynShape
	}

	// Registered before its fields: a def that refers to itself finds it.
	name := d.prefix + ".$" + strings.ReplaceAll(key, "\x00", "/")
	out := &shape{kind: kindObject, name: name, fields: map[string]*shape{}}
	d.done[key] = out
	b.objects[name] = out

	for _, f := range def.GetFields() {
		out.fields[f.GetName()] = b.field(f, name+"."+f.GetName(), d)
		if f.GetRequired() {
			out.required = append(out.required, f.GetName())
		}
	}

	slices.Sort(out.required)

	return out
}

// choiceShape: string when every option is a string, dyn otherwise.
func choiceShape(c *sp.Schema_Field_Choice) *shape {
	if len(c.GetOptions()) == 0 {
		return dynShape
	}

	for _, o := range c.GetOptions() {
		if _, ok := o.GetValue().GetKind().(*sp.Value_StringValue); !ok {
			return dynShape
		}
	}

	return &shape{kind: kindString}
}

// celType is the CEL type values of the shape check against.
func (s *shape) celType() *types.Type {
	switch s.kind {
	case kindInt:
		return types.IntType
	case kindUint:
		return types.UintType
	case kindDouble:
		return types.DoubleType
	case kindBool:
		return types.BoolType
	case kindString:
		return types.StringType
	case kindBytes:
		return types.BytesType
	case kindObject:
		return types.NewObjectType(s.name)
	case kindList:
		return types.NewListType(s.elem.celType())
	case kindMap:
		return types.NewMapType(types.StringType, s.elem.celType())
	default:
		return types.DynType
	}
}

func (s *shape) String() string {
	switch s.kind {
	case kindObject:
		return "object"
	case kindList:
		return "list(" + s.elem.String() + ")"
	case kindMap:
		return "map(string, " + s.elem.String() + ")"
	default:
		return s.celType().String()
	}
}

// provider knows the object types of the shapes besides CEL's own.
type provider struct {
	*types.Registry

	objects map[string]*shape
}

func (p *provider) FindStructType(name string) (*types.Type, bool) {
	if _, ok := p.objects[name]; ok {
		return types.NewTypeTypeWithParam(types.NewObjectType(name)), true
	}

	return p.Registry.FindStructType(name)
}

func (p *provider) FindStructFieldNames(name string) ([]string, bool) {
	if s, ok := p.objects[name]; ok {
		return slices.Sorted(maps.Keys(s.fields)), true
	}

	return p.Registry.FindStructFieldNames(name)
}

// FindStructFieldType: no IsSet/GetFrom, so evaluation selects fields of
// the map values by key.
func (p *provider) FindStructFieldType(name, field string) (*types.FieldType, bool) {
	if s, ok := p.objects[name]; ok {
		f, declared := s.fields[field]
		if !declared {
			return nil, false
		}

		return &types.FieldType{Type: f.celType()}, true
	}

	return p.Registry.FindStructFieldType(name, field)
}

// decodeJSON reads a payload keeping numbers exact; empty is null.
func decodeJSON(data []byte) (any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil //nolint:nilnil // an empty payload is null
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}

	if dec.More() {
		return nil, errors.New("json: data after the value") //nolint:err113 // one-off
	}

	return v, nil
}

// convert turns a decoded JSON value into what CEL expects of the shape:
// integers of int and uint fields (numbers or protojson's strings) as
// int64 and uint64, doubles as float64, bytes from base64. A value that
// does not fit stays as JSON gave it: evaluation then fails where an
// expression uses it, with CEL's message. Without a schema a number is an
// int when its text is an integer, a double otherwise.
//

func convert(s *shape, v any) any {
	switch x := v.(type) {
	case json.Number:
		return convertNumber(s, x)
	case string:
		return convertString(s, x)
	case map[string]any:
		out := make(map[string]any, len(x))

		for k, item := range x {
			switch {
			case s.kind == kindObject && s.fields[k] != nil:
				out[k] = convert(s.fields[k], item)
			case s.kind == kindMap:
				out[k] = convert(s.elem, item)
			default:
				out[k] = convert(dynShape, item)
			}
		}

		return out
	case []any:
		elem := dynShape
		if s.kind == kindList {
			elem = s.elem
		}

		out := make([]any, len(x))
		for i, item := range x {
			out[i] = convert(elem, item)
		}

		return out
	default:
		return v
	}
}

func convertNumber(s *shape, n json.Number) any {
	var (
		out       any
		converted bool
	)

	switch s.kind {
	case kindInt:
		out, converted = numberInt(n)
	case kindUint:
		out, converted = numberUint(n)
	case kindDouble:
		if f, err := n.Float64(); err == nil {
			return f
		}
	default:
	}

	if converted {
		return out
	}

	if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return i
	}

	if f, err := n.Float64(); err == nil {
		return f
	}

	return n.String()
}

// numberInt is n as an int64 when it is a whole number in range.
func numberInt(n json.Number) (int64, bool) {
	if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return i, true
	}

	if f, err := n.Float64(); err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<63 {
		return int64(f), true
	}

	return 0, false
}

// numberUint is n as a uint64 when it is a non-negative whole number in
// range.
func numberUint(n json.Number) (uint64, bool) {
	if u, err := strconv.ParseUint(n.String(), 10, 64); err == nil {
		return u, true
	}

	if f, err := n.Float64(); err == nil && f == math.Trunc(f) && f >= 0 && f < 1<<64 {
		return uint64(f), true
	}

	return 0, false
}

func convertString(s *shape, str string) any {
	switch s.kind {
	case kindInt:
		if i, err := strconv.ParseInt(str, 10, 64); err == nil {
			return i
		}
	case kindUint:
		if u, err := strconv.ParseUint(str, 10, 64); err == nil {
			return u
		}
	case kindDouble:
		switch str {
		case "NaN":
			return math.NaN()
		case "Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		}

		if f, err := strconv.ParseFloat(str, 64); err == nil {
			return f
		}
	case kindBytes:
		if b, err := base64.StdEncoding.DecodeString(str); err == nil {
			return b
		}

		if b, err := base64.URLEncoding.DecodeString(str); err == nil {
			return b
		}
	default:
	}

	return str
}
